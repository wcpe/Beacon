package top.wcpe.beacon.agent.adapters.messaging

import redis.clients.jedis.JedisPool
import redis.clients.jedis.JedisPubSub
import redis.clients.jedis.StreamEntryID
import redis.clients.jedis.params.XAddParams
import redis.clients.jedis.params.XReadGroupParams
import top.wcpe.beacon.agent.core.messaging.MessageTransport
import top.wcpe.beacon.agent.core.messaging.MessagingSettings
import top.wcpe.beacon.agent.core.messaging.PlayerLocator
import top.wcpe.beacon.agent.core.messaging.RosterDirectory
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 基于 Jedis 的 [MessageTransport] 实现（ADR-0016：Redis 客户端只在适配器、不经 CoreLib）。
 *
 * 信道（见 [RedisChannels]）：
 * - 收件流：`beacon:msg:{serverId}` + 消费组 = serverId（可靠送达，离线补消费、至少送达一次）。
 * - 主题：`beacon:topic:{topic}`（pub/sub，可丢）。
 * - 回信：`beacon:reply:{serverId}`（pub/sub，两端在线 + RPC 层超时兜底）。
 *
 * 故障域隔离（决策 6）：本类独立持有 [JedisPool] 与若干后台线程，与配置同步/心跳互不共享、互不阻塞。
 * fail-static（决策 9）：Redis 断时 [isConnected] 转 false、发送异常上抛由 MessageBus 报失败；
 * 消费线程退避重连，恢复后从消费组未 ack 位置续传。
 *
 * 线程：
 * - 一条「收件流消费」线程跑 XREADGROUP 阻塞读循环。
 * - 一条「pub/sub」线程跑 JedisPubSub.subscribe（订阅本服回信通道 + 各主题）。
 * 回调一律在这两条后台线程触发（绝不上 MC 主线程）。
 *
 * @param connection Redis 连接参数（Beacon 下发）
 * @param serverId   本服 serverId（决定收件流 / 消费组 / 回信通道）
 * @param settings   消息运行参数（MAXLEN、消费者名）
 * @param info       INFO 日志回调
 * @param warn       WARN 日志回调
 * @param error      ERROR 日志回调（附异常）
 */
class RedisMessageTransport(
    private val connection: RedisConnection,
    private val serverId: String,
    private val settings: MessagingSettings,
    private val info: (String) -> Unit = {},
    private val warn: (String) -> Unit = {},
    private val error: (String, Throwable?) -> Unit = { _, _ -> },
) : MessageTransport {

    private val running = AtomicBoolean(false)

    /** 命令连接池（XADD / PUBLISH / XACK / hget 等短操作借还）。 */
    @Volatile
    private var pool: JedisPool? = null

    /** 收件流消费线程。 */
    @Volatile
    private var inboxThread: Thread? = null

    /** pub/sub 订阅线程。 */
    @Volatile
    private var pubSubThread: Thread? = null

    /** 收件流回调（start 时由上层 subscribeServerInbox 注入）。 */
    @Volatile
    private var inboxHandler: ((String) -> Unit)? = null

    /** 各 pub/sub 信道（回信通道 + 主题）的回调：channel → handler。 */
    private val pubSubHandlers = ConcurrentHashMap<String, (String) -> Unit>()

    /** 活跃的 JedisPubSub（用于动态 subscribe/unsubscribe 新信道）。 */
    @Volatile
    private var activePubSub: JedisPubSub? = null

    /** 串行化「取信道快照 + 设 activePubSub」与「动态追加/对账订阅」，消除订阅激活窗口的漏订阅竞态、并避免并发写订阅连接。 */
    private val pubSubLock = Any()

    override fun start() {
        if (!running.compareAndSet(false, true)) return
        pool = buildPool()
        ensureConsumerGroup()
        startInboxConsumer()
        startPubSubLoop()
        info("跨服消息传输已启动：serverId=$serverId redis=${connection.host}:${connection.port}/${connection.database}")
    }

    override fun close() {
        if (!running.compareAndSet(true, false)) return
        try {
            activePubSub?.takeIf { it.isSubscribed }?.unsubscribe()
        } catch (t: Throwable) {
            warn("关闭 pub/sub 订阅异常：${t.message}")
        }
        inboxThread?.interrupt()
        pubSubThread?.interrupt()
        try {
            pool?.close()
        } catch (t: Throwable) {
            warn("关闭 Redis 连接池异常：${t.message}")
        }
        pool = null
        info("跨服消息传输已关闭：serverId=$serverId")
    }

    override fun isConnected(): Boolean {
        if (!running.get()) return false
        val p = pool ?: return false
        return try {
            p.resource.use { it.ping() == "PONG" }
        } catch (t: Throwable) {
            false
        }
    }

    override fun sendToServer(serverId: String, rawJson: String) {
        val key = RedisChannels.serverInbox(serverId)
        // XADD + 近似 MAXLEN 裁剪（决策 12：旧消息自动淘汰，防无限增长）。
        val params = XAddParams.xAddParams()
            .maxLen(settings.streamMaxLen)
            .approximateTrimming()
        withResource { jedis ->
            jedis.xadd(key, params, mapOf(RedisChannels.ENVELOPE_FIELD to rawJson))
        }
    }

    override fun publishTopic(topic: String, rawJson: String) {
        val channel = RedisChannels.topic(topic)
        withResource { jedis -> jedis.publish(channel, rawJson) }
    }

    override fun sendReply(replyChannel: String, rawJson: String) {
        // replyChannel 是 MessageBus 生成的逻辑名（reply:<serverId>）；映射到 Redis pub/sub 物理信道。
        val channel = mapReplyToRedisChannel(replyChannel)
        withResource { jedis -> jedis.publish(channel, rawJson) }
    }

    override fun subscribeServerInbox(onMessage: (String) -> Unit) {
        inboxHandler = onMessage
    }

    override fun subscribeReplyInbox(replyChannel: String, onMessage: (String) -> Unit) {
        val channel = mapReplyToRedisChannel(replyChannel)
        addPubSubChannel(channel, onMessage)
    }

    override fun subscribeTopic(topic: String, onMessage: (String) -> Unit) {
        val channel = RedisChannels.topic(topic)
        addPubSubChannel(channel, onMessage)
    }

    override fun unsubscribeTopic(topic: String) {
        val channel = RedisChannels.topic(topic)
        pubSubHandlers.remove(channel)
        try {
            activePubSub?.takeIf { it.isSubscribed }?.unsubscribe(channel)
        } catch (t: Throwable) {
            warn("取消订阅主题异常：topic=$topic ${t.message}")
        }
    }

    /**
     * 暴露基于本传输 Redis 连接的玩家位置解析（读名册 hash），供 MessageBus 按玩家寻址用。
     *
     * 名册由 BC 上的 beacon-proxy 维护（见 [RedisPlayerRoster]）；本端只读。
     */
    fun playerLocator(): PlayerLocator = object : PlayerLocator {
        override fun resolveServerId(playerName: String): String? {
            return try {
                withResource { jedis -> jedis.hget(RedisChannels.PLAYER_LOCATION_HASH, playerName) }
            } catch (t: Throwable) {
                warn("读玩家名册异常：player=$playerName ${t.message}")
                null
            }
        }
    }

    /**
     * 暴露基于本传输 Redis 连接的玩家位置名册全表读（FR-31 / ADR-0022），供 DiscoveryView 只读名册查询用。
     *
     * 走 HGETALL beacon:player-loc，复用本传输的 Redis 连接 / 线程（不另起连接）；
     * 异常 / 名册空 → 返回空 Map（优雅降级，绝不抛、绝不阻塞调用方）。HGETALL 须在异步线程调用（守不变量 #5）。
     */
    fun rosterDirectory(): RosterDirectory = object : RosterDirectory {
        override fun snapshot(): Map<String, String> {
            return try {
                withResource { jedis -> jedis.hgetAll(RedisChannels.PLAYER_LOCATION_HASH) } ?: emptyMap()
            } catch (t: Throwable) {
                warn("读玩家名册全表异常，降级返空：${t.message}")
                emptyMap()
            }
        }
    }

    // ---- 内部 ----

    /** 把 MessageBus 的逻辑回信名（reply:<serverId>）映射为 Redis 回信信道（beacon:reply:<serverId>）。 */
    private fun mapReplyToRedisChannel(logicalReply: String): String {
        val sid = logicalReply.removePrefix("reply:")
        return RedisChannels.replyChannel(sid)
    }

    private fun buildPool(): JedisPool = JedisPoolFactory.create(connection)

    /** 创建本服收件流的消费组（已存在则忽略 BUSYGROUP 错误）。 */
    private fun ensureConsumerGroup() {
        val key = RedisChannels.serverInbox(serverId)
        val group = RedisChannels.consumerGroup(serverId)
        try {
            pool?.resource?.use { jedis ->
                // mkStream=true：流不存在时一并创建，从 0 起（不漏历史，配合 MAXLEN 裁剪）。
                jedis.xgroupCreate(key, group, StreamEntryID("0-0"), true)
            }
        } catch (t: Throwable) {
            // 消费组已存在（BUSYGROUP）属正常重启场景，仅 debug 级忽略。
            if (t.message?.contains("BUSYGROUP") != true) {
                warn("创建消费组异常（可能已存在）：${t.message}")
            }
        }
    }

    private fun startInboxConsumer() {
        val key = RedisChannels.serverInbox(serverId)
        val group = RedisChannels.consumerGroup(serverId)
        val consumer = settings.consumerName
        val thread = Thread({ inboxLoop(key, group, consumer) }, "beacon-msg-inbox-$serverId")
        thread.isDaemon = true
        inboxThread = thread
        thread.start()
    }

    private fun inboxLoop(key: String, group: String, consumer: String) {
        val params = XReadGroupParams.xReadGroupParams().count(16).block(2000)
        while (running.get()) {
            try {
                pool?.resource?.use { jedis ->
                    // ">" 表示只取本消费者尚未投递过的新消息（消费组语义，离线期间留存的也在内）。
                    val streams = mapOf(key to StreamEntryID.UNRECEIVED_ENTRY)
                    val result = jedis.xreadGroup(group, consumer, params, streams)
                    if (result != null) {
                        for (entry in result) {
                            for (streamEntry in entry.value) {
                                val raw = streamEntry.fields[RedisChannels.ENVELOPE_FIELD]
                                if (raw != null) {
                                    dispatchInbox(raw)
                                }
                                // 处理后 ack（至少送达一次；业务侧幂等）。
                                jedis.xack(key, group, streamEntry.id)
                            }
                        }
                    }
                }
            } catch (ie: InterruptedException) {
                Thread.currentThread().interrupt()
                break
            } catch (t: Throwable) {
                if (running.get()) {
                    warn("收件流消费异常，2 秒后重试：${t.message}")
                    sleepQuiet(2000)
                }
            }
        }
    }

    private fun dispatchInbox(raw: String) {
        try {
            inboxHandler?.invoke(raw)
        } catch (t: Throwable) {
            warn("收件流回调异常，已隔离：${t.message}")
        }
    }

    private fun addPubSubChannel(channel: String, onMessage: (String) -> Unit) {
        synchronized(pubSubLock) {
            pubSubHandlers[channel] = onMessage
            val current = activePubSub
            if (current != null && current.isSubscribed) {
                // 已有活跃订阅：动态追加（与对账/快照在同一把锁下串行，避免并发写订阅连接）。
                try {
                    current.subscribe(channel)
                } catch (t: Throwable) {
                    warn("追加订阅信道异常：channel=$channel ${t.message}")
                }
            }
            // 否则：订阅线程激活后由 onSubscribe 对账补订阅本信道（消除「快照后、激活前」漏订阅窗口）。
        }
    }

    private fun startPubSubLoop() {
        val thread = Thread({ pubSubLoop() }, "beacon-msg-pubsub-$serverId")
        thread.isDaemon = true
        pubSubThread = thread
        thread.start()
    }

    private fun pubSubLoop() {
        while (running.get()) {
            // 本轮订阅的初始信道集合（快照），供 onSubscribe 对账判定「窗口内新增、未订阅」的信道。
            var initialChannels: List<String> = emptyList()
            try {
                val reconciled = AtomicBoolean(false)
                val pubSub = object : JedisPubSub() {
                    override fun onMessage(channel: String, message: String) {
                        try {
                            pubSubHandlers[channel]?.invoke(message)
                        } catch (t: Throwable) {
                            warn("pub/sub 回调异常，已隔离：channel=$channel ${t.message}")
                        }
                    }

                    override fun onSubscribe(channel: String, subscribedChannels: Int) {
                        // 订阅已激活：对账一次，补订阅在「快照后、激活前」窗口内并发加入、未进初始集合的信道。
                        if (!reconciled.compareAndSet(false, true)) return
                        synchronized(pubSubLock) {
                            if (!isSubscribed) return
                            for (ch in pubSubHandlers.keys.toList()) {
                                if (ch !in initialChannels) {
                                    try {
                                        subscribe(ch)
                                    } catch (t: Throwable) {
                                        warn("对账补订阅信道异常：channel=$ch ${t.message}")
                                    }
                                }
                            }
                        }
                    }
                }
                // 在同一把锁下原子地「设 activePubSub + 取信道快照」，与 addPubSubChannel 串行。
                synchronized(pubSubLock) {
                    initialChannels = pubSubHandlers.keys.toList()
                    if (initialChannels.isNotEmpty()) {
                        activePubSub = pubSub
                    }
                }
                if (initialChannels.isEmpty()) {
                    // 尚无任何信道（回信通道在 subscribeReplyInbox 后才登记）：短暂等待再重试，避免空订阅报错。
                    sleepQuiet(200)
                    continue
                }
                // subscribe 阻塞直到 unsubscribe。
                pool?.resource?.use { jedis ->
                    jedis.subscribe(pubSub, *initialChannels.toTypedArray())
                }
            } catch (ie: InterruptedException) {
                Thread.currentThread().interrupt()
                break
            } catch (t: Throwable) {
                if (running.get()) {
                    warn("pub/sub 订阅异常，2 秒后重连：${t.message}")
                    sleepQuiet(2000)
                }
            } finally {
                activePubSub = null
            }
        }
    }

    /** 借池执行短操作；池不可用或借还异常上抛（由 MessageBus 报失败）。 */
    private fun <T> withResource(block: (redis.clients.jedis.Jedis) -> T): T {
        val p = pool ?: throw IllegalStateException("Redis 连接池未初始化（消息模块未启动或已关闭）")
        return p.resource.use(block)
    }

    private fun sleepQuiet(ms: Long) {
        try {
            Thread.sleep(ms)
        } catch (ie: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }
}

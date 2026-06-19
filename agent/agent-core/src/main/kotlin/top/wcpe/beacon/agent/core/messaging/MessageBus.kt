package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.UUID
import java.util.concurrent.CompletableFuture
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeoutException

/**
 * 跨服消息总线（core 引擎）：信封编解码、按 type 路由分发、RPC 关联 ID 配对 + 超时、
 * 四种模式（定向 / RPC / 主题 / 按玩家寻址）的编排。
 *
 * 只依赖抽象：[MessageTransport]（搬运原始 json）、[JsonCodec]（编解码）、[PlayerLocator]（玩家寻址）。
 * 不 import 任何具体库（Redis/okhttp/kotlinx），守 ADR-0005/0016 边界。
 *
 * 线程：发送方法可在任意线程调用（内部仅做编码 + 委托 transport）。入站消息由 transport 的后台
 * 线程回调本类，handler 在该后台线程同步执行（绝不上 MC 主线程；handler 自行切回平台线程）。
 *
 * @param transport     传输端口（Redis 适配器注入；测试注入假实现）
 * @param codec         信封 json 编解码
 * @param selfServerId  本服 serverId（消费组、回信通道、source 标识用）
 * @param settings      运行参数（RPC 超时等）
 * @param playerLocator 玩家位置解析（按玩家寻址用；可空 = 不支持按玩家寻址）
 * @param scheduleTimeout 延迟调度（RPC 超时清理用）：默认 daemon 线程，壳层可注入平台 runAsyncDelayed
 * @param warn          告警日志（无法配对的回信、非法消息等）
 */
class MessageBus(
    private val transport: MessageTransport,
    private val codec: JsonCodec,
    private val selfServerId: String,
    private val settings: MessagingSettings,
    private val playerLocator: PlayerLocator? = null,
    private val scheduleTimeout: (delayMs: Long, task: () -> Unit) -> Unit = DEFAULT_SCHEDULER,
    private val warn: (String) -> Unit = {},
) {

    /** 按消息类型注册的处理器：type → handler。非 RPC 收消息后回调，返回值忽略。 */
    private val typeHandlers = ConcurrentHashMap<String, (MessageContext) -> Unit>()

    /** 主题处理器：topic → handler。 */
    private val topicHandlers = ConcurrentHashMap<String, (Message) -> Unit>()

    /** 等待回信的 RPC 请求：correlationId → Future。 */
    private val pending = ConcurrentHashMap<String, CompletableFuture<Any?>>()

    /** 本服专属回信通道名（发起 RPC 时填入 replyTo，目标据此回发）。 */
    private val replyChannel: String = "$REPLY_PREFIX$selfServerId"

    @Volatile
    private var started = false

    /**
     * 启动：连 transport、订阅本服收件流与回信通道。失败抛异常由上层降级（isAvailable 仍为 false）。
     */
    fun start() {
        transport.start()
        transport.subscribeServerInbox { raw -> onInboundRaw(raw) }
        transport.subscribeReplyInbox(replyChannel) { raw -> onReplyRaw(raw) }
        started = true
    }

    /** 关闭：失败所有挂起 Future、关 transport。 */
    fun close() {
        started = false
        failAllPending(IllegalStateException("消息总线已关闭"))
        transport.close()
    }

    /** 模块是否可用（已启动且 transport 已连上）。业务侧据此优雅降级。 */
    fun isAvailable(): Boolean = started && transport.isConnected()

    /** 注册按类型分发的处理器。重复注册同 type 覆盖前者。 */
    fun on(type: String, handler: (MessageContext) -> Unit) {
        typeHandlers[type] = handler
    }

    /**
     * 定向发送（fire-and-forget，可靠送达）：写入目标服收件流。
     *
     * @throws IllegalStateException 模块不可用
     */
    fun send(targetServerId: String, type: String, payload: Any?) {
        requireAvailable()
        val message = Message(type = type, payload = payload, source = selfServerId)
        transport.sendToServer(targetServerId, encode(message))
    }

    /**
     * 请求-响应（RPC）：发请求并返回 Future，目标回信后完成；超时则 Future 异常完成。
     *
     * @return 完成值为目标返回的 payload（泛型树）；超时抛 [TimeoutException]
     * @throws IllegalStateException 模块不可用
     */
    fun call(targetServerId: String, type: String, payload: Any?): CompletableFuture<Any?> {
        requireAvailable()
        val correlationId = UUID.randomUUID().toString()
        val future = CompletableFuture<Any?>()
        pending[correlationId] = future

        val request = Message(
            type = type,
            payload = payload,
            correlationId = correlationId,
            replyTo = replyChannel,
            source = selfServerId,
        )
        try {
            transport.sendToServer(targetServerId, encode(request))
        } catch (t: Throwable) {
            // 发送失败立刻清理，不留悬挂 Future。
            pending.remove(correlationId)
            future.completeExceptionally(t)
            return future
        }

        // 超时兜底：到点仍未完成则异常完成并清理（回信通道不持久化，过期即弃）。
        scheduleTimeout(settings.rpcTimeoutMs) {
            val removed = pending.remove(correlationId)
            if (removed != null && !removed.isDone) {
                removed.completeExceptionally(
                    TimeoutException("RPC 超时：target=$targetServerId type=$type 超过 ${settings.rpcTimeoutMs}ms 未收回信"),
                )
            }
        }
        return future
    }

    /**
     * 主题发布（可丢，pub/sub）：当前无订阅者即丢弃。
     *
     * @throws IllegalStateException 模块不可用
     */
    fun publish(topic: String, payload: Any?) {
        requireAvailable()
        val message = Message(type = topic, payload = payload, source = selfServerId)
        transport.publishTopic(topic, encode(message))
    }

    /**
     * 主题订阅：注册处理器并向 transport 订阅。重复订阅同 topic 覆盖前者 handler。
     *
     * @throws IllegalStateException 模块不可用
     */
    fun subscribe(topic: String, handler: (Message) -> Unit) {
        requireAvailable()
        topicHandlers[topic] = handler
        transport.subscribeTopic(topic) { raw -> onTopicRaw(topic, raw) }
    }

    /** 取消主题订阅。 */
    fun unsubscribe(topic: String) {
        topicHandlers.remove(topic)
        transport.unsubscribeTopic(topic)
    }

    /**
     * 按玩家寻址：解析玩家所在服后定向发送。
     *
     * @return true=已解析并投递；false=名册无此玩家（找不到目标兜底，调用方可重试/丢弃）
     * @throws IllegalStateException 模块不可用 / 未配置 PlayerLocator
     */
    fun sendToPlayer(playerName: String, type: String, payload: Any?): Boolean {
        requireAvailable()
        val locator = playerLocator
            ?: throw IllegalStateException("未配置玩家位置解析（PlayerLocator），无法按玩家寻址")
        val serverId = locator.resolveServerId(playerName)
        if (serverId == null) {
            warn("按玩家寻址落空：玩家 $playerName 不在名册（可能已换服/离线），丢弃 type=$type")
            return false
        }
        send(serverId, type, payload)
        return true
    }

    // ---- 入站处理 ----

    /** 收件流入站：解码 → 路由到 type 处理器；若为 RPC 请求，处理器回值经回信通道返回。 */
    private fun onInboundRaw(raw: String) {
        val message = decode(raw) ?: return
        val handler = typeHandlers[message.type]
        if (handler == null) {
            warn("无处理器的消息类型：type=${message.type} source=${message.source}，丢弃")
            return
        }
        val context = MessageContext(message, this)
        try {
            handler(context)
        } catch (t: Throwable) {
            warn("消息处理器抛异常：type=${message.type}，已隔离，错误=${t.message}")
        }
    }

    /** 回信入站：按 correlationId 唤醒等待的 Future。 */
    private fun onReplyRaw(raw: String) {
        val message = decode(raw) ?: return
        val correlationId = message.correlationId
        if (correlationId == null) {
            warn("回信缺 correlationId，丢弃 source=${message.source}")
            return
        }
        val future = pending.remove(correlationId)
        if (future == null) {
            // 超时后才到的迟到回信，或重复回信：正常丢弃。
            return
        }
        future.complete(message.payload)
    }

    /** 主题入站：解码 → 回调该 topic 处理器。 */
    private fun onTopicRaw(topic: String, raw: String) {
        val message = decode(raw) ?: return
        val handler = topicHandlers[topic] ?: return
        try {
            handler(message)
        } catch (t: Throwable) {
            warn("主题处理器抛异常：topic=$topic，已隔离，错误=${t.message}")
        }
    }

    /** 由 [MessageContext.reply] 调用：把响应经发起方回信通道发回。 */
    internal fun reply(request: Message, payload: Any?) {
        val replyTo = request.replyTo ?: return
        val response = Message(
            type = request.type,
            payload = payload,
            correlationId = request.correlationId,
            source = selfServerId,
        )
        transport.sendReply(replyTo, encode(response))
    }

    private fun requireAvailable() {
        if (!isAvailable()) {
            throw IllegalStateException("跨服消息模块不可用（未启用或 Redis 未连上）")
        }
    }

    private fun failAllPending(error: Throwable) {
        val ids = pending.keys.toList()
        for (id in ids) {
            pending.remove(id)?.completeExceptionally(error)
        }
    }

    private fun encode(message: Message): String = codec.encode(message.toMap())

    /** 解码原始 json 为信封；非法消息（缺 type / 解析失败）告警并返回 null。 */
    private fun decode(raw: String): Message? {
        val message = try {
            Message.fromMap(codec.decode(raw))
        } catch (t: Throwable) {
            warn("消息解码失败，丢弃，错误=${t.message}")
            return null
        }
        if (message == null) {
            warn("非法消息（缺 type 或非对象），丢弃")
        }
        return message
    }

    companion object {

        /** 回信通道前缀：本服回信通道 = reply:<serverId>。 */
        private const val REPLY_PREFIX: String = "reply:"

        /** 默认超时调度用的 daemon 定时器（单例，全 bus 共享）。 */
        private val TIMEOUT_TIMER = java.util.Timer("beacon-rpc-timeout", true)

        /**
         * 默认超时调度器：daemon 单线程定时器。壳层应注入平台 runAsyncDelayed 以复用平台调度，
         * 测试可注入受控调度器以确定性触发超时。
         */
        val DEFAULT_SCHEDULER: (Long, () -> Unit) -> Unit = { delayMs, task ->
            TIMEOUT_TIMER.schedule(
                object : java.util.TimerTask() {
                    override fun run() = task()
                },
                delayMs,
            )
        }
    }
}

/**
 * 入站消息上下文：把信封交给 handler，并在 RPC 请求时提供 [reply] 回信能力。
 *
 * @property message 收到的信封
 */
class MessageContext internal constructor(
    val message: Message,
    private val bus: MessageBus,
) {

    /** 本消息是否为 RPC 请求（带回信通道）。 */
    fun isRequest(): Boolean = message.isRequest()

    /** 业务负载（泛型树）。 */
    fun payload(): Any? = message.payload

    /**
     * 回信（仅对 RPC 请求有效；非请求调用无副作用）。
     *
     * @param payload 响应负载（泛型树）
     */
    fun reply(payload: Any?) {
        if (!message.isRequest()) return
        bus.reply(message, payload)
    }
}

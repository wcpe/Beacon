package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.ConcurrentHashMap

/**
 * 测试用 JsonCodec：把泛型树编码为令牌字符串，再据令牌还原原树。
 *
 * core 测试不得依赖 adapters（kotlinx），故不引真 JSON；本实现保证经「字符串这条线」往返的
 * 树与原树一致（深拷贝隔离），足以脚本化 MessageBus 的编解码 + 路由 + RPC 行为。
 */
class FakeJsonCodec : JsonCodec {

    override fun encode(value: Any?): String {
        val token = "tok-${SEQ.getAndIncrement()}"
        // 深拷贝，模拟真序列化切断引用共享。
        STORE[token] = deepCopy(value)
        return token
    }

    override fun decode(json: String): Any? = deepCopy(STORE[json])

    @Suppress("UNCHECKED_CAST")
    private fun deepCopy(value: Any?): Any? = when (value) {
        is Map<*, *> -> {
            val copy = LinkedHashMap<String, Any?>()
            for ((k, v) in value) copy[k.toString()] = deepCopy(v)
            copy
        }

        is List<*> -> value.map { deepCopy(it) }
        else -> value
    }

    private companion object {
        // 跨 bus 实例共享的「线缆」存储：encode 端与 decode 端可能是不同 codec 实例。
        val STORE = ConcurrentHashMap<String, Any?>()
        val SEQ = java.util.concurrent.atomic.AtomicLong(0)
    }
}

/**
 * 测试用 MessageTransport：用进程内 map 把多个 bus 连成「集群」，模拟 Redis 的
 * 收件流 / 回信通道 / 主题三类信道。无网络、确定性、同步投递。
 *
 * @param network 共享网络（多个 transport 共享同一实例即互通）
 * @param selfServerId 本服 serverId（决定订阅哪个收件流）
 */
class FakeMessageTransport(
    private val network: FakeNetwork,
    private val selfServerId: String,
) : MessageTransport {

    @Volatile
    private var connected = false

    override fun start() {
        connected = true
    }

    override fun close() {
        connected = false
        network.dropInbox(selfServerId)
    }

    override fun isConnected(): Boolean = connected

    override fun sendToServer(serverId: String, rawJson: String) {
        network.deliverInbox(serverId, rawJson)
    }

    override fun publishTopic(topic: String, rawJson: String) {
        network.deliverTopic(topic, rawJson)
    }

    override fun sendReply(replyChannel: String, rawJson: String) {
        network.deliverReply(replyChannel, rawJson)
    }

    override fun subscribeServerInbox(onMessage: (String) -> Unit) {
        network.registerInbox(selfServerId, onMessage)
    }

    override fun subscribeReplyInbox(replyChannel: String, onMessage: (String) -> Unit) {
        network.registerReply(replyChannel, onMessage)
    }

    override fun subscribeTopic(topic: String, onMessage: (String) -> Unit) {
        network.registerTopic(topic, onMessage)
    }

    override fun unsubscribeTopic(topic: String) {
        network.unregisterTopic(topic)
    }
}

/** 测试用共享网络：收件流（消费组语义简化为单消费者）、回信通道、主题三张表。 */
class FakeNetwork {

    private val inboxes = ConcurrentHashMap<String, (String) -> Unit>()
    private val replies = ConcurrentHashMap<String, (String) -> Unit>()
    private val topics = ConcurrentHashMap<String, (String) -> Unit>()

    /** 离线目标的留存消息（模拟 Streams：目标上线注册后补投）。 */
    private val pendingInbox = ConcurrentHashMap<String, MutableList<String>>()

    fun registerInbox(serverId: String, onMessage: (String) -> Unit) {
        inboxes[serverId] = onMessage
        // 补投离线期间留存的消息（模拟消费组补消费）。
        pendingInbox.remove(serverId)?.forEach { onMessage(it) }
    }

    fun dropInbox(serverId: String) {
        inboxes.remove(serverId)
    }

    fun registerReply(channel: String, onMessage: (String) -> Unit) {
        replies[channel] = onMessage
    }

    fun registerTopic(topic: String, onMessage: (String) -> Unit) {
        topics[topic] = onMessage
    }

    fun unregisterTopic(topic: String) {
        topics.remove(topic)
    }

    fun deliverInbox(serverId: String, raw: String) {
        val handler = inboxes[serverId]
        if (handler != null) {
            handler(raw)
        } else {
            // 目标离线：留存待上线补投（Streams 可靠送达语义）。
            pendingInbox.getOrPut(serverId) { mutableListOf() }.add(raw)
        }
    }

    fun deliverReply(channel: String, raw: String) {
        replies[channel]?.invoke(raw)
    }

    fun deliverTopic(topic: String, raw: String) {
        // pub/sub：无订阅者即丢弃（可丢语义）。
        topics[topic]?.invoke(raw)
    }
}

/** 测试用玩家位置解析：固定 map。 */
class FakePlayerLocator(private val table: Map<String, String>) : PlayerLocator {
    override fun resolveServerId(playerName: String): String? = table[playerName]
}

/** 测试用受控超时调度器：手动触发到期任务，确定性验证 RPC 超时。 */
class ManualScheduler {
    private val tasks = mutableListOf<Pair<Long, () -> Unit>>()

    val schedule: (Long, () -> Unit) -> Unit = { delayMs, task ->
        tasks.add(delayMs to task)
    }

    /** 触发所有已登记的到期任务（模拟时间走到超时点）。 */
    fun fireAll() {
        val snapshot = tasks.toList()
        tasks.clear()
        snapshot.forEach { it.second() }
    }

    fun pendingCount(): Int = tasks.size
}

package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.id.Uuid7
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * MessageBus 广播寻址（FR-180 / ADR-0065）单测：publish 组广播信封报文键、zone 重载、
 * 入站广播按 topic 分发且与定向 on(type) 分发表隔离、可丢语义（无订阅者仍 delivered）、handler 隔离。
 *
 * 用捕获式假 transport，无网络；广播上行经 publishTopic 捕获、入站经 deliverInbound 直驱（HTTP 中转模型）。
 */
class MessageBusBroadcastTest {
    private val settings =
        MessagingSettings(enabled = true, rpcTimeoutMs = 1000, streamMaxLen = 0, consumerName = "t")

    /** 捕获 publishTopic 出站 raw 的假 transport；入站不经此。 */
    private class CapturingBroadcastTransport : MessageTransport {
        val lastTopic = AtomicReference<String>()
        val lastRaw = AtomicReference<String>()

        @Volatile
        private var connected = false

        override fun start() {
            connected = true
        }

        override fun close() {
            connected = false
        }

        override fun isConnected(): Boolean = connected

        override fun publishTopic(
            topic: String,
            rawJson: String,
        ) {
            lastTopic.set(topic)
            lastRaw.set(rawJson)
        }

        override fun sendToServer(
            serverId: String,
            rawJson: String,
        ) = Unit

        override fun sendReply(
            replyChannel: String,
            rawJson: String,
        ) = Unit

        override fun subscribeServerInbox(onMessage: (String) -> Unit) = Unit

        override fun subscribeReplyInbox(
            replyChannel: String,
            onMessage: (String) -> Unit,
        ) = Unit

        override fun subscribeTopic(
            topic: String,
            onMessage: (String) -> Unit,
        ) = Unit

        override fun unsubscribeTopic(topic: String) = Unit
    }

    /** 尺寸真实的 codec：String 编码为自身（供 payload 上限校验测真字节数）；其它编码为空串。 */
    private class SizingCodec : JsonCodec {
        override fun encode(value: Any?): String = value as? String ?: ""

        override fun decode(json: String): Any? = json
    }

    private fun bus(
        transport: MessageTransport,
        codec: JsonCodec = FakeJsonCodec(),
    ): MessageBus =
        MessageBus(
            transport = transport,
            codec = codec,
            selfServerId = "A",
            settings = settings,
        ).also { it.start() }

    @Test
    fun `publish 组广播信封 携带 broadcast 标记与 targetKind 且 topic 落 type`() {
        val transport = CapturingBroadcastTransport()
        val codec = FakeJsonCodec()
        val bus = bus(transport, codec)

        bus.publish("chat.global", mapOf("text" to "hi"))

        assertEquals("chat.global", transport.lastTopic.get())
        val sent = Message.fromMap(codec.decode(transport.lastRaw.get()))!!
        assertEquals("chat.global", sent.type, "topic 落信封 type（对齐 msgType）")
        assertEquals(Message.TARGET_BROADCAST, sent.targetKind)
        assertTrue(sent.broadcast, "广播信封 broadcast 标记须为真")
        assertEquals("A", sent.source, "source 为发送者本服（含自身语义）")
        assertNull(sent.targetId, "无 zone 定向时 targetId 省略")
        assertTrue(sent.messageId != null, "广播须带 messageId 供控制面定位日表")
        assertTrue(sent.sentAt != null, "广播须带 sentAt")
    }

    @Test
    fun `publish zone 重载 信封 targetId 落 zone 名`() {
        val transport = CapturingBroadcastTransport()
        val codec = FakeJsonCodec()
        val bus = bus(transport, codec)

        bus.publish("cache.invalidate", "key-1", zone = "zone-pvp")

        val sent = Message.fromMap(codec.decode(transport.lastRaw.get()))!!
        assertEquals(Message.TARGET_BROADCAST, sent.targetKind)
        assertTrue(sent.broadcast)
        assertEquals("zone-pvp", sent.targetId, "zone 级定向落信封 targetId")
    }

    @Test
    fun `入站广播路由 topic 分发表 与定向 on(type) 隔离`() {
        val bus = bus(CapturingBroadcastTransport())
        var onTypeHit: Any? = null
        var topicHit: Any? = null
        bus.on("evt") { ctx -> onTypeHit = ctx.payload() }
        bus.subscribe("evt") { message -> topicHit = message.payload }

        // 广播入站（broadcast=true）：只应命中 topic 订阅表，不碰 on(type)。
        val bcastOutcome =
            bus.deliverInbound(
                Message(type = "evt", payload = "bcast", messageId = Uuid7.generate(), source = "B", broadcast = true),
            )
        assertEquals("bcast", topicHit, "广播应路由到 subscribe 处理器")
        assertNull(onTypeHit, "广播绝不应触发定向 on(type) 处理器")
        assertEquals(InboundOutcome.STATUS_DELIVERED, bcastOutcome.status)

        // 定向单向 send 入站（broadcast 缺省 false）：只应命中 on(type)，不碰 topic 表。
        topicHit = null
        val directOutcome =
            bus.deliverInbound(Message(type = "evt", payload = "direct", messageId = Uuid7.generate(), source = "B"))
        assertEquals("direct", onTypeHit, "定向消息应路由到 on(type) 处理器")
        assertNull(topicHit, "定向消息绝不应触发 topic 订阅处理器")
        assertEquals(InboundOutcome.STATUS_DELIVERED, directOutcome.status)
    }

    @Test
    fun `入站广播无订阅者仍回 delivered 可丢语义`() {
        val bus = bus(CapturingBroadcastTransport())
        // 未 subscribe 任何 topic：广播 fan-out 到全在线服，本服不订阅不构成投递失败。
        val outcome =
            bus.deliverInbound(
                Message(type = "no.subscriber", payload = null, messageId = Uuid7.generate(), broadcast = true),
            )
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
        assertNull(outcome.reason)
    }

    @Test
    fun `入站广播订阅 handler 抛异常回 failed 且隔离`() {
        val bus = bus(CapturingBroadcastTransport())
        bus.subscribe("boom") { error("订阅处理器炸了") }
        val outcome =
            bus.deliverInbound(Message(type = "boom", payload = null, messageId = Uuid7.generate(), broadcast = true))
        assertEquals(InboundOutcome.STATUS_FAILED, outcome.status)
        assertTrue(outcome.reason!!.contains("炸"))
    }

    @Test
    fun `入站广播命中订阅 handler 回 delivered 带非负耗时`() {
        val bus = bus(CapturingBroadcastTransport())
        bus.subscribe("evt") { }
        val outcome =
            bus.deliverInbound(Message(type = "evt", payload = null, messageId = Uuid7.generate(), broadcast = true))
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
        assertTrue((outcome.handlerCostMs ?: -1) >= 0, "命中订阅的广播应带非负 handler 耗时")
    }

    @Test
    fun `unsubscribe 后广播不再命中处理器`() {
        val bus = bus(CapturingBroadcastTransport())
        var hit = false
        bus.subscribe("evt") { hit = true }
        bus.unsubscribe("evt")
        val outcome =
            bus.deliverInbound(Message(type = "evt", payload = null, messageId = Uuid7.generate(), broadcast = true))
        assertFalse(hit, "已注销的 topic 不应再命中")
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status, "无订阅者广播仍 delivered")
    }

    @Test
    fun `publish payload 超过上限本地前置拒绝`() {
        val bus = bus(CapturingBroadcastTransport(), codec = SizingCodec())
        val tooBig = "x".repeat(MessageBus.MAX_PAYLOAD_BYTES + 1)
        val ex = assertFailsWith<IllegalArgumentException> { bus.publish("t", tooBig) }
        assertTrue(ex.message!!.contains("上限"))
    }
}

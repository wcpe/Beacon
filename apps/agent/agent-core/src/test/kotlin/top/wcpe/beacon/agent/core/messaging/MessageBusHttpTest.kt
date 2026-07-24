package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.id.Uuid7
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * MessageBus 的 HTTP 中转模型行为单测（ADR-0063）：三路入站分发（响应/请求/单向）、payload 上限前置校验、
 * 按玩家寻址（无本地名册时发 targetKind=player）、correlationId 自引用标记。用捕获式假 transport，无网络。
 */
class MessageBusHttpTest {
    private val settings =
        MessagingSettings(enabled = true, rpcTimeoutMs = 1000, streamMaxLen = 0, consumerName = "t")

    /** 捕获出站 raw 的假 transport；入站不经此（HTTP 模型由协调器直调 deliverInbound）。 */
    private class CapturingTransport : MessageTransport {
        val lastServerId = AtomicReference<String>()
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

        override fun sendToServer(
            serverId: String,
            rawJson: String,
        ) {
            lastServerId.set(serverId)
            lastRaw.set(rawJson)
        }

        override fun publishTopic(
            topic: String,
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
    fun `单向 send 入站路由到 handler 返回 delivered`() {
        val bus = bus(CapturingTransport())
        var got: Any? = null
        bus.on("evt") { ctx -> got = ctx.payload() }

        val outcome = bus.deliverInbound(Message(type = "evt", payload = "hi", messageId = Uuid7.generate(), source = "B"))

        assertEquals("hi", got)
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
        assertTrue((outcome.handlerCostMs ?: -1) >= 0, "delivered 应带非负 handler 耗时")
    }

    @Test
    fun `无处理器入站回 failed`() {
        val bus = bus(CapturingTransport())
        val outcome = bus.deliverInbound(Message(type = "unknown", payload = null, messageId = Uuid7.generate()))
        assertEquals(InboundOutcome.STATUS_FAILED, outcome.status)
        assertEquals("no_handler_for_type", outcome.reason)
    }

    @Test
    fun `handler 抛异常入站回 failed 且隔离`() {
        val bus = bus(CapturingTransport())
        bus.on("boom") { error("业务处理器炸了") }
        val outcome = bus.deliverInbound(Message(type = "boom", payload = null, messageId = Uuid7.generate()))
        assertEquals(InboundOutcome.STATUS_FAILED, outcome.status)
        assertTrue(outcome.reason!!.contains("炸"))
    }

    @Test
    fun `RPC 请求 correlationId 自引用 messageId 路由 handler 且 isRequest 为真`() {
        val bus = bus(CapturingTransport())
        var seenRequest: Boolean? = null
        bus.on("q") { ctx -> seenRequest = ctx.isRequest() }

        val id = Uuid7.generate()
        // 请求：correlationId == 自身 messageId（自引用标记）。
        val outcome = bus.deliverInbound(Message(type = "q", payload = null, correlationId = id, messageId = id, source = "B"))

        assertEquals(true, seenRequest, "correlationId 自引用的请求 isRequest 应为真")
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
    }

    @Test
    fun `RPC 响应按 correlationId 唤醒挂起的 call future 不走 type 路由`() {
        val transport = CapturingTransport()
        val codec = FakeJsonCodec()
        val bus = MessageBus(transport, codec, "A", settings).also { it.start() }
        // 注册同 type 处理器：若响应被误当请求路由，会触发它（用以反证不该发生）。
        var handlerHit = false
        bus.on("sum") { handlerHit = true }

        val future = bus.call("B", "sum", null)
        // 从捕获的出站请求取 messageId（响应的 correlationId 应回填它）。
        val requestId = Message.fromMap(codec.decode(transport.lastRaw.get()))!!.messageId!!

        // 构造响应：自身 messageId 为新 id、correlationId 指回请求 messageId。
        val response =
            Message(type = "sum", payload = mapOf("r" to 5L), correlationId = requestId, messageId = Uuid7.generate(), source = "B")
        val outcome = bus.deliverInbound(response)

        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
        assertTrue(future.isDone)
        assertEquals(mapOf("r" to 5L), future.get())
        assertTrue(!handlerHit, "响应绝不应被当作请求路由到 type 处理器")
    }

    @Test
    fun `无主的迟到响应静默受理不抛不路由`() {
        val bus = bus(CapturingTransport())
        var handlerHit = false
        bus.on("t") { handlerHit = true }
        // 无对应 pending 的响应（correlationId != messageId）。
        val outcome =
            bus.deliverInbound(Message(type = "t", payload = null, correlationId = "no-such-id", messageId = Uuid7.generate()))
        assertEquals(InboundOutcome.STATUS_DELIVERED, outcome.status)
        assertTrue(!handlerHit)
    }

    @Test
    fun `按玩家寻址无本地名册时发 targetKind=player`() {
        val transport = CapturingTransport()
        val codec = FakeJsonCodec()
        val bus =
            MessageBus(transport = transport, codec = codec, selfServerId = "A", settings = settings).also { it.start() }

        val ok = bus.sendToPlayer("Steve", "dm", "hi")

        assertTrue(ok, "HTTP 中转下按玩家寻址恒 true（在线与否由控制面判定）")
        assertEquals("Steve", transport.lastServerId.get())
        val sent = Message.fromMap(codec.decode(transport.lastRaw.get()))!!
        assertEquals(Message.TARGET_PLAYER, sent.targetKind)
        assertEquals("Steve", sent.targetId)
        assertNull(sent.correlationId)
    }

    @Test
    fun `payload 超过 64KB 上限本地拒绝发送`() {
        val bus = bus(CapturingTransport(), codec = SizingCodec())
        val tooBig = "x".repeat(MessageBus.MAX_PAYLOAD_BYTES + 1)
        val ex = assertFailsWith<IllegalArgumentException> { bus.send("B", "t", tooBig) }
        assertTrue(ex.message!!.contains("上限"))
        // call / sendToPlayer 同样前置拒绝。
        assertFailsWith<IllegalArgumentException> { bus.call("B", "t", tooBig) }
        assertFailsWith<IllegalArgumentException> { bus.sendToPlayer("Steve", "t", tooBig) }
    }

    @Test
    fun `payload 恰在 64KB 上限内放行`() {
        val transport = CapturingTransport()
        val bus = bus(transport, codec = SizingCodec())
        val atLimit = "x".repeat(MessageBus.MAX_PAYLOAD_BYTES)
        bus.send("B", "t", atLimit) // 不抛即通过
        assertEquals("B", transport.lastServerId.get())
    }
}

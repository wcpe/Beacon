package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.api.IncomingMessage
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** MessagingView（Java 门面适配）与 DisabledMessaging、MessagingHolder、MessagingModule 单测。 */
class MessagingViewTest {

    private val settings = MessagingSettings(enabled = true, rpcTimeoutMs = 1000, streamMaxLen = 1000, consumerName = "t")

    @Test
    fun `DisabledMessaging isAvailable 为 false 且发送抛异常`() {
        assertFalse(DisabledMessaging.isAvailable())
        assertFailsWith<IllegalStateException> { DisabledMessaging.send("B", "t", null) }
        assertFailsWith<IllegalStateException> { DisabledMessaging.publish("t", null) }
        assertFalse(DisabledMessaging.sendToPlayer("p", "t", null))
        // call 返回异常完成的 Future，不直接抛。
        val future = DisabledMessaging.call("B", "t", null)
        assertTrue(future.isCompletedExceptionally)
        // subscribe/on 返回空句柄，remove 安全。
        DisabledMessaging.subscribe("t") { _, _ -> }.remove()
        DisabledMessaging.on("t") { }.remove()
    }

    @Test
    fun `MessagingHolder 默认 Disabled 可切换可复位`() {
        val holder = MessagingHolder()
        assertFalse(holder.get().isAvailable())

        val net = FakeNetwork()
        val bus = newBus(net, "A").also { it.start() }
        holder.set(MessagingView(bus))
        assertTrue(holder.get().isAvailable())

        holder.reset()
        assertFalse(holder.get().isAvailable())
    }

    @Test
    fun `MessagingView 定向发送与按类型收消息`() {
        val net = FakeNetwork()
        val a = MessagingView(newBus(net, "A").also { it.start() })
        val b = newBus(net, "B").also { it.start() }
        val bView = MessagingView(b)

        var received: Any? = null
        bView.on("greet") { msg: IncomingMessage -> received = msg.payload() }

        a.send("B", "greet", "hi")
        assertEquals("hi", received)
    }

    @Test
    fun `MessagingView RPC 经 IncomingMessage reply 闭环`() {
        val net = FakeNetwork()
        val a = MessagingView(newBus(net, "A").also { it.start() })
        val b = MessagingView(newBus(net, "B").also { it.start() })

        b.on("echo") { msg -> msg.reply(msg.payload()) }

        val resp = a.call("B", "echo", "ping").get()
        assertEquals("ping", resp)
    }

    @Test
    fun `MessagingModule 启用时启动成功并注入活跃门面`() {
        val net = FakeNetwork()
        val holder = MessagingHolder()
        val module = MessagingModule(
            transport = FakeMessageTransport(net, "A"),
            codec = FakeJsonCodec(),
            selfServerId = "A",
            settings = settings,
            holder = holder,
        )
        assertTrue(module.start())
        assertTrue(holder.get().isAvailable())

        module.stop()
        assertFalse(holder.get().isAvailable())
    }

    @Test
    fun `MessagingModule 未启用时不启动 保持降级`() {
        val net = FakeNetwork()
        val holder = MessagingHolder()
        val module = MessagingModule(
            transport = FakeMessageTransport(net, "A"),
            codec = FakeJsonCodec(),
            selfServerId = "A",
            settings = settings.copy(enabled = false),
            holder = holder,
        )
        assertFalse(module.start())
        assertFalse(holder.get().isAvailable())
    }

    @Test
    fun `MessagingModule 启动失败时降级不抛`() {
        val holder = MessagingHolder()
        val module = MessagingModule(
            transport = ThrowingTransport(),
            codec = FakeJsonCodec(),
            selfServerId = "A",
            settings = settings,
            holder = holder,
        )
        // start 内部捕获异常并降级，返回 false，不抛。
        assertFalse(module.start())
        assertFalse(holder.get().isAvailable())
    }

    private fun newBus(net: FakeNetwork, serverId: String): MessageBus {
        return MessageBus(
            transport = FakeMessageTransport(net, serverId),
            codec = FakeJsonCodec(),
            selfServerId = serverId,
            settings = settings,
        )
    }

    /** 启动即抛的传输，验证模块降级容错。 */
    private class ThrowingTransport : MessageTransport {
        override fun start() = throw RuntimeException("连接失败")
        override fun close() {}
        override fun isConnected(): Boolean = false
        override fun sendToServer(serverId: String, rawJson: String) {}
        override fun publishTopic(topic: String, rawJson: String) {}
        override fun sendReply(replyChannel: String, rawJson: String) {}
        override fun subscribeServerInbox(onMessage: (String) -> Unit) {}
        override fun subscribeReplyInbox(replyChannel: String, onMessage: (String) -> Unit) {}
        override fun subscribeTopic(topic: String, onMessage: (String) -> Unit) {}
        override fun unsubscribeTopic(topic: String) {}
    }
}

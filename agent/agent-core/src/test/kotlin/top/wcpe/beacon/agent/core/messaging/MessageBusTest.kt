package top.wcpe.beacon.agent.core.messaging

import java.util.concurrent.ExecutionException
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** MessageBus 四种模式 + RPC 超时 + 可靠送达 + 降级的脚本化单测（假 transport，无 Redis）。 */
class MessageBusTest {

    private val settings = MessagingSettings(
        enabled = true,
        rpcTimeoutMs = 1000,
        streamMaxLen = 10000,
        consumerName = "test",
    )

    private fun bus(
        network: FakeNetwork,
        serverId: String,
        locator: PlayerLocator? = null,
        scheduler: ManualScheduler = ManualScheduler(),
    ): MessageBus {
        val transport = FakeMessageTransport(network, serverId)
        val codec = FakeJsonCodec()
        return MessageBus(
            transport = transport,
            codec = codec,
            selfServerId = serverId,
            settings = settings,
            playerLocator = locator,
            scheduleTimeout = scheduler.schedule,
        )
    }

    @Test
    fun `定向发送 目标按 type 收到 payload`() {
        val net = FakeNetwork()
        val a = bus(net, "A")
        val b = bus(net, "B")
        a.start()
        b.start()

        var received: Any? = null
        var seenSource: String? = null
        b.on("greet") { ctx ->
            received = ctx.payload()
            seenSource = ctx.message.source
        }

        a.send("B", "greet", mapOf("msg" to "hi"))

        assertEquals(mapOf("msg" to "hi"), received)
        assertEquals("A", seenSource)
    }

    @Test
    fun `RPC call 在超时内拿到目标返回值`() {
        val net = FakeNetwork()
        val a = bus(net, "A")
        val b = bus(net, "B")
        a.start()
        b.start()

        b.on("sum") { ctx ->
            val args = ctx.payload() as Map<*, *>
            val x = (args["x"] as Number).toLong()
            val y = (args["y"] as Number).toLong()
            ctx.reply(mapOf("result" to x + y))
        }

        val future = a.call("B", "sum", mapOf("x" to 2L, "y" to 3L))
        val resp = future.get(1, TimeUnit.SECONDS) as Map<*, *>

        assertEquals(5L, (resp["result"] as Number).toLong())
    }

    @Test
    fun `RPC 目标不在线则超时`() {
        val net = FakeNetwork()
        val scheduler = ManualScheduler()
        val a = bus(net, "A", scheduler = scheduler)
        a.start()
        // 目标 B 未启动（不在线）。

        val future = a.call("B", "q", null)
        assertFalse(future.isDone)
        assertEquals(1, scheduler.pendingCount())

        // 模拟时间走到超时点。
        scheduler.fireAll()

        assertTrue(future.isDone)
        val ex = assertFailsWith<ExecutionException> { future.get() }
        assertTrue(ex.cause is TimeoutException)
    }

    @Test
    fun `迟到回信不影响已超时的请求 不抛异常`() {
        val net = FakeNetwork()
        val scheduler = ManualScheduler()
        val a = bus(net, "A", scheduler = scheduler)
        val b = bus(net, "B")
        a.start()
        b.start()

        // B 收到后先不回，等 A 超时后再回（手动控制顺序）。
        var captured: MessageContext? = null
        b.on("q") { ctx -> captured = ctx }

        val future = a.call("B", "q", null)
        scheduler.fireAll() // A 超时
        assertTrue(future.isCompletedExceptionally)

        // 迟到回信：correlationId 已无对应 Future，应静默丢弃不抛。
        captured!!.reply(mapOf("late" to true))
        // 无异常即通过。
    }

    @Test
    fun `主题订阅者只收所订主题 非订阅者收不到`() {
        val net = FakeNetwork()
        val pub = bus(net, "P")
        val sub = bus(net, "S")
        pub.start()
        sub.start()

        var got: Any? = null
        sub.subscribe("news") { msg -> got = msg.payload }

        // 未被订阅的主题：丢弃（pub/sub 可丢）。
        pub.publish("sports", mapOf("x" to 1L))
        assertNull(got)

        pub.publish("news", mapOf("headline" to "hello"))
        assertEquals(mapOf("headline" to "hello"), got)
    }

    @Test
    fun `取消订阅后不再收到`() {
        val net = FakeNetwork()
        val pub = bus(net, "P")
        val sub = bus(net, "S")
        pub.start()
        sub.start()

        var count = 0
        sub.subscribe("t") { count++ }
        pub.publish("t", null)
        assertEquals(1, count)

        sub.unsubscribe("t")
        pub.publish("t", null)
        assertEquals(1, count)
    }

    @Test
    fun `按玩家寻址 解析所在服后投递`() {
        val net = FakeNetwork()
        val locator = FakePlayerLocator(mapOf("Steve" to "B"))
        val a = bus(net, "A", locator = locator)
        val b = bus(net, "B")
        a.start()
        b.start()

        var received: Any? = null
        b.on("dm") { ctx -> received = ctx.payload() }

        val delivered = a.sendToPlayer("Steve", "dm", "hello")

        assertTrue(delivered)
        assertEquals("hello", received)
    }

    @Test
    fun `按玩家寻址 名册落空走找不到目标兜底`() {
        val net = FakeNetwork()
        val locator = FakePlayerLocator(emptyMap())
        val a = bus(net, "A", locator = locator)
        a.start()

        val warnings = mutableListOf<String>()
        val busWithWarn = MessageBus(
            transport = FakeMessageTransport(net, "A2").also { it.start() },
            codec = FakeJsonCodec(),
            selfServerId = "A2",
            settings = settings,
            playerLocator = locator,
            warn = warnings::add,
        ).also { it.start() }

        val delivered = busWithWarn.sendToPlayer("Ghost", "dm", "x")

        assertFalse(delivered)
        assertTrue(warnings.single().contains("Ghost"))
    }

    @Test
    fun `离线目标上线后补收到可靠消息`() {
        val net = FakeNetwork()
        val a = bus(net, "A")
        a.start()

        // 目标 B 尚未上线，先发可靠消息（留存）。
        a.send("B", "evt", mapOf("n" to 1L))

        // B 上线注册收件流后应补收到。
        var received: Any? = null
        val b = bus(net, "B")
        b.on("evt") { ctx -> received = ctx.payload() }
        b.start()

        assertEquals(mapOf("n" to 1L), received)
    }

    @Test
    fun `未启动时 isAvailable 为 false 且发送报错`() {
        val net = FakeNetwork()
        val a = bus(net, "A")
        // 未 start。
        assertFalse(a.isAvailable())
        assertFailsWith<IllegalStateException> { a.send("B", "t", null) }
        assertFailsWith<IllegalStateException> { a.call("B", "t", null) }
        assertFailsWith<IllegalStateException> { a.publish("t", null) }
    }

    @Test
    fun `close 后挂起的 RPC Future 异常完成`() {
        val net = FakeNetwork()
        val scheduler = ManualScheduler()
        val a = bus(net, "A", scheduler = scheduler)
        a.start()

        val future = a.call("B", "q", null)
        a.close()

        assertTrue(future.isCompletedExceptionally)
        assertFalse(a.isAvailable())
    }

    @Test
    fun `无处理器的消息类型被丢弃并告警`() {
        val net = FakeNetwork()
        val warnings = mutableListOf<String>()
        val a = MessageBus(
            transport = FakeMessageTransport(net, "A").also {},
            codec = FakeJsonCodec(),
            selfServerId = "A",
            settings = settings,
            warn = warnings::add,
        )
        val b = bus(net, "B")
        a.start()
        b.start()

        b.send("A", "unknown", "x")

        assertTrue(warnings.any { it.contains("unknown") })
    }
}

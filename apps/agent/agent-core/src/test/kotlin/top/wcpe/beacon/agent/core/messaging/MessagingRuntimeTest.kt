package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** HTTP 消息运行时回归测试：控制面健康门控与启动失败回滚。 */
class MessagingRuntimeTest {
    private val delegateCodec = FakeJsonCodec()
    private val codec: JsonCodec =
        object : JsonCodec {
            override fun encode(value: Any?): String = delegateCodec.encode(value)

            override fun decode(json: String): Any? =
                when (json) {
                    "非法轮询响应" -> throw IllegalArgumentException("模拟 poll 响应解析失败")
                    "非法回执响应" -> throw IllegalArgumentException("模拟 ack 响应解析失败")
                    else -> delegateCodec.decode(json)
                }
        }

    /** 可观察启停的消息传输，用于断言运行时失败回滚关闭总线。 */
    private class TrackingMessageTransport : MessageTransport {
        val closeCalls = AtomicInteger(0)

        @Volatile
        private var connected = false

        override fun start() {
            connected = true
        }

        override fun close() {
            closeCalls.incrementAndGet()
            connected = false
        }

        override fun isConnected(): Boolean = connected

        override fun sendToServer(
            serverId: String,
            rawJson: String,
        ) = Unit

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

    /** 首次立即调度可注入异常，关闭异常后同步执行任务。 */
    private class ToggleScheduleAdapter(
        private val delegate: ManualAsyncAdapter = ManualAsyncAdapter(),
    ) : PlatformAdapter by delegate {
        @Volatile
        var failImmediately = true

        override fun runAsync(task: () -> Unit) {
            check(!failImmediately) { "模拟轮询调度失败" }
            task()
        }
    }

    /** 所有异步任务只入队，测试逐个推进以观察连接状态转移。 */
    private class QueuedAdapter(
        private val delegate: ManualAsyncAdapter = ManualAsyncAdapter(),
    ) : PlatformAdapter by delegate {
        private val tasks = ArrayDeque<() -> Unit>()

        override fun runAsync(task: () -> Unit) {
            tasks.addLast(task)
        }

        override fun runAsyncDelayed(
            delayMs: Long,
            task: () -> Unit,
        ) {
            tasks.addLast(task)
        }

        fun runNext() {
            tasks.removeFirst().invoke()
        }
    }

    /** 按顺序返回 poll 与 ack 结果；异常项模拟控制面失联。 */
    private class ScriptedControlTransport(
        pollOutcomes: List<Any>,
        ackOutcomes: List<Any> = emptyList(),
    ) : HttpTransport {
        private val pollOutcomes = ArrayDeque(pollOutcomes)
        private val ackOutcomes = ArrayDeque(ackOutcomes)
        val pollCalls = AtomicInteger(0)
        val ackCalls = AtomicInteger(0)
        val sendCalls = AtomicInteger(0)

        override fun execute(request: HttpRequest): HttpResponse =
            when {
                request.url.endsWith("/messages/poll") -> {
                    pollCalls.incrementAndGet()
                    next(pollOutcomes)
                }

                request.url.endsWith("/messages/ack") -> {
                    ackCalls.incrementAndGet()
                    next(ackOutcomes)
                }

                request.url.endsWith("/messages/send") -> {
                    sendCalls.incrementAndGet()
                    HttpResponse(200, "sent-ok")
                }

                else -> HttpResponse(404, "")
            }

        private fun next(outcomes: ArrayDeque<Any>): HttpResponse =
            when (val outcome = outcomes.removeFirst()) {
                is HttpResponse -> outcome
                is RuntimeException -> throw outcome
                else -> error("不支持的测试结果：$outcome")
            }
    }

    @Test
    fun `poll start 抛异常会关闭总线并保持 holder Disabled`() {
        val tracking = TrackingMessageTransport()
        val bus = bus(tracking)
        val holder = MessagingHolder()
        val adapter = ToggleScheduleAdapter()
        val control =
            object : HttpTransport {
                override fun execute(request: HttpRequest): HttpResponse = HttpResponse(204, "")
            }
        val poll = MessagePollCoordinator(apiClient(control), identity(), adapter, bus)
        val runtime = runtime(holder, bus, poll)

        runtime.start()

        assertFalse(holder.get().isAvailable())
        assertFalse(bus.isAvailable(), "启动失败后不得遗留已启动总线")
        assertEquals(1, tracking.closeCalls.get(), "启动回滚必须关闭总线资源")
    }

    @Test
    fun `poll start 失败后协调器可再次启动`() {
        val tracking = TrackingMessageTransport()
        val bus = bus(tracking)
        val holder = MessagingHolder()
        val adapter = ToggleScheduleAdapter()
        val control = ScriptedControlTransport(listOf(RuntimeException("模拟控制面失联")))
        val poll = MessagePollCoordinator(apiClient(control), identity(), adapter, bus)
        val runtime = runtime(holder, bus, poll)

        runtime.start()
        adapter.failImmediately = false
        runtime.start()

        assertEquals(1, control.pollCalls.get(), "失败回滚必须停止旧轮询代，使后续 start 能真正重启")
    }

    @Test
    fun `poll 健康决定门面可用性且失败后可恢复`() {
        val control =
            ScriptedControlTransport(
                listOf(
                    HttpResponse(204, ""),
                    RuntimeException("模拟控制面失联"),
                    HttpResponse(204, ""),
                ),
            )
        val apiClient = apiClient(control)
        val messageTransport = HttpMessageTransport(apiClient, identity(), codec)
        val bus = bus(messageTransport)
        val holder = MessagingHolder()
        val adapter = QueuedAdapter()
        val poll = MessagePollCoordinator(apiClient, identity(), adapter, bus)
        val runtime = runtime(holder, bus, poll)

        runtime.start()
        val messaging = holder.get()
        assertFalse(messaging.isAvailable(), "未完成首轮控制面探测前不得宣称消息可用")

        adapter.runNext()
        assertTrue(messaging.isAvailable(), "poll 成功后已取得的门面引用应恢复可用")

        adapter.runNext()
        assertFalse(holder.get().isAvailable(), "poll 失败后 holder 必须立即反映失联")
        assertFalse(messaging.isAvailable(), "poll 失败后既有门面引用也必须反映失联")
        assertFailsWith<IllegalStateException> { messaging.send("game-1", "evt", null) }
        assertEquals(0, control.sendCalls.get(), "失联时 send 必须快速失败，不请求控制面")

        adapter.runNext()
        assertTrue(holder.get().isAvailable(), "控制面恢复后应重新启用消息门面")
        assertTrue(messaging.isAvailable(), "控制面恢复后既有门面引用也应恢复可用")
    }

    @Test
    fun `非法 poll 响应停用当前轮询代且 runtime 可重新启动`() {
        val control =
            ScriptedControlTransport(
                listOf(
                    HttpResponse(200, "非法轮询响应"),
                    HttpResponse(204, ""),
                ),
            )
        val apiClient = apiClient(control)
        val bus = bus(HttpMessageTransport(apiClient, identity(), codec))
        val holder = MessagingHolder()
        val adapter = QueuedAdapter()
        val runtime = runtime(holder, bus, MessagePollCoordinator(apiClient, identity(), adapter, bus))

        runtime.start()
        adapter.runNext()

        assertFalse(holder.get().isAvailable(), "poll 响应解析异常后 holder 必须不可用")
        assertFalse(bus.isAvailable(), "poll 响应解析异常后总线必须关闭")

        runtime.start()
        adapter.runNext()

        assertEquals(2, control.pollCalls.get(), "异常轮询代必须停用，使后续 start 能启动新一代")
        assertTrue(holder.get().isAvailable(), "新一代 poll 成功后应恢复可用")
    }

    @Test
    fun `非法 ack 响应停用当前轮询代且 runtime 可重新启动`() {
        val control =
            ScriptedControlTransport(
                pollOutcomes = listOf(validPollResponse(), HttpResponse(204, "")),
                ackOutcomes = listOf(HttpResponse(200, "非法回执响应")),
            )
        val apiClient = apiClient(control)
        val bus = bus(HttpMessageTransport(apiClient, identity(), codec))
        val holder = MessagingHolder()
        val adapter = QueuedAdapter()
        val runtime = runtime(holder, bus, MessagePollCoordinator(apiClient, identity(), adapter, bus))

        runtime.start()
        adapter.runNext()

        assertEquals(1, control.ackCalls.get(), "首轮消息必须进入 ack 响应解析")
        assertFalse(holder.get().isAvailable(), "ack 响应解析异常后 holder 必须不可用")
        assertFalse(bus.isAvailable(), "ack 响应解析异常后总线必须关闭")

        runtime.start()
        adapter.runNext()

        assertEquals(2, control.pollCalls.get(), "异常轮询代必须停用，使后续 start 能启动新一代")
        assertTrue(holder.get().isAvailable(), "新一代 poll 成功后应恢复可用")
    }

    private fun runtime(
        holder: MessagingHolder,
        bus: MessageBus,
        poll: MessagePollCoordinator,
    ): MessagingRuntime =
        MessagingRuntime(
            settings = messagingSettings(),
            holder = holder,
            bus = bus,
            poll = poll,
        )

    private fun bus(transport: MessageTransport): MessageBus =
        MessageBus(
            transport = transport,
            codec = codec,
            selfServerId = "lobby-1",
            settings = messagingSettings(),
        )

    private fun apiClient(transport: HttpTransport): BeaconApiClient = BeaconApiClient(transport, codec, agentSettings())

    private fun validPollResponse(): HttpResponse =
        HttpResponse(
            200,
            codec.encode(
                mapOf(
                    "messages" to
                        listOf(
                            mapOf(
                                "messageId" to "m1",
                                "msgType" to "evt",
                                "sourceServerId" to "game-1",
                                "payload" to null,
                                "createdAt" to "1970-01-01T00:00:00Z",
                            ),
                        ),
                ),
            ),
        )

    private fun messagingSettings(): MessagingSettings =
        MessagingSettings(enabled = true, rpcTimeoutMs = 1000, streamMaxLen = 0, consumerName = "test")

    private fun identity(): AgentIdentity =
        AgentIdentity(
            namespace = "prod",
            serverId = "lobby-1",
            role = "bukkit",
            groupHint = "area1",
            address = "127.0.0.1:25565",
            version = "1.0",
            capacity = 100,
            weight = 1,
            metadata = emptyMap(),
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    private fun agentSettings(): AgentSettings =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            backoff = BackoffSettings(initialMs = 1000, maxMs = 1000, multiplier = 1.0, jitterRatio = 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
            override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        )
}

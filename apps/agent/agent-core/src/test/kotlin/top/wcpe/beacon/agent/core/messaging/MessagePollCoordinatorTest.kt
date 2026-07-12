package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
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
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 跨服消息长轮询协调器 [MessagePollCoordinator] 单测（FR-149 §4.2）。
 *
 * 覆盖：取回消息 → 分发到 on(type) 处理器 → 回执 delivered；无 handler 回执 failed；
 * 连接失败退避（下一轮入延迟队列，不刷屏）。用同步 runAsync 使首帧确定性发生，第二次 poll 置 down 令循环停在延迟队列。
 */
class MessagePollCoordinatorTest {
    private val adapter = ManualAsyncAdapter()

    /** 假控制面：poll 首次返消息、之后 down（停循环）；ack 恒 200 并捕获回执报文。 */
    private class MsgFakeTransport : HttpTransport {
        val pollCalls = AtomicInteger(0)
        val ackCalls = AtomicInteger(0)

        override fun execute(request: HttpRequest): HttpResponse =
            when {
                request.url.endsWith("/messages/poll") -> {
                    if (pollCalls.incrementAndGet() == 1) {
                        HttpResponse(200, "poll-msgs")
                    } else {
                        throw RuntimeException("模拟控制面不可达") // exec 吞为 null → Failed，循环退避入延迟队列
                    }
                }

                request.url.endsWith("/messages/ack") -> {
                    ackCalls.incrementAndGet()
                    HttpResponse(200, "ack-ok")
                }

                else -> HttpResponse(404, "")
            }
    }

    /** 捕获 ack 报文、按 body 返回预置树的 codec。 */
    private class MsgCodec(private val msgType: String, private val correlationId: String?) : JsonCodec {
        val lastAck = AtomicReference<Map<String, Any?>>(null)

        @Suppress("UNCHECKED_CAST")
        override fun encode(value: Any?): String {
            val map = value as? Map<String, Any?>
            if (map != null && map.containsKey("results")) lastAck.set(map)
            return "enc"
        }

        override fun decode(json: String): Any? =
            when (json) {
                "poll-msgs" ->
                    mapOf(
                        "messages" to
                            listOf(
                                mapOf(
                                    "messageId" to "m1",
                                    "msgType" to msgType,
                                    "sourceServerId" to "game-7",
                                    "correlationId" to correlationId,
                                    "payload" to mapOf("k" to "v"),
                                    "createdAt" to "1970-01-01T00:00:00.001Z",
                                ),
                            ),
                    )

                "ack-ok" -> mapOf("applied" to 1, "ignored" to 0)
                else -> emptyMap<String, Any?>()
            }
    }

    private fun bus(codec: JsonCodec): MessageBus =
        MessageBus(
            transport = NoopMessageTransport(),
            codec = codec,
            selfServerId = "lobby-1",
            settings = MessagingSettings(enabled = true, rpcTimeoutMs = 1000, streamMaxLen = 0, consumerName = "t"),
        ).also { it.start() }

    @Test
    fun `取回消息分发到 handler 并回执 delivered`() {
        val transport = MsgFakeTransport()
        // 单向消息（correlationId=null）：应路由到 on 处理器。
        val codec = MsgCodec(msgType = "evt", correlationId = null)
        val bus = bus(codec)
        var received: Any? = null
        bus.on("evt") { ctx -> received = ctx.payload() }

        val coord = MessagePollCoordinator(BeaconApiClient(transport, codec, settings()), identity(), adapter, bus)
        coord.configure(waitSec = 1, maxMessages = 10, retryDelayMs = 5000)
        coord.start()

        assertEquals(mapOf("k" to "v"), received, "消息应分发到 on(type) 处理器")
        assertEquals(1, transport.ackCalls.get(), "应回执一次")
        @Suppress("UNCHECKED_CAST")
        val results = transport.let { codec.lastAck.get()!!["results"] as List<Map<String, Any?>> }
        assertEquals("m1", results[0]["messageId"])
        assertEquals("delivered", results[0]["status"])
        // 第二次 poll down → 循环退避入延迟队列（不再同步递归）。
        assertTrue(adapter.delayedCount() >= 1, "连接失败后下一轮应入延迟队列")
    }

    @Test
    fun `无 handler 的消息回执 failed`() {
        val transport = MsgFakeTransport()
        val codec = MsgCodec(msgType = "unknown", correlationId = null)
        val bus = bus(codec)

        val coord = MessagePollCoordinator(BeaconApiClient(transport, codec, settings()), identity(), adapter, bus)
        coord.configure(waitSec = 1, maxMessages = 10, retryDelayMs = 5000)
        coord.start()

        @Suppress("UNCHECKED_CAST")
        val results = codec.lastAck.get()!!["results"] as List<Map<String, Any?>>
        assertEquals("failed", results[0]["status"])
        assertEquals("no_handler_for_type", results[0]["reason"])
    }

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

    private fun settings(): AgentSettings =
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

/** 测试用 no-op 传输：连接态恒真，出站与订阅均不动作（poll 协调器不经此出站）。 */
private class NoopMessageTransport : MessageTransport {
    override fun start() = Unit

    override fun close() = Unit

    override fun isConnected(): Boolean = true

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

package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * HTTP 消息传输 [HttpMessageTransport] 单测：出站信封 → wire 目标映射（server / player 分支）+ 缺 messageId 丢弃。
 *
 * 这是「MessageBus 建信封」与「BeaconApiClient 建 wire」之间的接缝，单独锁定其解信封 + 建 OutboundMessage 逻辑。
 */
class HttpMessageTransportTest {
    private val codec = FakeJsonCodec()

    /** 捕获 send 报文的假控制面：/messages/send 返 200 并记请求。 */
    private class SendCaptureTransport : HttpTransport {
        val lastRequest = AtomicReference<HttpRequest>()

        override fun execute(request: HttpRequest): HttpResponse {
            lastRequest.set(request)
            return HttpResponse(200, "sent-ok")
        }
    }

    /** 首次发送模拟连接失败，恢复开关打开后返回成功。 */
    private class RecoverableSendTransport : HttpTransport {
        val calls = AtomicInteger(0)

        @Volatile
        var available = false

        override fun execute(request: HttpRequest): HttpResponse {
            calls.incrementAndGet()
            check(available) { "模拟控制面失联" }
            return HttpResponse(200, "sent-ok")
        }
    }

    /** 控制面返回 200，但响应体无法解析为发送结果。 */
    private class MalformedSendResponseTransport : HttpTransport {
        val calls = AtomicInteger(0)

        override fun execute(request: HttpRequest): HttpResponse {
            calls.incrementAndGet()
            return HttpResponse(200, "非法发送响应")
        }
    }

    private fun apiClientWith(transport: HttpTransport) = BeaconApiClient(transport, sendResponseCodec(), settings())

    private fun startedHttp(
        transport: HttpTransport,
        warn: (String) -> Unit = {},
        outboundCodec: JsonCodec = codec,
    ): HttpMessageTransport = HttpMessageTransport(apiClientWith(transport), identity(), outboundCodec, warn).also { it.start() }

    /** 用于 BeaconApiClient 内部：encode 走 FakeJsonCodec 存储、decode 识别 sent-ok。 */
    private fun sendResponseCodec(): JsonCodec =
        object : JsonCodec {
            override fun encode(value: Any?): String = codec.encode(value)

            override fun decode(json: String): Any? =
                when (json) {
                    "sent-ok" -> mapOf("messageId" to "m1", "status" to "accepted")
                    "非法发送响应" -> throw IllegalArgumentException("模拟发送响应解析失败")
                    else -> codec.decode(json)
                }
        }

    @Suppress("UNCHECKED_CAST")
    private fun wireBody(transport: SendCaptureTransport): Map<String, Any?> =
        codec.decode(transport.lastRequest.get().body!!) as Map<String, Any?>

    @Test
    fun `server 信封映射为 targetServerId`() {
        val transport = SendCaptureTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "match.invite",
                payload = mapOf("k" to "v"),
                correlationId = "c1",
                messageId = "m1",
                sentAt = 123L,
                targetKind = Message.TARGET_SERVER,
                targetId = "game-7",
            )
        http.sendToServer("game-7", codec.encode(message.toMap()))

        val body = wireBody(transport)
        assertEquals("server", body["targetKind"])
        assertEquals("game-7", body["targetServerId"])
        assertNull(body["targetPlayerUuid"])
        assertEquals("match.invite", body["msgType"])
        assertEquals("m1", body["messageId"])
        assertEquals("c1", body["correlationId"])
    }

    @Test
    fun `player 信封映射为 targetPlayerUuid`() {
        val transport = SendCaptureTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "dm",
                payload = "hi",
                messageId = "m2",
                sentAt = 0L,
                targetKind = Message.TARGET_PLAYER,
                targetId = "Steve",
            )
        http.sendToServer("Steve", codec.encode(message.toMap()))

        val body = wireBody(transport)
        assertEquals("player", body["targetKind"])
        assertEquals("Steve", body["targetPlayerUuid"])
        assertNull(body["targetServerId"])
    }

    @Test
    fun `broadcast 信封 zone 映射为 targetZone 无 server 与 player`() {
        val transport = SendCaptureTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "chat.global",
                payload = "hi",
                messageId = "m3",
                sentAt = 0L,
                targetKind = Message.TARGET_BROADCAST,
                targetId = "zone-pvp",
                broadcast = true,
            )
        http.publishTopic("chat.global", codec.encode(message.toMap()))

        val body = wireBody(transport)
        assertEquals("broadcast", body["targetKind"])
        assertEquals("zone-pvp", body["targetZone"])
        assertNull(body["targetServerId"])
        assertNull(body["targetPlayerUuid"])
        assertEquals("chat.global", body["msgType"])
    }

    @Test
    fun `broadcast 信封无 zone 时 targetZone 省略`() {
        val transport = SendCaptureTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "cache.invalidate",
                payload = null,
                messageId = "m4",
                sentAt = 0L,
                targetKind = Message.TARGET_BROADCAST,
                targetId = null,
                broadcast = true,
            )
        http.publishTopic("cache.invalidate", codec.encode(message.toMap()))

        val body = wireBody(transport)
        assertEquals("broadcast", body["targetKind"])
        assertNull(body["targetZone"], "无 zone 定向时 targetZone 省略（全 namespace 广播）")
    }

    @Test
    fun `缺 messageId 的信封被丢弃不发请求`() {
        val transport = SendCaptureTransport()
        val warns = mutableListOf<String>()
        val http = startedHttp(transport, warn = warns::add)
        val message = Message(type = "t", payload = null, targetKind = Message.TARGET_SERVER, targetId = "B")
        http.sendToServer("B", codec.encode(message.toMap()))

        assertNull(transport.lastRequest.get(), "缺 messageId 不应发送")
        assertTrue(warns.any { it.contains("messageId") })
    }

    @Test
    fun `非法 rawJson 解码异常后标记不可用并抛出保留 cause 的异常`() {
        val transport = SendCaptureTransport()
        val decodeFailure = IllegalArgumentException("模拟非法 JSON")
        val failingCodec =
            object : JsonCodec {
                override fun encode(value: Any?): String = codec.encode(value)

                override fun decode(json: String): Any? = throw decodeFailure
            }
        val http = startedHttp(transport, outboundCodec = failingCodec)

        val failure = assertFailsWith<IllegalStateException> { http.sendToServer("game-1", "非法 JSON") }

        assertEquals(decodeFailure, failure.cause)
        assertFalse(http.isConnected(), "codec 解码异常后 transport 必须 fail-closed")
        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", "非法 JSON") }
        assertNull(transport.lastRequest.get(), "解码失败及后续快速失败均不得请求控制面")
    }

    @Test
    fun `Message fromMap 解析失败后标记不可用并快速失败`() {
        val transport = SendCaptureTransport()
        val http = startedHttp(transport)
        val raw = codec.encode(mapOf("payload" to "缺少 type"))

        val failure = assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }

        assertTrue(failure.cause is IllegalArgumentException, "信封结构解析失败应保留具体 cause")
        assertFalse(http.isConnected(), "Message.fromMap 解析失败后 transport 必须 fail-closed")
        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }
        assertNull(transport.lastRequest.get(), "解析失败及后续快速失败均不得请求控制面")
    }

    @Test
    fun `发送失联后标记不可用并快速失败且可由重连恢复`() {
        val transport = RecoverableSendTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "evt",
                payload = null,
                messageId = "m-fail",
                targetKind = Message.TARGET_SERVER,
                targetId = "game-1",
            )
        val raw = codec.encode(message.toMap())

        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }
        assertFalse(http.isConnected(), "发送连接失败后 transport 必须反映失联")
        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }
        assertEquals(1, transport.calls.get(), "失联后后续发送必须快速失败，不再请求控制面")

        transport.available = true
        http.start()
        http.sendToServer("game-1", raw)
        assertTrue(http.isConnected(), "重连后成功发送应恢复连接态")
        assertEquals(2, transport.calls.get())
    }

    @Test
    fun `发送接口返回 200 非法响应体后标记不可用并快速失败`() {
        val transport = MalformedSendResponseTransport()
        val http = startedHttp(transport)
        val message =
            Message(
                type = "evt",
                payload = null,
                messageId = "m-malformed",
                targetKind = Message.TARGET_SERVER,
                targetId = "game-1",
            )
        val raw = codec.encode(message.toMap())

        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }
        assertFalse(http.isConnected(), "发送响应解析异常后 transport 必须 fail-closed")
        assertFailsWith<IllegalStateException> { http.sendToServer("game-1", raw) }
        assertEquals(1, transport.calls.get(), "解析异常后后续发送必须快速失败")
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

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
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
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

    private fun apiClientWith(transport: HttpTransport) = BeaconApiClient(transport, sendResponseCodec(), settings())

    /** 用于 BeaconApiClient 内部：encode 走 FakeJsonCodec 存储、decode 识别 sent-ok。 */
    private fun sendResponseCodec(): JsonCodec =
        object : JsonCodec {
            override fun encode(value: Any?): String = codec.encode(value)

            override fun decode(json: String): Any? =
                if (json == "sent-ok") mapOf("messageId" to "m1", "status" to "accepted") else codec.decode(json)
        }

    @Suppress("UNCHECKED_CAST")
    private fun wireBody(transport: SendCaptureTransport): Map<String, Any?> =
        codec.decode(transport.lastRequest.get().body!!) as Map<String, Any?>

    @Test
    fun `server 信封映射为 targetServerId`() {
        val transport = SendCaptureTransport()
        val http = HttpMessageTransport(apiClientWith(transport), identity(), codec)
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
        val http = HttpMessageTransport(apiClientWith(transport), identity(), codec)
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
        val http = HttpMessageTransport(apiClientWith(transport), identity(), codec)
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
        val http = HttpMessageTransport(apiClientWith(transport), identity(), codec)
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
        val http = HttpMessageTransport(apiClientWith(transport), identity(), codec, warn = warns::add)
        val message = Message(type = "t", payload = null, targetKind = Message.TARGET_SERVER, targetId = "B")
        http.sendToServer("B", codec.encode(message.toMap()))

        assertNull(transport.lastRequest.get(), "缺 messageId 不应发送")
        assertTrue(warns.any { it.contains("messageId") })
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

package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.messaging.Message
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
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 跨服消息三端点契约单测（FR-149 §5.1，冻结 wire）。
 *
 * 锁定：send/poll/ack 端点路径与鉴权头、报文键集（camelCase）、server/player 目标分支、
 * sentAt/deliveredAt UTC ISO8601、状态码 → outcome 映射（send 200/403/400、poll 200/204、ack 200）。
 */
class BeaconApiClientMessagesTest {
    private class CapturingCodec(private val decodeBody: (String) -> Any?) : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? = decodeBody(json)
    }

    private class StatusTransport(private val status: Int, private val body: String = "") : HttpTransport {
        val lastRequest = AtomicReference<HttpRequest>()

        override fun execute(request: HttpRequest): HttpResponse {
            lastRequest.set(request)
            return HttpResponse(status, body)
        }
    }

    private fun settings() =
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

    private fun identity() =
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

    @Suppress("UNCHECKED_CAST")
    private fun envelope(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    private fun serverMessage() =
        OutboundMessage(
            messageId = "0190a1b2-c3d4-7000-8000-000000000001",
            msgType = "match.invite",
            targetKind = Message.TARGET_SERVER,
            targetServerId = "game-7",
            targetPlayerUuid = null,
            correlationId = "0190a1b2-c3d4-7000-8000-000000000001",
            payload = mapOf("k" to "v"),
            sentAtMs = 123L,
        )

    @Test
    fun `send server 目标键集与鉴权头符合契约`() {
        val codec = CapturingCodec { mapOf("messageId" to "0190a1b2-c3d4-7000-8000-000000000001", "status" to "accepted") }
        val transport = StatusTransport(200)
        val outcome = BeaconApiClient(transport, codec, settings()).sendMessage(identity(), serverMessage())

        val ok = assertIs<MessageSendOutcome.Ok>(outcome)
        assertEquals("accepted", ok.status)
        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/messages/send"))
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers[BeaconApiClient.HEADER_IDENTITY])
        val body = envelope(codec)
        assertEquals(
            setOf("messageId", "msgType", "targetKind", "targetServerId", "correlationId", "payload", "sentAt"),
            body.keys,
            "server 目标报文键集须与契约一致",
        )
        assertEquals("server", body["targetKind"])
        assertEquals("game-7", body["targetServerId"])
        assertEquals("1970-01-01T00:00:00.123Z", body["sentAt"], "sentAt 须为 UTC ISO8601")
    }

    @Test
    fun `send player 目标用 targetPlayerUuid 且无 targetServerId`() {
        val codec = CapturingCodec { mapOf("messageId" to "m", "status" to "accepted") }
        BeaconApiClient(StatusTransport(200), codec, settings()).sendMessage(
            identity(),
            OutboundMessage(
                messageId = "m1",
                msgType = "dm",
                targetKind = Message.TARGET_PLAYER,
                targetServerId = null,
                targetPlayerUuid = "11111111-1111-1111-1111-111111111111",
                correlationId = null,
                payload = "hi",
                sentAtMs = 0L,
            ),
        )
        val body = envelope(codec)
        assertEquals(setOf("messageId", "msgType", "targetKind", "targetPlayerUuid", "payload", "sentAt"), body.keys)
        assertEquals("player", body["targetKind"])
        assertEquals("11111111-1111-1111-1111-111111111111", body["targetPlayerUuid"])
        assertNull(body["correlationId"], "无 correlationId 时省略")
    }

    @Test
    fun `send 403 跨域无信任 400 被拒`() {
        val codec = CapturingCodec { mapOf("code" to "payload_too_large") }
        assertIs<MessageSendOutcome.Forbidden>(
            BeaconApiClient(StatusTransport(403), codec, settings()).sendMessage(identity(), serverMessage()),
        )
        val rejected =
            assertIs<MessageSendOutcome.Rejected>(
                BeaconApiClient(StatusTransport(400, "err"), codec, settings()).sendMessage(identity(), serverMessage()),
            )
        assertEquals("payload_too_large", rejected.reason)
    }

    @Test
    fun `poll 200 解析消息 204 为空`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "messages" to
                        listOf(
                            mapOf(
                                "messageId" to "m1",
                                "msgType" to "match.invite",
                                "sourceServerId" to "game-7",
                                "correlationId" to "req-1",
                                "payload" to mapOf("k" to "v"),
                                "createdAt" to "1970-01-01T00:00:00.001Z",
                            ),
                        ),
                )
            }
        val transport = StatusTransport(200)
        val outcome = BeaconApiClient(transport, codec, settings()).pollMessages(identity(), waitSec = 20, max = 50)

        val messages = assertIs<MessagePollOutcome.Messages>(outcome)
        assertEquals(1, messages.messages.size)
        val m = messages.messages.first()
        assertEquals("m1", m.messageId)
        assertEquals("match.invite", m.msgType)
        assertEquals("game-7", m.sourceServerId)
        assertEquals("req-1", m.correlationId)
        assertTrue(transport.lastRequest.get().url.endsWith("/beacon/v2/agent/messages/poll"))
        // 报文键集。
        val body = envelope(codec)
        assertEquals(setOf("waitSec", "max"), body.keys)
        assertEquals(20, body["waitSec"])

        assertIs<MessagePollOutcome.Empty>(
            BeaconApiClient(StatusTransport(204), codec, settings()).pollMessages(identity(), 20, 50),
        )
    }

    @Test
    fun `ack 报文键集与状态映射`() {
        val codec = CapturingCodec { mapOf("applied" to 2, "ignored" to 1) }
        val transport = StatusTransport(200)
        val outcome =
            BeaconApiClient(transport, codec, settings()).ackMessages(
                identity(),
                listOf(
                    MessageAck("m1", "delivered", null, deliveredAtMs = 123L, handlerCostMs = 15L),
                    MessageAck("m2", "failed", "no_handler_for_type", deliveredAtMs = 456L, handlerCostMs = null),
                ),
            )

        val applied = assertIs<MessageAckOutcome.Applied>(outcome)
        assertEquals(2, applied.applied)
        assertEquals(1, applied.ignored)
        assertTrue(transport.lastRequest.get().url.endsWith("/beacon/v2/agent/messages/ack"))
        @Suppress("UNCHECKED_CAST")
        val results = envelope(codec)["results"] as List<Map<String, Any?>>
        assertEquals(setOf("messageId", "status", "deliveredAt", "handlerCostMs"), results[0].keys)
        assertEquals("delivered", results[0]["status"])
        assertEquals("1970-01-01T00:00:00.123Z", results[0]["deliveredAt"])
        assertEquals(15L, results[0]["handlerCostMs"])
        // 失败项带 reason、无 handlerCostMs。
        assertEquals(setOf("messageId", "status", "reason", "deliveredAt"), results[1].keys)
        assertEquals("no_handler_for_type", results[1]["reason"])
    }
}

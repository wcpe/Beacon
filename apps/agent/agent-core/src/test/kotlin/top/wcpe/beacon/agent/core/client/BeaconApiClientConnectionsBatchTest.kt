package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.connection.ConnectionEvent
import top.wcpe.beacon.agent.core.connection.ConnectionEventKind
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
import kotlin.test.assertIs
import kotlin.test.assertTrue

/**
 * BeaconApiClient.reportConnectionsBatch 契约单测（FR-145 §5.1，冻结 wire）。
 *
 * 锁定：v2 端点路径、鉴权头、报文键集（bootId/droppedCount/events）、event 元素键集（camelCase）、
 * 空可选字段省略、kind wire 值、时间 UTC ISO8601、状态码 → outcome 映射（202/429/403）。
 */
class BeaconApiClientConnectionsBatchTest {
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
            serverId = "proxy-1",
            role = "bungee",
            groupHint = "area1",
            address = "127.0.0.1:25577",
            version = "1.0",
            capacity = 0,
            weight = 1,
            metadata = emptyMap(),
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    private fun openEvent() =
        ConnectionEvent(
            kind = ConnectionEventKind.OPEN,
            connId = "0190a1b2-c3d4-7000-8000-000000000001",
            playerUuid = "11111111-1111-1111-1111-111111111111",
            playerName = "Steve",
            clientIp = "1.2.3.4",
            protocolVersion = 763,
            openedAtMs = 123L,
            closedAtMs = null,
            closeKind = null,
            closeReason = null,
            firstBackend = null,
            lastBackend = null,
            backendSwitchCount = null,
        )

    private fun closeEvent() =
        ConnectionEvent(
            kind = ConnectionEventKind.CLOSE,
            connId = "0190a1b2-c3d4-7000-8000-000000000001",
            playerUuid = "11111111-1111-1111-1111-111111111111",
            playerName = "Steve",
            clientIp = null,
            protocolVersion = null,
            openedAtMs = 123L,
            closedAtMs = 60_456L,
            closeKind = "quit",
            closeReason = "客户端断开",
            firstBackend = "lobby-1",
            lastBackend = "game-7",
            backendSwitchCount = 2,
        )

    @Suppress("UNCHECKED_CAST")
    private fun envelope(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    @Suppress("UNCHECKED_CAST")
    private fun firstEvent(codec: CapturingCodec): Map<String, Any?> = (envelope(codec)["events"] as List<Map<String, Any?>>).first()

    @Test
    fun `信封键与鉴权头与端点符合契约`() {
        val codec = CapturingCodec { mapOf("accepted" to 2, "duplicated" to 0) }
        val transport = StatusTransport(202)
        val outcome =
            BeaconApiClient(transport, codec, settings())
                .reportConnectionsBatch(identity(), bootId = "boot-9", droppedCount = 3L, events = listOf(openEvent()))

        val accepted = assertIs<ConnectionsReportOutcome.Accepted>(outcome)
        assertEquals(2, accepted.accepted)
        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/connections/batch"), "应打 v2 连接批上报端点")
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers[BeaconApiClient.HEADER_IDENTITY])
        val body = envelope(codec)
        assertEquals(setOf("bootId", "droppedCount", "events"), body.keys, "信封键集合须与契约一致")
        assertEquals("boot-9", body["bootId"])
        assertEquals(3L, body["droppedCount"])
    }

    @Test
    fun `open 事件元素键集与时间格式`() {
        val codec = CapturingCodec { mapOf("accepted" to 1) }
        BeaconApiClient(StatusTransport(202), codec, settings())
            .reportConnectionsBatch(identity(), "b", 0L, listOf(openEvent()))

        val e = firstEvent(codec)
        assertEquals(
            setOf("kind", "connId", "playerUuid", "playerName", "clientIp", "protocolVersion", "openedAt"),
            e.keys,
            "open 事件应只含非空字段（close 特有字段省略）",
        )
        assertEquals("open", e["kind"])
        assertEquals("1970-01-01T00:00:00.123Z", e["openedAt"], "openedAt 须为 UTC ISO8601")
        assertEquals(763, e["protocolVersion"])
    }

    @Test
    fun `close 事件携全部会话摘要字段`() {
        val codec = CapturingCodec { mapOf("accepted" to 1) }
        BeaconApiClient(StatusTransport(202), codec, settings())
            .reportConnectionsBatch(identity(), "b", 0L, listOf(closeEvent()))

        val e = firstEvent(codec)
        assertEquals("close", e["kind"])
        assertEquals("1970-01-01T00:01:00.456Z", e["closedAt"])
        assertEquals("quit", e["closeKind"])
        assertEquals("客户端断开", e["closeReason"])
        assertEquals("lobby-1", e["firstBackend"])
        assertEquals("game-7", e["lastBackend"])
        assertEquals(2, e["backendSwitchCount"])
    }

    @Test
    fun `429 为忙 403 为未确认`() {
        val codec = CapturingCodec { emptyMap<String, Any?>() }
        assertIs<ConnectionsReportOutcome.Busy>(
            BeaconApiClient(StatusTransport(429), codec, settings())
                .reportConnectionsBatch(identity(), "b", 0L, listOf(openEvent())),
        )
        assertIs<ConnectionsReportOutcome.Forbidden>(
            BeaconApiClient(StatusTransport(403), codec, settings())
                .reportConnectionsBatch(identity(), "b", 0L, listOf(openEvent())),
        )
    }
}

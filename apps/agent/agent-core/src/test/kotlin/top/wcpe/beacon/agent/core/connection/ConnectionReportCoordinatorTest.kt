package top.wcpe.beacon.agent.core.connection

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
 * 连接批上报协调器 [ConnectionReportCoordinator] 单测（FR-145 §4.1）。
 *
 * 覆盖：周期 tick 上报 202 后 ack 清缓冲、空缓冲不上报、flushNow 即时上报、失败保留缓冲、warn-once。
 */
class ConnectionReportCoordinatorTest {
    private val transport = ConnFakeTransport()
    private val codec = CapturingCodec()
    private val adapter = ManualAsyncAdapter()
    private val buffer = ConnectionEventBuffer(capacity = 100)
    private val apiClient = BeaconApiClient(transport, codec, settings())

    private fun coordinator(): ConnectionReportCoordinator =
        ConnectionReportCoordinator(adapter, apiClient, identity(), buffer, bootId = "boot-1")

    private fun openEvent(connId: String): ConnectionEvent =
        ConnectionEvent(
            kind = ConnectionEventKind.OPEN,
            connId = connId,
            playerUuid = "u",
            playerName = "p",
            clientIp = null,
            protocolVersion = null,
            openedAtMs = 1000L,
            closedAtMs = null,
            closeKind = null,
            closeReason = null,
            firstBackend = null,
            lastBackend = null,
            backendSwitchCount = null,
        )

    @Test
    fun `周期 tick 上报 202 后 ack 清缓冲且报文含 bootId 与 events`() {
        buffer.add(openEvent("c1"))
        buffer.add(openEvent("c2"))
        coordinator().start()
        adapter.drainOne() // 首个周期 tick

        assertEquals(1, transport.calls.get(), "应 POST 一次批上报")
        assertEquals(0, buffer.size(), "202 受理后应 ack 移除已上报事件")
        val body = codec.lastEncoded.get()!!
        assertEquals("boot-1", body["bootId"])
        assertEquals(0L, body["droppedCount"])
        @Suppress("UNCHECKED_CAST")
        val events = body["events"] as List<Map<String, Any?>>
        assertEquals(2, events.size)
        assertEquals("open", events.first()["kind"])
        assertEquals("c1", events.first()["connId"])
    }

    @Test
    fun `空缓冲不发上报请求`() {
        coordinator().start()
        adapter.drainOne()
        assertEquals(0, transport.calls.get())
    }

    @Test
    fun `flushNow 即时上报满阈值缓冲`() {
        val coord = coordinator()
        coord.start()
        buffer.add(openEvent("c1"))
        coord.flushNow()
        assertEquals(1, transport.calls.get(), "flushNow 应即时触发一次上报")
        assertEquals(0, buffer.size())
    }

    @Test
    fun `上报失败保留缓冲并 warn 一次`() {
        transport.status = 429
        buffer.add(openEvent("c1"))
        val coord = coordinator()
        coord.start()
        adapter.drainOne() // 失败一次
        assertEquals(1, buffer.size(), "失败应保留缓冲")
        assertTrue(adapter.warns.any { it.contains("批上报失败") })
        // 再失败一次不重复 warn（warn-once）。
        val warnsBefore = adapter.warns.size
        adapter.drainOne()
        assertEquals(warnsBefore, adapter.warns.size, "连续失败不刷屏")
    }

    @Test
    fun `丢弃计数随批上报`() {
        val small = ConnectionEventBuffer(capacity = 1)
        small.add(openEvent("c1"))
        small.add(openEvent("c2")) // 覆盖 c1，dropped=1
        val coord = ConnectionReportCoordinator(adapter, apiClient, identity(), small, bootId = "boot-1")
        coord.start()
        adapter.drainOne()
        assertEquals(1L, codec.lastEncoded.get()!!["droppedCount"], "droppedCount 应随批上报")
    }

    /** 假控制面：/connections/batch 返回可切状态码，计数调用。 */
    private class ConnFakeTransport : HttpTransport {
        @Volatile
        var status: Int = 202
        val calls = AtomicInteger(0)

        override fun execute(request: HttpRequest): HttpResponse {
            if (request.url.contains("/connections/batch")) {
                calls.incrementAndGet()
                return HttpResponse(status, "conn-ok")
            }
            return HttpResponse(404, "")
        }
    }

    /** 捕获出站信封的 codec；decode 返回受理计数。 */
    private class CapturingCodec : JsonCodec {
        val lastEncoded = AtomicReference<Map<String, Any?>>(null)

        @Suppress("UNCHECKED_CAST")
        override fun encode(value: Any?): String {
            lastEncoded.set(value as? Map<String, Any?>)
            return "encoded"
        }

        override fun decode(json: String): Any? = mapOf("accepted" to 2, "duplicated" to 0)
    }

    private fun identity(): AgentIdentity =
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
            bootId = "boot-1",
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

package top.wcpe.beacon.agent.core.client

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
import kotlin.test.assertTrue

/**
 * BeaconApiClient.report 报文契约单测（FR-32 相位1）。
 *
 * 重点：report 载荷在既有 playerCount/tps 之外新增 memUsed/memMax/cpuLoad 三键，
 * 键名与类型固定（供控制面 Go 侧对齐）；cpuLoad 不可用时如实发 -1.0。
 */
class BeaconApiClientReportTest {

    /** 捕获 encode 入参的 codec：把待序列化的 Map 暴露给断言。 */
    private class CapturingCodec : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "captured"
        }

        override fun decode(json: String): Any? = emptyMap<String, Any?>()
    }

    /** 始终对 /report 返回 200 的 transport。 */
    private class OkReportTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(200, "")
    }

    private fun identity() = AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "127.0.0.1:25565",
        version = "1.0",
        capacity = 100,
        weight = 1,
        metadata = emptyMap(),
    )

    private fun settings() = AgentSettings(
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

    @Suppress("UNCHECKED_CAST")
    private fun lastBody(codec: CapturingCodec): Map<String, Any?> =
        codec.lastEncoded.get() as Map<String, Any?>

    @Test
    fun `report 载荷含人数TPS与新增内存CPU字段`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkReportTransport(), codec, settings())

        val ok = client.report(
            identity(),
            appliedMd5 = "md5-x",
            playerCount = 8,
            tps = 19.5,
            memUsed = 123L,
            memMax = 456L,
            cpuLoad = 0.25,
        )
        assertTrue(ok, "report 应在 200 时返回 true")

        val body = lastBody(codec)
        // 既有字段保持。
        assertEquals("prod", body["namespace"])
        assertEquals("lobby-1", body["serverId"])
        assertEquals("md5-x", body["appliedMd5"])
        assertEquals(8, body["playerCount"])
        assertEquals(19.5, body["tps"])
        // 新增字段：键名固定 memUsed/memMax/cpuLoad。
        assertEquals(123L, body["memUsed"])
        assertEquals(456L, body["memMax"])
        assertEquals(0.25, body["cpuLoad"])
    }

    @Test
    fun `report 在 CPU 不可用时如实发 -1`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkReportTransport(), codec, settings())

        client.report(
            identity(),
            appliedMd5 = "md5-x",
            playerCount = 0,
            tps = 0.0,
            memUsed = 1L,
            memMax = 2L,
            cpuLoad = -1.0,
        )

        val body = lastBody(codec)
        assertEquals(-1.0, body["cpuLoad"], "CPU 不可用应原样发 -1.0，由控制面判定不可用")
    }

    @Test
    fun `report 报文键名集合精确为约定字段`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkReportTransport(), codec, settings())

        client.report(identity(), "md5", 1, 20.0, 10L, 20L, 0.5)

        val body = lastBody(codec)
        // 锁定报文键集合，防止漏键 / 多键漂移（与控制面 Go 侧契约对齐）。
        assertEquals(
            setOf("namespace", "serverId", "appliedMd5", "playerCount", "tps", "memUsed", "memMax", "cpuLoad"),
            body.keys,
            "report 报文键集合必须与契约一致",
        )
    }
}

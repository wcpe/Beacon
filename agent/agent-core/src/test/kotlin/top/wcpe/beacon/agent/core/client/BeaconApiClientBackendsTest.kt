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
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * BeaconApiClient register / report 携带 bc 后端归属集合（backends）的报文契约单测（FR-36）。
 *
 * 重点：backends 为运行期事实（非身份），由调用方按帧传入；仅当非空时才拼入报文键
 * （旧 agent / bukkit 传空、向后兼容）；键名固定为 backends（供控制面 Go 侧对齐）。
 */
class BeaconApiClientBackendsTest {

    /** 捕获 encode 入参的 codec：把待序列化的 Map 暴露给断言。 */
    private class CapturingCodec : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "captured"
        }

        // register 解析需要一个最小响应对象。
        override fun decode(json: String): Any? = mapOf(
            "instanceKey" to "prod/bc-1",
            "heartbeatIntervalSec" to 10,
            "ttlSec" to 30,
            "assigned" to false,
        )
    }

    private class OkTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(200, "")
    }

    private fun identity() = AgentIdentity(
        namespace = "prod",
        serverId = "bc-1",
        role = "bungee",
        groupHint = "area1",
        address = "127.0.0.1:25577",
        version = "1.0",
        capacity = 0,
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
    fun `register 携带非空 backends`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        client.register(identity(), backends = listOf("lobby-1", "lobby-2"))

        val body = lastBody(codec)
        assertEquals(listOf("lobby-1", "lobby-2"), body["backends"])
    }

    @Test
    fun `register 在 backends 为空时不拼键`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        // 默认空 backends（bukkit / 旧行为）。
        client.register(identity())

        val body = lastBody(codec)
        assertFalse(body.containsKey("backends"), "空 backends 不应拼入报文（向后兼容旧控制面/bukkit）")
    }

    @Test
    fun `report 携带非空 backends`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        val ok = client.report(
            identity(),
            appliedMd5 = "m",
            playerCount = 3,
            tps = 0.0,
            memUsed = 1L,
            memMax = 2L,
            cpuLoad = 0.1,
            backends = listOf("lobby-1"),
        )
        assertTrue(ok)

        val body = lastBody(codec)
        assertEquals(listOf("lobby-1"), body["backends"])
    }

    @Test
    fun `report 在 backends 为空时不拼键`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        // 默认空 backends（bukkit / 旧行为）。
        client.report(identity(), "m", 0, 0.0, 1L, 2L, 0.1)

        val body = lastBody(codec)
        assertFalse(body.containsKey("backends"), "空 backends 不应拼入 report 报文")
        // 既有键集合保持不变（不漏不多）。
        assertEquals(
            setOf("namespace", "serverId", "appliedMd5", "playerCount", "tps", "memUsed", "memMax", "cpuLoad"),
            body.keys,
        )
    }
}

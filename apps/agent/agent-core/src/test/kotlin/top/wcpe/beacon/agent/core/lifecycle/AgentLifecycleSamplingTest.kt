package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * AgentLifecycle v2 指标采样 + 批上报循环的集成单测（FR-144）。
 *
 * 用 [ThreadPoolPlatformAdapter] 真并发 + waitUntil，覆盖：启用后周期批上报到 v2 端点、
 * 断连（5xx）保留缓冲重试、恢复后一次批把断连期积压的多个已闭合桶补报、shutdown 停循环。
 * 采样 / 上报 / 桶宽经 enableMetricsSampling 加速到毫秒级以在测试窗口内成立。
 */
class AgentLifecycleSamplingTest {
    private val backend = FakeBeaconBackend()
    private val adapter = ThreadPoolPlatformAdapter()
    private val store = EffectiveConfigStore()

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
        )

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
            override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        )

    private val fixedMetrics =
        RuntimeMetrics(playerCount = 7, tps = 19.5, memUsed = 256L * 1024 * 1024, memMax = 512L * 1024 * 1024, cpuLoad = 0.4)

    private fun newLifecycle(codec: JsonCodec): AgentLifecycle {
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null, metricsProvider = { fixedMetrics })
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `启用采样后周期批上报到 v2 端点`() {
        val lifecycle = newLifecycle(CannedJsonCodec())
        // 加速：30ms 采样、60ms 上报、100ms 桶——数百毫秒内即有已闭合桶被上报。
        lifecycle.enableMetricsSampling(sampleIntervalMs = 30, batchReportIntervalMs = 60, bucketMs = 100)
        lifecycle.bootstrapWithSnapshotThenConnect()

        waitUntil(4000) { backend.metricsReportCalls.get() >= 1 }
        assertTrue(backend.metricsReportCalls.get() >= 1, "启用采样后应周期批上报到 v2 指标端点")
    }

    @Test
    fun `未启用采样时不上报 v2 指标`() {
        val lifecycle = newLifecycle(CannedJsonCodec())
        // 不调 enableMetricsSampling（向后兼容旧行为）。
        lifecycle.bootstrapWithSnapshotThenConnect()

        // 给足时间让其它循环跑起来，确认 v2 指标端点始终无调用。
        Thread.sleep(600)
        assertTrue(backend.metricsReportCalls.get() == 0, "未启用采样不应打 v2 指标端点")
    }

    @Test
    fun `断连保留缓冲恢复后一次补报积压多桶`() {
        val codec = BatchCapturingCodec()
        // 断连：v2 指标端点先返 500。
        backend.metricsReportStatus = 500
        val lifecycle = newLifecycle(codec)
        lifecycle.enableMetricsSampling(sampleIntervalMs = 25, batchReportIntervalMs = 50, bucketMs = 100)
        lifecycle.bootstrapWithSnapshotThenConnect()

        // 断连期：多次上报被拒（500），样本在缓冲积压不丢（未 ack）。等约半秒让 ≥4 个 100ms 桶闭合。
        waitUntil(4000) { backend.metricsReportCalls.get() >= 2 }
        assertTrue(backend.metricsReportCalls.get() >= 2, "断连期应按固定节奏重试上报")
        Thread.sleep(500)

        // 只观察恢复后的批：清空捕获再恢复，确保 max 反映的是 202 成功批而非断连期失败批。
        codec.maxSamplesInBatch.set(0)
        // 恢复：端点返 202，下一批把积压的多个已闭合桶一次补报。
        backend.metricsReportStatus = 202
        waitUntil(4000) { codec.maxSamplesInBatch.get() >= 2 }
        assertTrue(
            codec.maxSamplesInBatch.get() >= 2,
            "恢复后应一次批补报断连期积压的多个已闭合桶（实测最大批含 ${codec.maxSamplesInBatch.get()} 桶）",
        )
    }

    @Test
    fun `shutdown 停止采样与批上报循环`() {
        val lifecycle = newLifecycle(CannedJsonCodec())
        lifecycle.enableMetricsSampling(sampleIntervalMs = 30, batchReportIntervalMs = 60, bucketMs = 100)
        lifecycle.bootstrapWithSnapshotThenConnect()

        waitUntil(4000) { backend.metricsReportCalls.get() >= 1 }
        lifecycle.shutdown()
        // 停机后再取一次基准，静默一段时间后计数不应再增长（循环已退出）。
        Thread.sleep(300)
        val afterShutdown = backend.metricsReportCalls.get()
        Thread.sleep(400)
        assertTrue(
            backend.metricsReportCalls.get() == afterShutdown,
            "shutdown 后采样 / 批上报循环应停止，不再有新的 v2 指标上报",
        )
    }

    private fun waitUntil(
        timeoutMs: Long,
        cond: () -> Boolean,
    ) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}

/**
 * 测试用 codec：解析复用 [CannedJsonCodec]，encode 捕获 v2 指标批报文（含 samples + agentTimeMs 键）的
 * 最大 samples 桶数——用于断言「断连恢复后一次批补报多桶」。
 */
private class BatchCapturingCodec : JsonCodec {
    private val canned = CannedJsonCodec()

    /** 历次批报文中 samples 列表的最大长度（桶数）。 */
    val maxSamplesInBatch = AtomicInteger(0)

    override fun encode(value: Any?): String {
        if (value is Map<*, *> && value.containsKey("samples") && value.containsKey("agentTimeMs")) {
            val samples = value["samples"] as? List<*>
            if (samples != null) {
                maxSamplesInBatch.updateAndGet { if (samples.size > it) samples.size else it }
            }
        }
        return canned.encode(value)
    }

    override fun decode(json: String): Any? = canned.decode(json)
}

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
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.MetricsCapturingCodec
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * AgentLifecycle 上报时取注入指标供给的单测（FR-32 相位1）。
 *
 * 验证：lifecycle 在 reportApplied 时调用注入的 metricsProvider 取当前指标，
 * 并把 memUsed/memMax/cpuLoad 与真实 playerCount/tps 发进 report 报文；
 * 未注入供给时按零指标上报（向后兼容）。
 */
class AgentLifecycleMetricsTest {

    private val backend = FakeBeaconBackend()
    private val adapter = ThreadPoolPlatformAdapter()
    private val store = EffectiveConfigStore()

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
        backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `上报时取注入指标供给的真实值`() {
        backend.pollStatus = 200
        val codec = MetricsCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        val fixed = RuntimeMetrics(
            playerCount = 17,
            tps = 19.7,
            memUsed = 333L,
            memMax = 777L,
            cpuLoad = 0.55,
        )
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            metricsProvider = { fixed },
        )

        lifecycle.bootstrapWithSnapshotThenConnect()
        // 等首轮 apply→report 发生（pollStatus=200 触发 report）。
        waitUntil(3000) { codec.lastReport.get() != null }

        val body = codec.lastReport.get() ?: error("应至少有一次 report 报文被捕获")
        assertEquals(17, body["playerCount"], "应上报供给的真实在线人数")
        assertEquals(19.7, body["tps"], "应上报供给的真实 TPS")
        assertEquals(333L, body["memUsed"])
        assertEquals(777L, body["memMax"])
        assertEquals(0.55, body["cpuLoad"])
    }

    @Test
    fun `未注入供给时按零指标上报向后兼容`() {
        backend.pollStatus = 200
        val codec = MetricsCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        // 不传 metricsProvider，走默认零指标。
        val lifecycle = AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null)

        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(3000) { codec.lastReport.get() != null }

        val body = codec.lastReport.get() ?: error("应至少有一次 report 报文被捕获")
        assertEquals(0, body["playerCount"])
        assertEquals(0.0, body["tps"])
        assertEquals(0L, body["memUsed"])
        assertEquals(0L, body["memMax"])
        // 默认指标 cpuLoad 为不可用哨兵 -1.0。
        assertEquals(RuntimeMetrics.CPU_UNAVAILABLE, body["cpuLoad"])
    }

    @Test
    fun `每次上报重新取指标供给`() {
        backend.pollStatus = 200
        val codec = MetricsCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        var calls = 0
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            metricsProvider = {
                calls++
                RuntimeMetrics.ZERO
            },
        )

        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(3000) { calls >= 1 }
        assertTrue(calls >= 1, "上报时应调用 metricsProvider 取当前指标")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}

package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.MetricsCapturingCodec
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
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
    fun `bc 上报时附注入的 BC 专属指标 proxy 段`() {
        backend.pollStatus = 200
        val codec = MetricsCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        val proxy = ProxyMetrics(
            onlineConnections = 99,
            threadCount = 50,
            uptimeMs = 12345L,
            backendUp = 2,
            backendTotal = 3,
            backendAvgLatencyMs = 8.0,
        )
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            proxyMetricsProvider = { proxy },
        )

        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(3000) { codec.lastReport.get() != null }

        val body = codec.lastReport.get() ?: error("应至少有一次 report 报文被捕获")
        @Suppress("UNCHECKED_CAST")
        val proxyBody = body["proxy"] as? Map<String, Any?> ?: error("bc 上报应含 proxy 子对象")
        assertEquals(99, proxyBody["onlineConnections"])
        assertEquals(50, proxyBody["threadCount"])
        assertEquals(12345L, proxyBody["uptimeMs"])
        assertEquals(2, proxyBody["backendUp"])
        assertEquals(3, proxyBody["backendTotal"])
        assertEquals(8.0, proxyBody["backendAvgLatencyMs"])
    }

    @Test
    fun `未注入 BC 指标供给时不附 proxy 段向后兼容`() {
        backend.pollStatus = 200
        val codec = MetricsCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        // 不传 proxyMetricsProvider（bukkit / 旧行为）。
        val lifecycle = AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null)

        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(3000) { codec.lastReport.get() != null }

        val body = codec.lastReport.get() ?: error("应至少有一次 report 报文被捕获")
        assertTrue(!body.containsKey("proxy"), "bukkit / 旧行为不应附 proxy 子对象")
    }

    @Test
    fun `配置稳态304下周期循环仍持续上报真实指标`() {
        // 复现并守护根因修复：report 此前仅由长轮询 200（配置变更）触发，稳态恒 304 → reportApplied 永不执行，
        // 控制面注册表里 TPS/内存/CPU/Proxy 恒为零值。现新增独立的周期性指标上报循环，应在配置不变（304）时
        // 仍按心跳周期把注入的真实指标持续发进 report。修复前本用例必失败（reportCalls 恒 0）。
        backend.pollStatus = 304
        // 注册回包心跳周期改 1s，缩短周期上报间隔，便于在测试窗口内断言「多次上报」。
        val codec = FastHeartbeatCapturingCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        val fixed = RuntimeMetrics(
            playerCount = 5,
            tps = 20.0,
            memUsed = 111L,
            memMax = 222L,
            cpuLoad = 0.3,
        )
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            metricsProvider = { fixed },
        )

        lifecycle.bootstrapWithSnapshotThenConnect()
        // 首跳即发 + 每 1s 续杯；3.5s 内应 ≥2 次 report，且全部由周期循环驱动（长轮询恒 304 绝不触发 report）。
        waitUntil(3500) { backend.reportCalls.get() >= 2 }

        assertTrue(
            backend.reportCalls.get() >= 2,
            "稳态 304 下周期循环应按心跳周期持续上报指标（实际 ${backend.reportCalls.get()} 次）",
        )
        val body = codec.lastReport.get() ?: error("稳态 304 下应有 report 报文（由周期循环上报）")
        assertEquals(5, body["playerCount"], "周期上报应携带注入的真实在线人数")
        assertEquals(20.0, body["tps"], "周期上报应携带注入的真实 TPS")
        assertEquals(111L, body["memUsed"])
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

/**
 * 测试用 codec：解析复用 [CannedJsonCodec]，但把注册回包的心跳周期改为 1s——
 * 使 [AgentLifecycle] 的周期性指标上报循环在短测试窗口内多次续杯；同时捕获最近一次 report 报文体供断言。
 */
private class FastHeartbeatCapturingCodec : JsonCodec {

    private val canned = CannedJsonCodec()

    /** 最近一次 report 报文体（Map）；null 表示尚未发生 report。 */
    val lastReport = AtomicReference<Map<String, Any?>?>(null)

    @Suppress("UNCHECKED_CAST")
    override fun encode(value: Any?): String {
        if (value is Map<*, *> && value.containsKey("appliedMd5")) {
            lastReport.set(value as Map<String, Any?>)
        }
        return canned.encode(value)
    }

    @Suppress("UNCHECKED_CAST")
    override fun decode(json: String): Any? {
        val base = canned.decode(json)
        // 仅把注册回包的心跳周期改 1s（其余字段照旧），其它端点解析不动。
        if (json == FakeBeaconBackend.BODY_REGISTER && base is Map<*, *>) {
            return (base as Map<String, Any?>).toMutableMap().apply { put("heartbeatIntervalSec", 1) }
        }
        return base
    }
}

package top.wcpe.beacon.agent.core.sampling

import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

/**
 * 采样样本工厂 [MetricSampleFactory] 单测（FR-144 §4.1）：单位归一与角色字段裁剪。
 */
class MetricSampleFactoryTest {
    @Test
    fun `backend 归一内存CPU并取身份容量`() {
        val runtime =
            RuntimeMetrics(
                playerCount = 12,
                tps = 19.5,
                memUsed = 256L * 1024 * 1024,
                memMax = 1024L * 1024 * 1024,
                cpuLoad = 0.5,
            )
        val s = MetricSampleFactory.backend(tsMs = 1000L, maxOnline = 200, runtime = runtime, reportRttMs = 7)
        assertEquals(1000L, s.tsMs)
        assertEquals(50.0, s.cpuPct, "CPU [0,1]→%")
        assertEquals(256.0, s.memUsedMb)
        assertEquals(1024, s.memMaxMb)
        assertEquals(7, s.reportRttMs)
        assertEquals(MetricKind.BACKEND, s.kind)
        val bp = assertIs<BackendSample>(s.payload)
        assertEquals(19.5, bp.tps)
        assertEquals(12, bp.onlineCount)
        assertEquals(200, bp.maxOnline, "容量取自身份 capacity")
    }

    @Test
    fun `CPU 不可用沿用 -1 哨兵`() {
        // RuntimeMetrics.ZERO 的 cpuLoad = -1.0。
        val s = MetricSampleFactory.backend(tsMs = 1L, maxOnline = 1, runtime = RuntimeMetrics.ZERO, reportRttMs = -1)
        assertEquals(MetricSample.CPU_UNAVAILABLE, s.cpuPct, "CPU 不可用不折算，沿用 -1")
    }

    @Test
    fun `memMax 未设上界封顶到 Int 上限不溢出`() {
        val runtime = RuntimeMetrics(playerCount = 0, tps = 0.0, memUsed = 0L, memMax = Long.MAX_VALUE, cpuLoad = 0.1)
        val s = MetricSampleFactory.backend(tsMs = 1L, maxOnline = 1, runtime = runtime, reportRttMs = -1)
        assertEquals(Int.MAX_VALUE, s.memMaxMb, "未设堆上界的巨值应封顶到 Int.MAX 而非溢出为负")
    }

    @Test
    fun `proxy 取连接与后端可达tps在线不适用`() {
        val runtime = RuntimeMetrics(playerCount = 99, tps = 0.0, memUsed = 0L, memMax = 0L, cpuLoad = 0.3)
        val proxy =
            ProxyMetrics(
                onlineConnections = 128,
                threadCount = 40,
                uptimeMs = 1000L,
                backendUp = 3,
                backendTotal = 4,
                backendAvgLatencyMs = 12.5,
            )
        val s = MetricSampleFactory.proxy(tsMs = 1L, runtime = runtime, proxy = proxy, reportRttMs = 5)
        assertEquals(MetricKind.PROXY, s.kind)
        val pp = assertIs<ProxySample>(s.payload)
        assertEquals(128, pp.connCount)
        assertEquals(3, pp.backendUp)
        assertEquals(4, pp.backendTotal)
        assertEquals(12.5, pp.backendAvgRttMs)
    }

    @Test
    fun `proxy 采集缺失回退空值样本`() {
        val s = MetricSampleFactory.proxy(tsMs = 1L, runtime = RuntimeMetrics.ZERO, proxy = null, reportRttMs = -1)
        val pp = assertIs<ProxySample>(s.payload)
        assertEquals(0, pp.connCount)
        assertEquals(MetricSample.RTT_UNAVAILABLE, pp.backendAvgRttMs, "proxy 采集缺失后端 RTT 回退 -1")
    }
}

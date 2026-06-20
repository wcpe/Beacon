package top.wcpe.beacon.agent.core.metrics

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * BC 专属指标载体 [ProxyMetrics] 与 JVM 线程 / 运行时长采集单测（FR-34）。
 *
 * 覆盖：字段透传、延迟不可用哨兵、线程数 / 运行时长 MXBean 读取的合理性。
 */
class ProxyMetricsTest {

    @Test
    fun `构造代理指标按入参透传`() {
        val m = ProxyMetrics(
            onlineConnections = 128,
            threadCount = 64,
            uptimeMs = 3_600_000L,
            backendUp = 3,
            backendTotal = 4,
            backendAvgLatencyMs = 12.5,
        )
        assertEquals(128, m.onlineConnections)
        assertEquals(64, m.threadCount)
        assertEquals(3_600_000L, m.uptimeMs)
        assertEquals(3, m.backendUp)
        assertEquals(4, m.backendTotal)
        assertEquals(12.5, m.backendAvgLatencyMs)
    }

    @Test
    fun `延迟不可用哨兵为负一`() {
        // 无任何可达后端时上报 -1.0，与「真实 0ms」区分。
        assertEquals(-1.0, ProxyMetrics.LATENCY_UNAVAILABLE, "延迟不可用哨兵应为 -1.0")
        val m = ProxyMetrics(0, 8, 1L, 0, 2, ProxyMetrics.LATENCY_UNAVAILABLE)
        assertTrue(m.backendAvgLatencyMs < 0, "无可达后端时平均延迟应为不可用哨兵")
    }

    @Test
    fun `JVM 线程数为正`() {
        // 当前进程至少有本测试线程，线程数必为正。
        assertTrue(JvmRuntimeMetrics.threadCount() > 0, "活动线程数应为正")
    }

    @Test
    fun `JVM 运行时长非负`() {
        // 运行毫秒数自 JVM 启动累计，必非负。
        assertTrue(JvmRuntimeMetrics.uptimeMs() >= 0L, "运行时长应非负")
    }
}

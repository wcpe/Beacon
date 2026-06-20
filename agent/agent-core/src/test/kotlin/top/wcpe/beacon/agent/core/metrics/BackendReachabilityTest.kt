package top.wcpe.beacon.agent.core.metrics

import top.wcpe.beacon.agent.core.metrics.BackendReachability.PingProbe
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * 后端可达性聚合 [BackendReachability] 纯逻辑单测（FR-34）。
 *
 * 覆盖：正常（部分可达求均值）、全可达、全不可达（延迟不可用）、空输入边界。
 */
class BackendReachabilityTest {

    @Test
    fun `部分可达时 up 计数且对可达样本求平均延迟`() {
        val r = BackendReachability.summarize(
            listOf(
                PingProbe(reachable = true, latencyMs = 10),
                PingProbe(reachable = true, latencyMs = 30),
                PingProbe(reachable = false, latencyMs = 0),
            ),
        )
        assertEquals(2, r.up, "可达后端数应为 2")
        assertEquals(3, r.total, "后端总数应为 3")
        // 平均延迟只对可达样本（10、30）求 → 20.0；不可达不进分母。
        assertEquals(20.0, r.avgLatencyMs, "平均延迟应为 20.0")
    }

    @Test
    fun `全部可达`() {
        val r = BackendReachability.summarize(
            listOf(
                PingProbe(reachable = true, latencyMs = 5),
                PingProbe(reachable = true, latencyMs = 15),
            ),
        )
        assertEquals(2, r.up)
        assertEquals(2, r.total)
        assertEquals(10.0, r.avgLatencyMs)
    }

    @Test
    fun `全部不可达时延迟为不可用哨兵`() {
        val r = BackendReachability.summarize(
            listOf(
                PingProbe(reachable = false, latencyMs = 0),
                PingProbe(reachable = false, latencyMs = 0),
            ),
        )
        assertEquals(0, r.up)
        assertEquals(2, r.total)
        assertEquals(ProxyMetrics.LATENCY_UNAVAILABLE, r.avgLatencyMs, "无可达后端时平均延迟应为不可用哨兵 -1.0")
    }

    @Test
    fun `空输入（无配置后端）`() {
        val r = BackendReachability.summarize(emptyList())
        assertEquals(0, r.up)
        assertEquals(0, r.total)
        assertEquals(ProxyMetrics.LATENCY_UNAVAILABLE, r.avgLatencyMs, "无后端时平均延迟应为不可用哨兵")
    }

    @Test
    fun `可达样本负延迟被钳为 0`() {
        // 极端时序下回调时间戳可能算出负值，钳为 0 防污染均值。
        val r = BackendReachability.summarize(
            listOf(PingProbe(reachable = true, latencyMs = -5)),
        )
        assertEquals(1, r.up)
        assertEquals(0.0, r.avgLatencyMs, "负延迟应被钳为 0")
    }
}

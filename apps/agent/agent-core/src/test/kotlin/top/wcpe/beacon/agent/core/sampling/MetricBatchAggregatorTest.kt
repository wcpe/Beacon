package top.wcpe.beacon.agent.core.sampling

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

/**
 * 批内 5s 桶聚合 [MetricBatchAggregator] 单测（FR-144 §4.1 / §3.1，穷举聚合口径）。
 *
 * 覆盖：桶起点公式、sample_count、avg/max/min 口径、CPU/RTT 不可用剔除、多桶切分、
 * backend 与 proxy 角色字段、reportRttMs 取桶内末样本。
 */
class MetricBatchAggregatorTest {
    private fun backend(
        tsMs: Long,
        cpuPct: Double = 50.0,
        tps: Double = 20.0,
        online: Int = 5,
        reportRttMs: Int = -1,
    ): MetricSample =
        MetricSample(
            tsMs = tsMs,
            cpuPct = cpuPct,
            memUsedMb = 100.0,
            memMaxMb = 200,
            reportRttMs = reportRttMs,
            payload = BackendSample(tps = tps, onlineCount = online, maxOnline = 100),
        )

    /** 仅 memMax 聚合用例需要变化 memMaxMb，直接构造以免污染通用 helper 参数表。 */
    private fun backendWithMemMax(
        tsMs: Long,
        memMaxMb: Int,
    ): MetricSample =
        MetricSample(
            tsMs = tsMs,
            cpuPct = 50.0,
            memUsedMb = 100.0,
            memMaxMb = memMaxMb,
            reportRttMs = -1,
            payload = BackendSample(tps = 20.0, onlineCount = 5, maxOnline = 100),
        )

    private fun proxy(
        tsMs: Long,
        conn: Int = 10,
        backendUp: Int = 2,
        backendTotal: Int = 3,
        rttMs: Double = 8.0,
    ): MetricSample =
        MetricSample(
            tsMs = tsMs,
            cpuPct = 30.0,
            memUsedMb = 100.0,
            memMaxMb = 200,
            reportRttMs = -1,
            payload = ProxySample(connCount = conn, backendUp = backendUp, backendTotal = backendTotal, backendAvgRttMs = rttMs),
        )

    @Test
    fun `空输入返回空`() {
        assertEquals(emptyList(), MetricBatchAggregator.aggregate(emptyList()))
    }

    @Test
    fun `单桶 backend 聚合 avg-max-min 与桶起点`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(
                    backend(tsMs = 1000L, cpuPct = 40.0, tps = 20.0, online = 4, reportRttMs = 11),
                    backend(tsMs = 2000L, cpuPct = 60.0, tps = 18.0, online = 6, reportRttMs = 12),
                    backend(tsMs = 3000L, cpuPct = 50.0, tps = 10.0, online = 8, reportRttMs = 13),
                ),
            )
        assertEquals(1, batches.size)
        val b = batches.single()
        assertEquals(0L, b.bucketStartMs, "桶起点 = ts − ts%5000")
        assertEquals(3, b.sampleCount)
        assertEquals(50.0, b.load.cpuPctAvg)
        assertEquals(60.0, b.load.cpuPctMax)
        assertEquals(13, b.reportRttMs, "reportRttMs 取桶内末样本")
        val bp = assertIs<BackendBatch>(b.payload)
        assertEquals(16.0, bp.tpsAvg)
        assertEquals(10.0, bp.tpsMin, "tps 取最小值（保守）")
        assertEquals(6, bp.onlineAvg, "在线均值四舍五入")
        assertEquals(8, bp.onlineMax)
        assertEquals(100, bp.maxOnline)
    }

    @Test
    fun `跨 5s 边界切分为多桶`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(backend(tsMs = 1000L), backend(tsMs = 4999L), backend(tsMs = 5000L), backend(tsMs = 7345L)),
            )
        assertEquals(2, batches.size, "0~4999 一桶、5000~9999 一桶")
        assertEquals(0L, batches[0].bucketStartMs)
        assertEquals(2, batches[0].sampleCount)
        assertEquals(5000L, batches[1].bucketStartMs)
        assertEquals(2, batches[1].sampleCount)
    }

    @Test
    fun `CPU 全不可用聚合为 -1`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(backend(tsMs = 1000L, cpuPct = -1.0), backend(tsMs = 2000L, cpuPct = -1.0)),
            )
        val load = batches.single().load
        assertEquals(-1.0, load.cpuPctAvg, "全不可用不被 -1 哨兵拉低，聚合为 -1")
        assertEquals(-1.0, load.cpuPctMax)
    }

    @Test
    fun `CPU 部分可用只对可用样本求均`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(backend(tsMs = 1000L, cpuPct = -1.0), backend(tsMs = 2000L, cpuPct = 40.0), backend(tsMs = 3000L, cpuPct = 60.0)),
            )
        val load = batches.single().load
        assertEquals(50.0, load.cpuPctAvg, "仅对可用样本（40/60）求均")
        assertEquals(60.0, load.cpuPctMax)
    }

    @Test
    fun `memMax 取桶内最大`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(backendWithMemMax(tsMs = 1000L, memMaxMb = 200), backendWithMemMax(tsMs = 2000L, memMaxMb = 512)),
            )
        assertEquals(512, batches.single().load.memMaxMb)
    }

    @Test
    fun `proxy 聚合连接与后端可达`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(
                    proxy(tsMs = 1000L, conn = 10, backendUp = 2, backendTotal = 3, rttMs = 6.0),
                    proxy(tsMs = 2000L, conn = 20, backendUp = 3, backendTotal = 3, rttMs = 10.0),
                ),
            )
        val pp = assertIs<ProxyBatch>(batches.single().payload)
        assertEquals(15, pp.connAvg)
        assertEquals(20, pp.connMax)
        assertEquals(3, pp.backendUp, "后端可达取桶内最大")
        assertEquals(3, pp.backendTotal)
        assertEquals(8.0, pp.backendRttMsAvg)
    }

    @Test
    fun `proxy RTT 全不可用聚合为 -1`() {
        val batches =
            MetricBatchAggregator.aggregate(
                listOf(proxy(tsMs = 1000L, rttMs = -1.0), proxy(tsMs = 2000L, rttMs = -1.0)),
            )
        val pp = assertIs<ProxyBatch>(batches.single().payload)
        assertEquals(-1.0, pp.backendRttMsAvg, "无可达后端 RTT 聚合为 -1")
    }
}

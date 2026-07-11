package top.wcpe.beacon.agent.core.sampling

import kotlin.math.roundToInt

/**
 * 批内 5s 桶聚合（FR-144 §4.1 / §3.1，无副作用纯函数，便于穷举单测）。
 *
 * 把升序的原始 1s 样本按 `bucketStart = tsMs − tsMs % 5000` 分桶，每桶聚成一行 [MetricBatch]
 * （avg / max / min，`sampleCount` 为桶内实际样本数 1~5）。桶内样本同角色（一台 agent 只采一种角色）。
 *
 * 聚合口径（agent 侧，控制面按行入库、不再二次聚合）：
 * - CPU：仅对**可用**样本（cpuPct ≥ 0）求 avg / max；全不可用为 -1（不被 -1 哨兵拉低）。
 * - 内存：memUsed 取 avg、memMax 取桶内最大。
 * - backend：tps 取 avg / min（保守），在线取 avg / max，容量取最大。
 * - proxy：连接取 avg / max，后端可达 up/total 取桶内最大，RTT 仅对可用样本求均（全不可用 -1）。
 * - reportRttMs：取桶内**最后一条**样本携带值（最新一次上报 RTT）。
 */
object MetricBatchAggregator {
    /**
     * 按 [bucketMs] 桶（默认 5s）聚合升序样本，返回按桶起点升序的聚合行。空输入返回空列表。
     *
     * [bucketMs] 必须与产出这些样本的 [MetricSampleBuffer] 桶宽一致——否则会把缓冲已按其桶宽闭合的多个桶
     * 错误合并成更粗的桶。生产两者同为 5s；测试加速时须同步覆盖。
     */
    fun aggregate(
        samples: List<MetricSample>,
        bucketMs: Long = MetricSampleBuffer.DEFAULT_BUCKET_MS,
    ): List<MetricBatch> {
        if (samples.isEmpty()) return emptyList()
        // LinkedHashMap 保留首次出现顺序（样本升序 → 桶起点升序）。
        val buckets = LinkedHashMap<Long, MutableList<MetricSample>>()
        for (s in samples) {
            val start = s.tsMs - Math.floorMod(s.tsMs, bucketMs)
            buckets.getOrPut(start) { ArrayList() }.add(s)
        }
        return buckets.map { (start, group) -> aggregateBucket(start, group) }
    }

    private fun aggregateBucket(
        bucketStart: Long,
        group: List<MetricSample>,
    ): MetricBatch =
        MetricBatch(
            bucketStartMs = bucketStart,
            sampleCount = group.size,
            load = aggregateLoad(group),
            reportRttMs = group.last().reportRttMs,
            payload = aggregatePayload(group),
        )

    private fun aggregateLoad(group: List<MetricSample>): LoadAgg {
        val cpuAvailable = group.map { it.cpuPct }.filter { it >= 0.0 }
        return LoadAgg(
            cpuPctAvg = if (cpuAvailable.isEmpty()) MetricSample.CPU_UNAVAILABLE else cpuAvailable.average(),
            cpuPctMax = cpuAvailable.maxOrNull() ?: MetricSample.CPU_UNAVAILABLE,
            memUsedMbAvg = group.map { it.memUsedMb }.average(),
            memMaxMb = group.maxOf { it.memMaxMb },
        )
    }

    private fun aggregatePayload(group: List<MetricSample>): BatchPayload =
        if (group.first().payload is ProxySample) {
            aggregateProxy(group.map { it.payload as ProxySample })
        } else {
            aggregateBackend(group.map { it.payload as BackendSample })
        }

    private fun aggregateBackend(payloads: List<BackendSample>): BackendBatch =
        BackendBatch(
            tpsAvg = payloads.map { it.tps }.average(),
            tpsMin = payloads.minOf { it.tps },
            onlineAvg = payloads.map { it.onlineCount }.average().roundToInt(),
            onlineMax = payloads.maxOf { it.onlineCount },
            maxOnline = payloads.maxOf { it.maxOnline },
        )

    private fun aggregateProxy(payloads: List<ProxySample>): ProxyBatch {
        val rttAvailable = payloads.map { it.backendAvgRttMs }.filter { it >= 0.0 }
        return ProxyBatch(
            connAvg = payloads.map { it.connCount }.average().roundToInt(),
            connMax = payloads.maxOf { it.connCount },
            backendUp = payloads.maxOf { it.backendUp },
            backendTotal = payloads.maxOf { it.backendTotal },
            backendRttMsAvg = if (rttAvailable.isEmpty()) MetricSample.RTT_UNAVAILABLE else rttAvailable.average(),
        )
    }
}

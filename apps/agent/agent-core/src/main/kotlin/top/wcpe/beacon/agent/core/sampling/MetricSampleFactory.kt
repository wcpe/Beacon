package top.wcpe.beacon.agent.core.sampling

import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics

/**
 * 由既有采集载体（[RuntimeMetrics] / [ProxyMetrics]）构造一条 [MetricSample]（FR-144，无副作用纯函数）。
 *
 * 复用既有 FR-32/FR-34 采集件的字段，仅做单位归一（字节→MB、CPU [0,1]→%）与角色字段裁剪：
 * - [backend]：tps / 在线取自 runtime，容量取自身份 capacity。
 * - [proxy]：连接 / 后端可达取自 proxy 段（缺失回退空值）。
 *
 * CPU 不可用（runtime 上报 -1）沿用 -1 哨兵、不折算。
 */
object MetricSampleFactory {
    private const val BYTES_PER_MB = 1024.0 * 1024.0
    private const val PERCENT = 100.0

    /**
     * 构造一条 backend 采样样本。
     *
     * @param tsMs        采样时刻（agent 时钟）
     * @param maxOnline   容量上限（身份 capacity）
     * @param runtime     内存 / CPU / 在线 / TPS 采集
     * @param reportRttMs 上一批上报 HTTP RTT，未知为 -1
     */
    fun backend(
        tsMs: Long,
        maxOnline: Int,
        runtime: RuntimeMetrics,
        reportRttMs: Int,
    ): MetricSample =
        common(tsMs, runtime, reportRttMs) {
            BackendSample(tps = runtime.tps, onlineCount = runtime.playerCount, maxOnline = maxOnline)
        }

    /**
     * 构造一条 proxy 采样样本。
     *
     * @param tsMs        采样时刻（agent 时钟）
     * @param runtime     内存 / CPU 采集（proxy 也提供）
     * @param proxy       proxy 专属采集（连接 / 后端可达）；缺失回退空值样本
     * @param reportRttMs 上一批上报 HTTP RTT，未知为 -1
     */
    fun proxy(
        tsMs: Long,
        runtime: RuntimeMetrics,
        proxy: ProxyMetrics?,
        reportRttMs: Int,
    ): MetricSample = common(tsMs, runtime, reportRttMs) { proxySample(proxy) }

    /** 组装公共字段（时刻 / CPU / 内存 / 上报 RTT）+ 角色载荷，避免两处重复。 */
    private fun common(
        tsMs: Long,
        runtime: RuntimeMetrics,
        reportRttMs: Int,
        payload: () -> SamplePayload,
    ): MetricSample =
        MetricSample(
            tsMs = tsMs,
            cpuPct = toCpuPercent(runtime.cpuLoad),
            memUsedMb = runtime.memUsed / BYTES_PER_MB,
            memMaxMb = toMb(runtime.memMax),
            reportRttMs = reportRttMs,
            payload = payload(),
        )

    /** CPU 负载 [0,1] → 使用率 %；不可用哨兵（<0）原样沿用 -1。 */
    private fun toCpuPercent(cpuLoad: Double): Double = if (cpuLoad < 0.0) MetricSample.CPU_UNAVAILABLE else cpuLoad * PERCENT

    /** 字节 → MB（向下取整为 Int）；未设堆上界时 memMax 可能为 Long.MAX，封顶到 Int.MAX 避免溢出。 */
    private fun toMb(bytes: Long): Int = (bytes / BYTES_PER_MB).toLong().coerceAtMost(Int.MAX_VALUE.toLong()).toInt()

    /** proxy 采集缺失（极端时序）时回退空值样本，绝不让采集失败中断采样。 */
    private fun proxySample(proxy: ProxyMetrics?): ProxySample =
        if (proxy == null) {
            ProxySample(connCount = 0, backendUp = 0, backendTotal = 0, backendAvgRttMs = MetricSample.RTT_UNAVAILABLE)
        } else {
            ProxySample(
                connCount = proxy.onlineConnections,
                backendUp = proxy.backendUp,
                backendTotal = proxy.backendTotal,
                backendAvgRttMs = proxy.backendAvgLatencyMs,
            )
        }
}

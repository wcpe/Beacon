package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.MetricsReportOutcome
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.sampling.MetricBatchAggregator
import top.wcpe.beacon.agent.core.sampling.MetricKind
import top.wcpe.beacon.agent.core.sampling.MetricSampleBuffer
import top.wcpe.beacon.agent.core.sampling.MetricSampleFactory
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * v2 指标采样 + 批上报协调器（FR-144）：从 [AgentLifecycle] 拆出，避免其膨胀成上帝类（AGENTS §10.1）。
 *
 * 两条独立「代」循环，各自续杯（[start] 幂等，重注册不重启；[stop] 令其退出，之后重新 [start] 递增代使旧 tick 失效）：
 * - **1s 采样循环**：每秒读一帧指标（廉价 MXBean + 主线程原子埋点值）经 [MetricSampleFactory] 建样本入环形缓冲，**绝不上主线程**。
 * - **5s 批上报循环**：取缓冲已闭合桶经 [MetricBatchAggregator] 聚成 5s 批上报（[BeaconApiClient.reportMetricsBatch]），
 *   202 才 ack 移除；忙 / 未确认 / 拒绝 / 失败保留缓冲下一 tick 重试（固定节奏、缓冲即背压不退避）。断连补报即此机制的自然结果。
 *
 * 缓冲跨重连保留以支撑断连补报；采集失败已在注入的 provider 内回退（fail-static 不变）。
 */
class MetricsSamplingCoordinator(
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val runtimeProvider: () -> RuntimeMetrics,
    private val proxyProvider: () -> ProxyMetrics?,
) {
    /** 采样角色：bungee → proxy，其余 → backend（与 v2 注册 kind 一致）。 */
    private val kind: MetricKind = if (identity.role == "bungee") MetricKind.PROXY else MetricKind.BACKEND

    /** 是否在运行（start 置 true、stop 置 false，循环据此退出）。 */
    private val active = AtomicBoolean(false)

    /** 采样 / 批上报循环各自的「代」标识：start 递增使旧循环退出。 */
    private val samplingGen = AtomicReference(0)
    private val batchGen = AtomicReference(0)

    /** 采样 / 批上报周期（毫秒）：默认生产值，[configure] 可覆盖（供测试加速）。 */
    @Volatile
    private var sampleIntervalMs: Long = SAMPLE_INTERVAL_MS

    @Volatile
    private var batchReportIntervalMs: Long = BATCH_REPORT_INTERVAL_MS

    /** 5s 桶宽（毫秒）：须与 [buffer] 桶宽一致以对齐聚合。 */
    @Volatile
    private var bucketMs: Long = MetricSampleBuffer.DEFAULT_BUCKET_MS

    /** 采样环形缓冲：跨重连保留支撑断连补报；[configure] 在启动前（无并发）按桶宽重建，故 var。 */
    private var buffer = MetricSampleBuffer(SAMPLE_BUFFER_CAPACITY)

    /** 上一批上报的 HTTP 往返毫秒（未知 -1）；采样时写入样本 reportRttMs。 */
    @Volatile
    private var lastReportRttMs: Int = -1

    /** 批上报是否健康：连续失败只在转入失败 / 恢复各告警一次，避免断连期每 5s 刷屏。 */
    @Volatile
    private var healthy: Boolean = true

    /**
     * 配置周期与桶宽（须在 [start] 前调用，无并发）。默认取生产值，仅测试为加速覆盖。
     */
    fun configure(
        sampleIntervalMs: Long,
        batchReportIntervalMs: Long,
        bucketMs: Long,
    ) {
        this.sampleIntervalMs = sampleIntervalMs
        this.batchReportIntervalMs = batchReportIntervalMs
        this.bucketMs = bucketMs
        this.buffer = MetricSampleBuffer(SAMPLE_BUFFER_CAPACITY, bucketMs)
    }

    /**
     * 启动两条循环（随注册成功调用）。**幂等**：已激活时的重复 start（心跳 404 / 断连恢复等触发的重注册）
     * 直接返回，不补打立即帧、不递增代，现有循环继续跑——否则旧代循环退出前与新代立即帧叠加，
     * 会把 >5 个 1s 样本挤进同一 5s 桶（spec §3.1 规定 1~5）。
     * 仅「未激活 → 激活」转变时启动；stop 后重新 start 仍正常重启（递增代使 stop 前遗留的旧 tick 失效）。
     */
    fun start() {
        if (!active.compareAndSet(false, true)) return
        val sGen = samplingGen.get() + 1
        samplingGen.set(sGen)
        scheduleSampling(sGen, 0)
        val bGen = batchGen.get() + 1
        batchGen.set(bGen)
        scheduleBatch(bGen, batchReportIntervalMs)
    }

    /** 停止两条循环（随 shutdown 调用）。 */
    fun stop() {
        active.set(false)
    }

    // ---- 1s 采样循环 ----

    private fun scheduleSampling(
        gen: Int,
        delayMs: Long,
    ) {
        if (!active.get()) return
        if (delayMs <= 0) adapter.runAsync { samplingTick(gen) } else adapter.runAsyncDelayed(delayMs) { samplingTick(gen) }
    }

    private fun samplingTick(gen: Int) {
        if (!active.get() || gen != samplingGen.get()) return
        // 建一帧样本：内存 / CPU 现采（廉价），在线 / TPS / 连接 / 后端可达取自注入的 provider（采集失败已内部回退）。
        val now = System.currentTimeMillis()
        val sample =
            if (kind == MetricKind.PROXY) {
                MetricSampleFactory.proxy(now, runtimeProvider(), proxyProvider(), lastReportRttMs)
            } else {
                MetricSampleFactory.backend(now, identity.capacity, runtimeProvider(), lastReportRttMs)
            }
        buffer.add(sample)
        scheduleSampling(gen, sampleIntervalMs)
    }

    // ---- 5s 批上报循环 ----

    private fun scheduleBatch(
        gen: Int,
        delayMs: Long,
    ) {
        if (!active.get()) return
        adapter.runAsyncDelayed(delayMs) { batchTick(gen) }
    }

    private fun batchTick(gen: Int) {
        if (!active.get() || gen != batchGen.get()) return
        reportOnce()
        // 固定节奏续杯：无论成功与否，失败保留缓冲下一 tick 重试（断连补报即此机制的自然结果）。
        scheduleBatch(gen, batchReportIntervalMs)
    }

    /**
     * 取一次已闭合桶聚合并上报：202 才 ack 移除已上报样本 + 刷新上报 RTT；其余保留缓冲不动，下一 tick 重试。
     * 断连超 10 分钟的最旧样本被环形覆盖、永久丢弃，恢复后首个成功批携丢弃数上报（丢了多少可见，ADR-0057 精神）。
     */
    private fun reportOnce() {
        val now = System.currentTimeMillis()
        val snapshot = buffer.peekClosed(now)
        // 无已闭合桶（首个桶尚未满 / 缓冲空）：本 tick 无需上报。
        if (snapshot.samples.isEmpty()) return
        val batches = MetricBatchAggregator.aggregate(snapshot.samples, bucketMs)
        val outcome = apiClient.reportMetricsBatch(identity, kind, now, snapshot.droppedSinceLast, batches)
        if (outcome is MetricsReportOutcome.Accepted) {
            buffer.ack(snapshot.lastSeq, snapshot.droppedSinceLast)
            lastReportRttMs = outcome.rttMs
            onSuccess(snapshot.droppedSinceLast)
        } else {
            onFailure(outcome)
        }
    }

    private fun onSuccess(droppedSinceLast: Long) {
        if (!healthy) {
            healthy = true
            adapter.info("指标批上报已恢复")
        }
        if (droppedSinceLast > 0L) {
            adapter.warn("指标批上报：自上次成功以来有 $droppedSinceLast 条采样因缓冲写满被覆盖丢弃（已随本批告知控制面）")
        }
    }

    /** 上报失败：仅在首次转入失败时 WARN 一次（含脱敏原因），避免断连期每 5s 刷屏；样本保留缓冲下一 tick 重试。 */
    private fun onFailure(outcome: MetricsReportOutcome) {
        if (!healthy) return
        val reason =
            when (outcome) {
                is MetricsReportOutcome.Busy -> "控制面写入队列忙"
                is MetricsReportOutcome.Forbidden -> "身份未确认"
                is MetricsReportOutcome.Rejected -> outcome.reason
                is MetricsReportOutcome.Failed -> outcome.reason
                is MetricsReportOutcome.Accepted -> "已受理" // 不会走到（成功分支已处理）
            }
        healthy = false
        adapter.warn("指标批上报失败（$reason），保留缓冲下一 tick 重试；后续同类失败不再刷屏")
    }

    private companion object {
        /** 采样周期（毫秒，FR-144 §4.1）。 */
        const val SAMPLE_INTERVAL_MS = 1_000L

        /** 批上报周期（毫秒，FR-144 §4.2，固定节奏、缓冲即背压不退避）。 */
        const val BATCH_REPORT_INTERVAL_MS = 5_000L

        /** 采样环形缓冲容量（FR-144 §4.1，600 条 = 10 分钟 1s 样本；写满覆盖最旧）。 */
        const val SAMPLE_BUFFER_CAPACITY = 600
    }
}

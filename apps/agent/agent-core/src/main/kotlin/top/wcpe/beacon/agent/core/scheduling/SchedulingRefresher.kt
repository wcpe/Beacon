package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.SchedCandidatesOutcome
import top.wcpe.beacon.agent.core.client.SchedReportLocalOutcome
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * 调度候选刷新循环（FR-148 §4.6 降级路径 step 1/3）：单条自续杯「代」循环，每 10s 拉一次候选快照刷新
 * [SchedulingCache] + 原子落盘 [SchedulingSnapshotStore]，并在刷新成功（控制面在线）时批量补报积压的降级决策。
 *
 * 「代」模式与 [MetricsSamplingCoordinator][top.wcpe.beacon.agent.core.lifecycle.MetricsSamplingCoordinator] 同构：
 * [start] 幂等（重注册不重启），[stop] 令循环退出，之后重新 [start] 递增代使旧 tick 失效。绝不上 MC 主线程。
 * warn-once-on-transition：连续失败仅转入失败 / 恢复各告警一次，避免断连期每 10s 刷屏。
 */
class SchedulingRefresher(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val adapter: PlatformAdapter,
    private val cache: SchedulingCache,
    private val snapshotStore: SchedulingSnapshotStore?,
    private val reportQueue: LocalDecisionReportQueue,
    private val now: () -> Long = { System.currentTimeMillis() },
) : SchedulingRuntime {
    private val active = AtomicBoolean(false)
    private val refreshGen = AtomicReference(0)

    /** 刷新周期（毫秒）：默认生产值，[configure] 可覆盖（供测试加速）。 */
    @Volatile
    private var refreshIntervalMs: Long = REFRESH_INTERVAL_MS

    /** 刷新是否健康：连续失败只在转入失败 / 恢复各告警一次。 */
    @Volatile
    private var healthy: Boolean = true

    /** 配置刷新周期（须在 [start] 前调用，无并发）。仅测试为加速覆盖。 */
    fun configure(refreshIntervalMs: Long) {
        this.refreshIntervalMs = refreshIntervalMs
    }

    override fun start() {
        if (!active.compareAndSet(false, true)) {
            return
        }
        val gen = refreshGen.get() + 1
        refreshGen.set(gen)
        // 立即拉一次（注册成功即刷新一帧候选），随后按周期自续杯。
        scheduleRefresh(gen, 0)
    }

    override fun stop() {
        active.set(false)
    }

    override fun restoreSnapshot() {
        val loaded = snapshotStore?.read() ?: return
        cache.set(loaded, live = false)
        adapter.info("已从落盘快照恢复调度候选：zones=${loaded.zones.size}，快照年龄=${now() - loaded.savedAtMs}ms")
    }

    private fun scheduleRefresh(
        gen: Int,
        delayMs: Long,
    ) {
        if (!active.get()) {
            return
        }
        if (delayMs <= 0) {
            adapter.runAsync { refreshTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { refreshTick(gen) }
        }
    }

    private fun refreshTick(gen: Int) {
        if (!active.get() || gen != refreshGen.get()) {
            return
        }
        refreshOnce()
        scheduleRefresh(gen, refreshIntervalMs)
    }

    private fun refreshOnce() {
        when (val outcome = apiClient.scheduleCandidates(identity)) {
            is SchedCandidatesOutcome.Success -> {
                val snap = outcome.candidates.toSnapshot(now())
                cache.set(snap, live = true)
                persist(snap)
                if (!healthy) {
                    healthy = true
                    adapter.info("调度候选刷新已恢复")
                }
                drainReports()
            }

            is SchedCandidatesOutcome.Failed -> {
                cache.markStale()
                if (healthy) {
                    healthy = false
                    adapter.warn("拉取调度候选失败（${outcome.reason}），按本地快照 fail-static 降级；后续同类失败不再刷屏")
                }
            }
        }
    }

    /** 原子落盘候选快照；失败仅 WARN、保留内存快照（fail-static，绝不抛到调度器）。 */
    private fun persist(snapshot: CandidateSnapshot) {
        val store = snapshotStore ?: return
        try {
            store.write(snapshot)
        } catch (t: Throwable) {
            adapter.warn("落盘候选快照失败（保留内存快照）：${t.message}")
        }
    }

    /**
     * 控制面在线时批量补报积压的降级决策（report-local，≤100 条/批）。某批非 202 受理即把该批及其后
     * 未报记录退回队列，下次刷新成功再补（best-effort，§8 待定 7）。
     */
    private fun drainReports() {
        val pending = reportQueue.drain()
        if (pending.isEmpty()) {
            return
        }
        val batches = pending.chunked(REPORT_BATCH_MAX)
        for ((index, batch) in batches.withIndex()) {
            if (apiClient.reportLocalDecisions(identity, batch) !is SchedReportLocalOutcome.Accepted) {
                batches.drop(index).flatten().forEach { reportQueue.offer(it) }
                return
            }
        }
    }

    private companion object {
        /** 候选刷新周期（毫秒，FR-148 §4.6，10s）。 */
        const val REFRESH_INTERVAL_MS = 10_000L

        /** 单批补报上限（FR-148 §5.1，report-local ≤100 条/批）。 */
        const val REPORT_BATCH_MAX = 100
    }
}

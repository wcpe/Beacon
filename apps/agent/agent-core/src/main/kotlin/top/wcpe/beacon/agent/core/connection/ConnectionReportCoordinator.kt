package top.wcpe.beacon.agent.core.connection

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.ConnectionsReportOutcome
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * 连接明细批上报协调器（FR-145 §4.1）：单条自续杯「代」循环，每 [REPORT_INTERVAL_MS] 或缓冲满 200 条（[flushNow]）
 * 批量 POST 一次 [BeaconApiClient.reportConnectionsBatch]，202 才 ack 移除已上报事件；忙/未确认/失败保留缓冲下一轮重试。
 *
 * 「代」模式与 [MetricsSamplingCoordinator][top.wcpe.beacon.agent.core.lifecycle.MetricsSamplingCoordinator] /
 * [SchedulingRefresher][top.wcpe.beacon.agent.core.scheduling.SchedulingRefresher] 同构：[start] 幂等（重注册不重启）、
 * [stop] 令循环退出。全程 TabooLib async，绝不上 BC 主线程。单飞 [reporting] 保证 interval 与 flush 不叠加重复上报。
 * fail-static：控制面不可用时缓冲照常累积、玩家进出服不受影响，恢复后补报（含丢弃计数，ADR-0057 可见）。
 *
 * @param bootId 本次进程启动标识（随批上报，控制面据此补 close proxy 宕机遗留的 open 行）
 */
class ConnectionReportCoordinator(
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val buffer: ConnectionEventBuffer,
    private val bootId: String,
) {
    private val active = AtomicBoolean(false)
    private val reportGen = AtomicReference(0)

    /** 单飞：任意时刻只一条上报在途，避免 interval 与 flush 触发叠加重复上报（控制面幂等去重亦兜底）。 */
    private val reporting = AtomicBoolean(false)

    /** 上报周期（毫秒）：默认生产值，[configure] 可覆盖（供测试加速）。 */
    @Volatile
    private var intervalMs: Long = REPORT_INTERVAL_MS

    /** 上报是否健康：连续失败只在转入失败 / 恢复各告警一次，避免断连期每 5s 刷屏。 */
    @Volatile
    private var healthy: Boolean = true

    /** 配置上报周期（须在 [start] 前调用，无并发）。仅测试为加速覆盖。 */
    fun configure(intervalMs: Long) {
        this.intervalMs = intervalMs
    }

    fun start() {
        if (!active.compareAndSet(false, true)) return
        val gen = reportGen.get() + 1
        reportGen.set(gen)
        // 首轮延一个周期（让缓冲累积），其后按周期自续杯。
        scheduleReport(gen, intervalMs)
    }

    fun stop() {
        active.set(false)
    }

    /** 缓冲达阈值时由采集侧触发一次即时上报（「满 200 条即上报」，单飞去重）。 */
    fun flushNow() {
        if (!active.get()) return
        adapter.runAsync { reportOnce() }
    }

    private fun scheduleReport(
        gen: Int,
        delayMs: Long,
    ) {
        if (!active.get()) return
        adapter.runAsyncDelayed(delayMs) { reportTick(gen) }
    }

    private fun reportTick(gen: Int) {
        if (!active.get() || gen != reportGen.get()) return
        reportOnce()
        scheduleReport(gen, intervalMs)
    }

    /**
     * 取一批（≤500）已缓冲事件上报：202 才 ack 移除已上报事件；忙/未确认/失败保留缓冲下一轮重试。
     * 单飞守卫：并发的 interval 与 flush 只有一条真正上报，另一条直接返回。
     */
    private fun reportOnce() {
        if (!reporting.compareAndSet(false, true)) return
        try {
            val snapshot = buffer.peek()
            if (snapshot.events.isEmpty()) return
            when (val outcome = apiClient.reportConnectionsBatch(identity, bootId, snapshot.droppedSinceLast, snapshot.events)) {
                is ConnectionsReportOutcome.Accepted -> {
                    buffer.ack(snapshot.lastSeq, snapshot.droppedSinceLast)
                    onSuccess(snapshot.droppedSinceLast)
                }

                else -> onFailure(outcome)
            }
        } finally {
            reporting.set(false)
        }
    }

    private fun onSuccess(droppedSinceLast: Long) {
        if (!healthy) {
            healthy = true
            adapter.info("连接明细批上报已恢复")
        }
        if (droppedSinceLast > 0L) {
            adapter.warn("连接明细批上报：自上次成功以来有 $droppedSinceLast 条连接事件因缓冲写满被覆盖丢弃（已随本批告知控制面）")
        }
    }

    /** 上报失败：仅在首次转入失败时 WARN 一次（含脱敏原因），避免断连期每 5s 刷屏；事件保留缓冲下一轮重试。 */
    private fun onFailure(outcome: ConnectionsReportOutcome) {
        if (!healthy) return
        val reason =
            when (outcome) {
                is ConnectionsReportOutcome.Busy -> "控制面写入队列忙"
                is ConnectionsReportOutcome.Forbidden -> "身份未确认"
                is ConnectionsReportOutcome.Failed -> outcome.reason
                is ConnectionsReportOutcome.Accepted -> "已受理" // 不会走到（成功分支已处理）
            }
        healthy = false
        adapter.warn("连接明细批上报失败（$reason），保留缓冲下一轮重试；后续同类失败不再刷屏")
    }

    companion object {
        /** 上报周期（毫秒，FR-145 §4.1「每 5s 或满 200 条」）。 */
        const val REPORT_INTERVAL_MS: Long = 5_000L
    }
}

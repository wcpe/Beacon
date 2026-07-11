package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.SelfHealth

/**
 * 自身健康视图的线程安全持有者（FR-148）：随每次指标上报 202 响应刷新（约 5s 新鲜度）。
 *
 * 指标上报循环（[MetricsSamplingCoordinator][top.wcpe.beacon.agent.core.lifecycle.MetricsSamplingCoordinator]）
 * 经 sink 写入，[SchedulingView] 的 selfHealth() 读出。仅在收到<b>非空</b> self 时更新——控制面偶发无视图（self=null）
 * 时保留上一次已知值，避免 selfHealth 抖动回 null（从未上报成功时才为 null）。
 *
 * @param now 当前时间提供者（毫秒），便于测试
 */
class SelfHealthHolder(
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    /** 最近一次已知的自身健康（带接收时刻）；null 表示从未收到过非空 self。 */
    @Volatile
    private var current: TimedSelfHealth? = null

    /** 写入最新自身健康；[self] 为 null 时保留上一次已知值（不清空）。 */
    fun set(self: SelfHealth?) {
        if (self != null) {
            current = TimedSelfHealth(self, now())
        }
    }

    /** 读最近一次自身健康（含接收时刻）；从未收到返回 null。 */
    fun get(): TimedSelfHealth? = current
}

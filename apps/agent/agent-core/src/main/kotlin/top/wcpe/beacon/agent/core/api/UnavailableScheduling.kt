package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.api.ScheduleState
import java.util.UUID
import java.util.concurrent.CompletableFuture

/**
 * 调度门面的未装配占位实现（FR-148）：当装配未注入真实 [SchedulingView] 时的兜底默认。
 *
 * 语义即极端 fail-static：无任何候选缓存 → acquireCandidate 立即以「**结论不可用**」正常完成
 * （不抛、不阻塞）；candidatesInZone / healthOf / selfHealth 返回空，dataSource 标本地快照且非新鲜。
 *
 * <h3>「绝不外抛」的范围（FR-244 起写清）</h3>
 *
 * 门面从未就绪时依然成立的是这几条：
 * - [acquireCandidate] 的两条重载都以 normal completed future 收尾（含带作用域的那条，
 *   结论是 [ScheduleState.UNAVAILABLE]——"看不到"，可重试）；
 * - [healthOf] / [selfHealth] / [dataSource] 返回空值或占位状态，不外抛。
 *
 * **不**适用于 [candidatesInZone] 的**按作用域重载**：门面从未就绪 = 判据看不到，
 * 此时返回空表就是把"看不到"冒充"没有"，会让调用方永久放弃一次派房。
 * 故该重载在作用域非空时**如实抛** [IllegalStateException]，由调用方按"当前状态、可重试"处置
 * （与 [ScheduleState.UNAVAILABLE] 同口径）；空作用域（或 null）不涉及判据，照 1 参重载返回空表。
 *
 * <p><b>FR-244 起口径更正</b>：本占位此前把结论报成 {@code failReason = "no_candidate"}（"确实没有候选"，
 * 一条**稳定事实**）。这是错的——门面从未就绪时**看不到任何东西**，调用方据此永久放弃一次本来
 * 只是环境未就绪的派房。现改为 {@link ScheduleState#UNAVAILABLE}（当前状态、可重试），
 * 与 {@code failReason = "unavailable"} 一起自证"这是看不到，不是没有"。</p>
 */
object UnavailableScheduling : BeaconScheduling {
    override fun acquireCandidate(zone: String): CompletableFuture<ScheduleResult> = acquireCandidate(zone, null)

    override fun acquireCandidate(
        zone: String,
        purpose: String?,
    ): CompletableFuture<ScheduleResult> =
        CompletableFuture.completedFuture(
            ScheduleResult(
                null,
                UUID.randomUUID().toString(),
                DecisionSource.LOCAL_FALLBACK,
                "unavailable",
                ScheduleState.UNAVAILABLE,
            ),
        )

    /**
     * 带作用域的重载同样以「结论不可用」正常完成：本占位没有任何候选缓存，
     * 作用域收窄与否都改变不了"看不到"这个事实，故不因作用域非空而改抛异常——
     * 它是 acquireCandidate 路径（fail-static 正常完成），不是"判据收窄"路径。
     */
    override fun acquireCandidate(
        zone: String,
        purpose: String?,
        scope: AdmissionScope?,
    ): CompletableFuture<ScheduleResult> = acquireCandidate(zone, purpose)

    override fun candidatesInZone(zone: String): List<CandidateView> = emptyList()

    /**
     * 按作用域收窄（FR-244）：空作用域（null 或 [AdmissionScope.isEmpty]）时与 1 参重载等价，返回空表；
     * 作用域非空时**判不了**（门面从未就绪 = 本帧判据看不到），故如实抛 [IllegalStateException]——
     * 返回空表就是把"看不到"读成"没有"，正是本 FR 要禁的静默误判。
     *
     * @throws IllegalStateException 作用域非空且门面从未就绪（判据不可用，属可重试的当前状态）
     */
    override fun candidatesInZone(
        zone: String,
        scope: AdmissionScope?,
    ): List<CandidateView> {
        if (scope == null || scope.isEmpty) {
            return candidatesInZone(zone)
        }
        throw IllegalStateException(
            "调度门面尚未就绪（没有候选缓存）：本次按作用域收窄判不了。" +
                "这是当前状态（可重试），不是「没有满足条件的候选」这一稳定结论",
        )
    }

    override fun healthOf(serverId: String): HealthView? = null

    override fun selfHealth(): HealthView? = null

    override fun dataSource(): DataSourceState = DataSourceState(DataSource.LOCAL_SNAPSHOT, false, Long.MAX_VALUE)
}

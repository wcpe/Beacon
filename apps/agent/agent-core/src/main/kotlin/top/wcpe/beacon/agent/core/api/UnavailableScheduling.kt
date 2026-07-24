package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import java.util.UUID
import java.util.concurrent.CompletableFuture

/**
 * 调度门面的未装配占位实现（FR-148）：当装配未注入真实 [SchedulingView] 时的兜底默认。
 *
 * 语义即极端 fail-static：无任何候选缓存 → acquireCandidate 立即以「无候选」正常完成（不抛、不阻塞），
 * candidatesInZone / healthOf / selfHealth 返回空，dataSource 标本地快照且非新鲜。保证契约「绝不外抛」在
 * 门面从未就绪时依然成立。
 */
object UnavailableScheduling : BeaconScheduling {
    override fun acquireCandidate(zone: String): CompletableFuture<ScheduleResult> = acquireCandidate(zone, null)

    override fun acquireCandidate(
        zone: String,
        purpose: String?,
    ): CompletableFuture<ScheduleResult> =
        CompletableFuture.completedFuture(
            ScheduleResult(null, UUID.randomUUID().toString(), DecisionSource.LOCAL_FALLBACK, "no_candidate"),
        )

    override fun candidatesInZone(zone: String): List<CandidateView> = emptyList()

    override fun healthOf(serverId: String): HealthView? = null

    override fun selfHealth(): HealthView? = null

    override fun dataSource(): DataSourceState = DataSourceState(DataSource.LOCAL_SNAPSHOT, false, Long.MAX_VALUE)
}

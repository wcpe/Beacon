package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.LocalDecisionReport
import top.wcpe.beacon.agent.core.client.SchedDecideOutcome
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.CompletableFuture

/**
 * BeaconScheduling 门面的 core 实现（FR-148）：对业务插件只读暴露调度候选与健康事实。
 *
 * - acquireCandidate：异步走 decide；控制面不可用（连接失败 / 超时 / 5xx）时用本地候选快照做 highest_score
 *   降级决策，future 仍正常完成（fail-static，绝不因控制面不可达异常完成、绝不阻塞玩家链路）；降级决策记入补报队列。
 * - candidatesInZone / healthOf / dataSource：读 [SchedulingCache] O(1) 快照（含落盘恢复），可在主线程调用。
 * - selfHealth：读 [SelfHealthHolder]（指标上报响应刷新）。
 *
 * 刷新循环与落盘由 [SchedulingRefresher] 负责（SRP 分离），本类只读缓存 + 做单次决策。
 */
class SchedulingView(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val adapter: PlatformAdapter,
    private val cache: SchedulingCache,
    private val reportQueue: LocalDecisionReportQueue,
    private val selfHealthHolder: SelfHealthHolder,
    private val now: () -> Long = { System.currentTimeMillis() },
) : BeaconScheduling {
    override fun acquireCandidate(zone: String): CompletableFuture<ScheduleResult> = acquireCandidate(zone, null)

    override fun acquireCandidate(
        zone: String,
        purpose: String?,
    ): CompletableFuture<ScheduleResult> {
        val future = CompletableFuture<ScheduleResult>()
        // 走独立异步线程（绝不阻塞调用线程与 MC 主线程）；任何异常都以本地兜底结果完成 future，绝不异常完成（fail-static 契约）。
        adapter.runAsync {
            try {
                future.complete(decide(zone, purpose))
            } catch (t: Throwable) {
                adapter.warn("调度决策异常，转本地兜底：${t.message}")
                future.complete(localFallback(zone, purpose))
            }
        }
        return future
    }

    override fun candidatesInZone(zone: String): List<CandidateView> = cache.entriesInZone(zone).map { it.toCandidateView(zone) }

    override fun healthOf(serverId: String): HealthView? {
        val entry = cache.findAnywhere(serverId) ?: return null
        return entry.toHealthView(cache.current()?.generatedAtMs ?: 0L)
    }

    override fun selfHealth(): HealthView? {
        val timed = selfHealthHolder.get() ?: return null
        return timed.self.toHealthView(identity.serverId, timed.atMs)
    }

    override fun dataSource(): DataSourceState = cache.dataSource()

    /** 走控制面 decide；权威失败（zone 不存在 / 跨域 / 参数）如实回控制面结果，连接级失败才降级本地决策。 */
    private fun decide(
        zone: String,
        purpose: String?,
    ): ScheduleResult =
        when (val outcome = apiClient.scheduleDecide(identity, zone, purpose, null)) {
            is SchedDecideOutcome.Decided -> controlPlaneResult(zone, outcome)
            is SchedDecideOutcome.ZoneNotFound -> failedControlPlane("zone_not_found")
            is SchedDecideOutcome.CrossNamespace -> failedControlPlane("cross_namespace")
            is SchedDecideOutcome.Rejected -> failedControlPlane(outcome.reason)
            is SchedDecideOutcome.Failed -> localFallback(zone, purpose)
        }

    /** 控制面权威失败结果（zone 不存在 / 跨域 / 参数非法）：不降级本地（快照亦无权威依据），如实回失败原因。 */
    private fun failedControlPlane(reason: String): ScheduleResult =
        ScheduleResult(null, newLocalTraceId(), DecisionSource.CONTROL_PLANE, reason)

    /** 控制面成功决策：以服务端 traceId 为准，chosen 视图从本地快照补全 zone/level/online 字段。 */
    private fun controlPlaneResult(
        zone: String,
        decided: SchedDecideOutcome.Decided,
    ): ScheduleResult {
        val chosen =
            decided.chosen?.let { choice ->
                val entry = cache.findEntry(zone, choice.serverId)
                CandidateView(
                    choice.serverId,
                    zone,
                    choice.score,
                    entry?.let { mapLevel(it.level) } ?: HealthLevel.HEALTHY,
                    entry?.onlineCount ?: 0,
                    entry?.maxOnline ?: 0,
                )
            }
        return ScheduleResult(chosen, decided.traceId, DecisionSource.CONTROL_PLANE, decided.failReason)
    }

    /** 本地快照降级决策（fail-static）：目标 zone 内 highest_score（仅 schedulable），记入补报队列。 */
    private fun localFallback(
        zone: String,
        purpose: String?,
    ): ScheduleResult {
        val traceId = newLocalTraceId()
        val candidates = cache.entriesInZone(zone)
        val best = candidates.filter { it.schedulable }.maxByOrNull { it.score }
        val chosen = best?.toCandidateView(zone)
        val failReason = if (best == null) "no_candidate" else null
        reportQueue.offer(
            LocalDecisionReport(
                localTraceId = traceId,
                tsMs = now(),
                zone = zone,
                plugin = null,
                purpose = purpose,
                candidateCount = candidates.size,
                excluded = emptyList(),
                chosenServerId = best?.serverId,
                failReason = failReason,
            ),
        )
        return ScheduleResult(chosen, traceId, DecisionSource.LOCAL_FALLBACK, failReason)
    }
}

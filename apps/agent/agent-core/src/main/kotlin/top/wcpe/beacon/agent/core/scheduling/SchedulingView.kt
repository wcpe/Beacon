package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.api.ScheduleState
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.ExcludedRef
import top.wcpe.beacon.agent.core.client.LocalDecisionReport
import top.wcpe.beacon.agent.core.client.SchedDecideOutcome
import top.wcpe.beacon.agent.core.client.scheduleDecide
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
 *
 * <h2>准入作用域（FR-244）与三态结论</h2>
 *
 * <p>带作用域的重载把「候选节点必须自己声明过哪些键值标签」带到决策里：控制面在**生成候选的那一刻**按它
 * 收窄，被排除的候选连被选中的机会都没有。作用域为空时逐位等于不带作用域的那条重载。</p>
 *
 * <p>结论一律分三态给出（[ScheduleState]）：选了 / **空**（稳定事实）/ **不可用**（当前状态、可重试）。
 * 降级路径上"看不到声明"（快照里根本没有 `labels` 键）与"快照整体缺失"都归**不可用**——
 * 拿它们冒充"没有候选"会让调用方永久放弃一次本来只是环境未就绪的派房。</p>
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
    ): CompletableFuture<ScheduleResult> = acquireCandidate(zone, purpose, AdmissionScope.empty())

    override fun acquireCandidate(
        zone: String,
        purpose: String?,
        scope: AdmissionScope?,
    ): CompletableFuture<ScheduleResult> {
        // null 视为空作用域（与不带作用域的重载等价），不让 null 穿透到下面的判定里。
        val admission = scope ?: AdmissionScope.empty()
        val future = CompletableFuture<ScheduleResult>()
        // 走独立异步线程（绝不阻塞调用线程与 MC 主线程）；任何异常都以本地兜底结果完成 future，绝不异常完成（fail-static 契约）。
        adapter.runAsync {
            try {
                future.complete(decide(zone, purpose, admission))
            } catch (t: Throwable) {
                adapter.warn("调度决策异常，转本地兜底：${t.message}")
                future.complete(localFallback(zone, purpose, admission))
            }
        }
        return future
    }

    override fun candidatesInZone(zone: String): List<CandidateView> = cache.entriesInZone(zone).map { it.toCandidateView(zone) }

    /**
     * 只保留满足准入作用域的候选（FR-244）。
     *
     * <p>判定用的标签是候选节点**自己声明**的那一份（[CandidateView.labels] 同一真源）。
     * 快照里带着 `labels` 键（[CandidateEntry.labelsPresent]）就说明本帧来源携带了该字段，
     * 此时"剩下 0 台"是**稳定事实**；反之本帧来源没有这个字段（旧控制面响应，或自旧格式落盘恢复的快照），
     * 本方法**不返回空表**——拿"看不到"冒充"没有"会让调用方永久放弃一次派房，
     * 故抛 [IllegalStateException] 让调用方按不可用处置。</p>
     *
     * <p>判据的适用范围：它依赖「控制面只下发有可调度候选的区」这一前提——该 zone 内一台候选都没有时
     * `any` 为 false，本方法照常返回空表（判成「没有候选」这一稳定结论），
     * 不去猜「一台都没有」是不是「标签不可见」的另一种表现。</p>
     *
     * @throws IllegalStateException 作用域非空、而候选快照缺 `labels` 字段（判据看不到）
     */
    override fun candidatesInZone(
        zone: String,
        scope: AdmissionScope?,
    ): List<CandidateView> {
        val admission = scope ?: AdmissionScope.empty()
        val entries = cache.entriesInZone(zone)
        if (admission.isEmpty || !entries.any { !it.labelsPresent }) {
            return entries.filter { admission.admits(it.labels) }.map { it.toCandidateView(zone) }
        }
        // 判不了时的报因只说事实、不指认成因：旧控制面响应与自旧格式落盘恢复的快照在下游长得一样，
        // 指认成任一方都会把玩家（或运维）引向错误方向。
        throw IllegalStateException(
            "候选快照里没有节点自声明标签（labels 字段缺失，本帧来源未携带该字段）：本次按作用域收窄判不了。" +
                "这是当前状态（可重试），不是「没有满足条件的候选」这一稳定结论",
        )
    }

    override fun healthOf(serverId: String): HealthView? {
        val entry = cache.findAnywhere(serverId) ?: return null
        return entry.toHealthView(cache.current()?.generatedAtMs ?: 0L)
    }

    override fun selfHealth(): HealthView? {
        val timed = selfHealthHolder.get() ?: return null
        return timed.self.toHealthView(identity.serverId, timed.atMs)
    }

    override fun dataSource(): DataSourceState = cache.dataSource()

    /**
     * 走控制面 decide；权威失败（zone 不存在 / 跨域 / 参数）如实回控制面结果，连接级失败才降级本地决策。
     *
     * <p>作用域非空但控制面判不了（503 `admission_unavailable`）**也**走本地降级：本地快照可能
     * 仍能答（它自己带标签），答不了时降级路径会如实给 {@link ScheduleState.UNAVAILABLE}——
     * 两条路都不把"判不了"印成"没有"。</p>
     */
    private fun decide(
        zone: String,
        purpose: String?,
        scope: AdmissionScope,
    ): ScheduleResult =
        when (val outcome = apiClient.scheduleDecide(identity, zone, purpose, null, scope.alternatives())) {
            is SchedDecideOutcome.Decided -> controlPlaneResult(cache, zone, outcome)
            is SchedDecideOutcome.ZoneNotFound -> controlPlaneFailure("zone_not_found")
            is SchedDecideOutcome.CrossNamespace -> controlPlaneFailure("cross_namespace")
            is SchedDecideOutcome.Rejected -> controlPlaneFailure(outcome.reason)
            is SchedDecideOutcome.AdmissionUnavailable -> {
                adapter.warn("控制面判不了本次准入作用域（${outcome.reason}），转本地快照决策")
                localFallback(zone, purpose, scope)
            }
            is SchedDecideOutcome.Failed -> localFallback(zone, purpose, scope)
        }

    /**
     * 本地快照降级决策（fail-static）：目标 zone 内 highest_score（仅 schedulable 且满足准入作用域），
     * 记入补报队列。
     *
     * <h3>三态在降级路径上怎么分</h3>
     *
     * <ul>
     *   <li>选出来了 → {@link ScheduleState.CHOSEN}（fail-static 照旧）；</li>
     *   <li>快照覆盖该 zone、且（作用域为空或标签可见）→ 真没选出来 →
     *       {@link ScheduleState.NO_CANDIDATE}（`no_candidate` / 作用域非空且全被滤掉时
     *       `no_candidate_in_scope`）；</li>
     *   <li><b>整体没有快照</b>（从未刷新成功也没落盘恢复）→ {@link ScheduleState.UNAVAILABLE}
     *       （`unavailable`）——我们**看不到任何东西**，这不是"确实没有候选"；</li>
     *   <li>作用域非空、而快照里没有 `labels` 字段（本帧来源未携带该字段：旧控制面响应，或自旧格式落盘
     *       恢复的快照）→ {@link ScheduleState.UNAVAILABLE}（`admission_scope_unavailable`）——判据看不到，
     *       既不能放行（可能不满足作用域）也不能报"没有"。</li>
     * </ul>
     */
    private fun localFallback(
        zone: String,
        purpose: String?,
        scope: AdmissionScope,
    ): ScheduleResult {
        val traceId = newLocalTraceId()
        val snapshot = cache.current()
        val candidates = cache.entriesInZone(zone)
        // 判据看不到的两种情形：整体没有快照；或作用域非空而快照缺 labels 字段。
        // 后者依赖「控制面只下发有可调度候选的区」这一前提：该 zone 内一台候选都没有时 any 为 false，
        // 不会被误判成"标签不可见"（那属于本仓之外的下发约定，不是这里能校验的硬保证）。
        val labelsInvisible = !scope.isEmpty && candidates.any { !it.labelsPresent }
        if (snapshot == null || labelsInvisible) {
            val reason = if (snapshot == null) "unavailable" else "admission_scope_unavailable"
            // 只看事实、不指认成因：两种成因（旧控制面响应 / 自旧格式落盘恢复）在降级路径上长得一样，
            // 指认成任一方都会把玩家（或运维）引向错误方向。
            val snapshotState = if (snapshot == null) "不存在" else "缺自声明标签字段"
            adapter.warn(
                "选服降级路径判不了：$reason（候选快照$snapshotState）。" +
                    "这是当前状态（可重试），不按「没有候选」这一稳定结论上报",
            )
            return ScheduleResult(null, traceId, DecisionSource.LOCAL_FALLBACK, reason, ScheduleState.UNAVAILABLE)
        }
        val admitted = candidates.filter { scope.admits(it.labels) }
        val best = admitted.filter { it.schedulable }.maxByOrNull { it.score }
        val chosen = best?.toCandidateView(zone)
        val failReason =
            when {
                best != null -> null
                // 作用域把候选滤掉了（而不是本来就没有可调度候选）：诊断要能分开这两种。
                !scope.isEmpty && candidates.isNotEmpty() && admitted.isEmpty() -> "no_candidate_in_scope"
                else -> "no_candidate"
            }
        reportQueue.offer(
            LocalDecisionReport(
                localTraceId = traceId,
                tsMs = now(),
                zone = zone,
                plugin = null,
                purpose = purpose,
                candidateCount = candidates.size,
                // 被作用域排除的节点如实入补报（原因码与控制面 excluded 列同一套词表）。
                excluded =
                    candidates
                        .filter { !scope.admits(it.labels) }
                        .map { ExcludedRef(it.serverId, "admission_scope_mismatch") },
                chosenServerId = best?.serverId,
                failReason = failReason,
            ),
        )
        return ScheduleResult(
            chosen,
            traceId,
            DecisionSource.LOCAL_FALLBACK,
            failReason,
            if (chosen != null) ScheduleState.CHOSEN else ScheduleState.NO_CANDIDATE,
            // 被作用域排除的台数在降级路径上同样如实回报：它与控制面路径同一口径，
            // 让调用方能判断"这次收窄真的发生过"（不回报就只剩空结果，收窄痕迹只留在补报队列里）。
            candidates.size - admitted.size,
        )
    }
}

/**
 * 控制面权威失败结果（zone 不存在 / 跨域 / 参数非法）：不降级本地（快照亦无权威依据），如实回失败原因。
 *
 * <p>它是**决策成立、结论为否**（稳定事实）——故归 {@link ScheduleState.NO_CANDIDATE}，不是 UNAVAILABLE。</p>
 *
 * <p>放在文件级（而非 [SchedulingView] 成员）：它只做「原因码 → 结果」的纯映射，不依赖视图持有的任何状态；
 * 抽出来也让门面类的函数数维持在 TooManyFunctions 阈值内。</p>
 */
internal fun controlPlaneFailure(reason: String): ScheduleResult =
    ScheduleResult(null, newLocalTraceId(), DecisionSource.CONTROL_PLANE, reason, ScheduleState.NO_CANDIDATE)

/**
 * 控制面成功决策 → [ScheduleResult]：以服务端 traceId 为准，chosen 视图从本地快照补全 zone/level/online 字段。
 *
 * <p>控制面做成了决策：选了 → {@link ScheduleState.CHOSEN}；没选 → {@link ScheduleState.NO_CANDIDATE}
 * （**稳定事实**，failReason 区分"没候选"还是"候选都被作用域滤掉"）。作用域排除台数原样带出（FR-244）。</p>
 *
 * @param snapshotCache 候选快照缓存（只读；用于补全 chosen 的 zone/level/online/labels 展示字段）
 */
internal fun controlPlaneResult(
    snapshotCache: SchedulingCache,
    zone: String,
    decided: SchedDecideOutcome.Decided,
): ScheduleResult {
    val chosen =
        decided.chosen?.let { choice ->
            val entry = snapshotCache.findEntry(zone, choice.serverId)
            CandidateView(
                choice.serverId,
                zone,
                choice.score,
                entry?.let { mapLevel(it.level) } ?: HealthLevel.HEALTHY,
                entry?.onlineCount ?: 0,
                entry?.maxOnline ?: 0,
                // 标签从本地快照补全（与 level / online 同一路）；快照缺该字段时空 map，
                // 但本视图的标签只用于展示——决策侧的作用域收窄是控制面做的，不依赖这里。
                entry?.labels ?: emptyMap(),
            )
        }
    return if (chosen != null) {
        ScheduleResult(
            chosen,
            decided.traceId,
            DecisionSource.CONTROL_PLANE,
            decided.failReason,
            ScheduleState.CHOSEN,
            decided.admissionExcludedCount,
        )
    } else {
        ScheduleResult(
            null,
            decided.traceId,
            DecisionSource.CONTROL_PLANE,
            decided.failReason ?: "no_candidate",
            ScheduleState.NO_CANDIDATE,
            decided.admissionExcludedCount,
        )
    }
}

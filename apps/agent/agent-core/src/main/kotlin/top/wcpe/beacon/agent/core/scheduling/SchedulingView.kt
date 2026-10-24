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
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.ExcludedRef
import top.wcpe.beacon.agent.core.client.LocalDecisionReport
import top.wcpe.beacon.agent.core.client.SCHED_SCOPE_LOBBY
import top.wcpe.beacon.agent.core.client.SCHED_SCOPE_SHARED_POOL
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
 * - acquireCandidateInLobby / acquireCandidateInSharedPool（FR-281）：同一套决策框架，作用域分别换成
 *   namespace 全局大厅池（控制面 `scope=lobby`）与共享游戏池（`scope=shared_pool`）；
 *   降级时读本地快照的**对应池段**，与小区档共用同一个排序 / 三态判定纯函数。
 * - candidatesInZone / candidatesInLobby / candidatesInSharedPool / healthOf / dataSource：读 [SchedulingCache]
 *   O(1) 快照（含落盘恢复），可在主线程调用。
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
 *
 * <h2>三个池 / 三种作用域的差别</h2>
 *
 * <p>按<b>用途</b>分三层，彼此不是同一批机器：</p>
 * <ul>
 *   <li><b>小区</b>（`zone` 档）：普通后端子服，按小区名定位；视图 zone 是小区名。</li>
 *   <li><b>入口大厅池</b>（`lobby` 档）：BC <b>首次落脚</b>的地方，玩家先落到那里再转进游戏；
 *       成员不属于任何小区，视图 zone 为空串。</li>
 *   <li><b>共享游戏池</b>（`shared_pool` 档）：<b>跨区共享的游戏服池</b>，选中即开游戏房；
 *       成员同样不属于任何小区，视图 zone 也为空串。</li>
 * </ul>
 *
 * <p>控制面的归属是**四选一**（小区 / 大厅 / 共享池 / 独立），故两池成员集合互斥、不重叠：
 * 它们各有各的用途，<b>不该互相兜底</b>——拿大厅的机器去开游戏房，与拿共享池的机器去接 BC 首次落脚，
 * 都是把玩家送错地方。三档的差别只有两处，且都收在 [CandidatePool] 里：候选从哪一段快照来，
 * 以及视图上的 zone 取什么值。收窄、排序、三态判定是同一份实现。</p>
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
                future.complete(localFallback(zonePool(cache, zone), purpose, admission))
            }
        }
        return future
    }

    override fun acquireCandidateInLobby(purpose: String?): CompletableFuture<ScheduleResult> = acquireCandidateInLobby(purpose, null)

    /**
     * 在 namespace 全局大厅池里取一台可调度候选（FR-281）。
     *
     * <p>走控制面**既有**的 `scope=lobby` 分支（不新造协议）；连接级失败时用本地快照的**大厅段**降级，
     * 与小区档共用同一套收窄 / 排序 / 三态判定（见 [localFallback]）。</p>
     *
     * <p>fail-static 契约与小区档一致：不因控制面不可达而异常完成，也绝不阻塞玩家链路。</p>
     */
    override fun acquireCandidateInLobby(
        purpose: String?,
        scope: AdmissionScope?,
    ): CompletableFuture<ScheduleResult> {
        val admission = scope ?: AdmissionScope.empty()
        val future = CompletableFuture<ScheduleResult>()
        adapter.runAsync {
            try {
                future.complete(decideInLobby(purpose, admission))
            } catch (t: Throwable) {
                adapter.warn("大厅调度决策异常，转本地兜底：${t.message}")
                future.complete(localFallback(lobbyPool(cache), purpose, admission))
            }
        }
        return future
    }

    override fun acquireCandidateInSharedPool(purpose: String?): CompletableFuture<ScheduleResult> =
        acquireCandidateInSharedPool(purpose, null)

    /**
     * 在 namespace 共享游戏池里取一台可调度候选（FR-281）。
     *
     * <p>走控制面 `scope=shared_pool` 分支且**不带 zone**；连接级失败时用本地快照的**共享池段**降级，
     * 与小区档 / 大厅档共用同一套收窄 / 排序 / 三态判定（见 [localFallback]）。</p>
     *
     * <p>fail-static 契约与另两档一致：不因控制面不可达而异常完成，也绝不阻塞玩家链路。</p>
     */
    override fun acquireCandidateInSharedPool(
        purpose: String?,
        scope: AdmissionScope?,
    ): CompletableFuture<ScheduleResult> {
        val admission = scope ?: AdmissionScope.empty()
        val future = CompletableFuture<ScheduleResult>()
        adapter.runAsync {
            try {
                future.complete(decideInSharedPool(purpose, admission))
            } catch (t: Throwable) {
                adapter.warn("共享游戏池调度决策异常，转本地兜底：${t.message}")
                future.complete(localFallback(sharedPool(cache), purpose, admission))
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
        val entries = cache.entriesInZone(zone)
        return entries.admittedBy(scope).map { it.toCandidateView(zone) }
    }

    override fun candidatesInLobby(): List<CandidateView> = candidatesInLobby(null)

    /**
     * 列出 namespace 全局大厅池当前候选快照，并只保留满足准入作用域的（FR-281）。
     *
     * <p><b>没有大厅模型时抛，绝不返回空表</b>：本帧来源是还没有 `lobby` 段的旧控制面（或从未刷新成功过）时，
     * 我们**看不到**大厅池——返回空表会把它冒充成"池里没有机器"这一稳定事实，
     * 让调用方永久放弃一次本来只是环境未就绪的派房。</p>
     *
     * <p>"大厅模型在、但池里确实是空的"是**稳定事实**，照常返回空表（例如本 namespace 还没有大厅集群，
     * 或集群里当前没有可调度成员）。</p>
     *
     * @throws IllegalStateException 本帧没有大厅模型，或作用域非空而候选缺 `labels` 字段（判据看不到）
     */
    override fun candidatesInLobby(scope: AdmissionScope?): List<CandidateView> {
        val entries =
            cache.poolEntries(PoolSection.LOBBY)
                ?: throw IllegalStateException(
                    "候选快照里没有大厅模型（本帧来源未下发 lobby 段）：本次读不出大厅池候选。" +
                        "这是当前状态（可重试），不是「池里没有机器」这一稳定结论",
                )
        // 大厅成员不属于任何小区，视图 zone 恒为空串（与接口契约「CHOSEN 时 zone() 为空串」一致）。
        return entries.admittedBy(scope).map { it.toCandidateView(LOBBY_VIEW_ZONE) }
    }

    override fun candidatesInSharedPool(): List<CandidateView> = candidatesInSharedPool(null)

    /**
     * 列出 namespace 共享游戏池当前候选快照，并只保留满足准入作用域的（FR-281）。
     *
     * <p><b>没有共享池模型时抛，绝不返回空表</b>：本帧来源是还没有 `shared` 段的旧控制面（或从未刷新成功过）时，
     * 我们**看不到**共享池——返回空表会把它冒充成"池里没有机器"这一稳定事实，
     * 让调用方永久放弃一次本来只是环境未就绪的派房。</p>
     *
     * <p>"共享池模型在、但池里确实是空的"是**稳定事实**，照常返回空表（例如本 namespace 的共享池
     * 当前没有可调度成员）。</p>
     *
     * @throws IllegalStateException 本帧没有共享池模型，或作用域非空而候选缺 `labels` 字段（判据看不到）
     */
    override fun candidatesInSharedPool(scope: AdmissionScope?): List<CandidateView> {
        val entries =
            cache.poolEntries(PoolSection.SHARED)
                ?: throw IllegalStateException(
                    "候选快照里没有共享池模型（本帧来源未下发 shared 段）：本次读不出共享游戏池候选。" +
                        "这是当前状态（可重试），不是「池里没有机器」这一稳定结论",
                )
        // 共享池成员同样不属于任何小区，视图 zone 恒为空串（与接口契约「CHOSEN 时 zone() 为空串」一致）。
        return entries.admittedBy(scope).map { it.toCandidateView(SHARED_VIEW_ZONE) }
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
     * 走控制面 decide（小区档）；权威失败（zone 不存在 / 跨域 / 参数）如实回控制面结果，连接级失败才降级本地决策。
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
            is SchedDecideOutcome.Decided -> controlPlaneResult(zone, { cache.findEntry(zone, it) }, outcome)
            is SchedDecideOutcome.ZoneNotFound -> controlPlaneFailure("zone_not_found")
            is SchedDecideOutcome.CrossNamespace -> controlPlaneFailure("cross_namespace")
            is SchedDecideOutcome.Rejected -> controlPlaneFailure(outcome.reason)
            is SchedDecideOutcome.AdmissionUnavailable -> {
                adapter.warn("控制面判不了本次准入作用域（${outcome.reason}），转本地快照决策")
                localFallback(zonePool(cache, zone), purpose, scope)
            }
            is SchedDecideOutcome.Failed -> localFallback(zonePool(cache, zone), purpose, scope)
        }

    /**
     * 走控制面 decide（大厅档，FR-281）：下发 `scope=lobby` 且**不带** zone。
     *
     * <p>与小区档唯一的口径差别在失败侧：大厅档的"问到了"只有一条路（200），
     * 因此 4xx（`zone_not_found` / `cross_namespace` / `Rejected`）一律归
     * {@link ScheduleState.UNAVAILABLE}——它们是**没问成**（例如对端还不认识大厅作用域、
     * 或把不带 zone 的请求判成参数非法），不是"池里确实没有机器"。把它印成后一种会让调用方
     * 永久放弃一次本来只是环境未就绪的派房。</p>
     *
     * <p>503 `admission_unavailable` 与连接级失败照旧走本地降级（与小区档同一处置）。</p>
     */
    private fun decideInLobby(
        purpose: String?,
        scope: AdmissionScope,
    ): ScheduleResult =
        when (
            val outcome =
                apiClient.scheduleDecide(identity, LOBBY_VIEW_ZONE, purpose, null, scope.alternatives(), SCHED_SCOPE_LOBBY)
        ) {
            is SchedDecideOutcome.Decided -> controlPlaneResult(LOBBY_VIEW_ZONE, { cache.findInPool(PoolSection.LOBBY, it) }, outcome)
            is SchedDecideOutcome.ZoneNotFound -> controlPlaneUnavailable("zone_not_found")
            is SchedDecideOutcome.CrossNamespace -> controlPlaneUnavailable("cross_namespace")
            is SchedDecideOutcome.Rejected -> controlPlaneUnavailable(outcome.reason)
            is SchedDecideOutcome.AdmissionUnavailable -> {
                adapter.warn("控制面判不了本次准入作用域（${outcome.reason}），转本地大厅快照决策")
                localFallback(lobbyPool(cache), purpose, scope)
            }
            is SchedDecideOutcome.Failed -> localFallback(lobbyPool(cache), purpose, scope)
        }

    /**
     * 走控制面 decide（共享游戏池档，FR-281）：下发 `scope=shared_pool` 且**不带** zone。
     *
     * <p>与大厅档逐条同构（两池是并列的兄弟档，不是同一档的两种写法）："问到了"只有一条路（200），
     * 因此 4xx（`zone_not_found` / `cross_namespace` / `Rejected`）一律归
     * {@link ScheduleState.UNAVAILABLE}——它们是**没问成**（例如对端还不认识共享池作用域、
     * 或把不带 zone 的请求判成参数非法），不是"池里确实没有机器"。把它印成后一种会让调用方
     * 永久放弃一次本来只是环境未就绪的派房。</p>
     *
     * <p>503 `admission_unavailable` 与连接级失败照旧走本地降级（与另两档同一处置）。</p>
     */
    private fun decideInSharedPool(
        purpose: String?,
        scope: AdmissionScope,
    ): ScheduleResult =
        when (
            val outcome =
                apiClient.scheduleDecide(
                    identity,
                    SHARED_VIEW_ZONE,
                    purpose,
                    null,
                    scope.alternatives(),
                    SCHED_SCOPE_SHARED_POOL,
                )
        ) {
            is SchedDecideOutcome.Decided -> controlPlaneResult(SHARED_VIEW_ZONE, { cache.findInPool(PoolSection.SHARED, it) }, outcome)
            is SchedDecideOutcome.ZoneNotFound -> controlPlaneUnavailable("zone_not_found")
            is SchedDecideOutcome.CrossNamespace -> controlPlaneUnavailable("cross_namespace")
            is SchedDecideOutcome.Rejected -> controlPlaneUnavailable(outcome.reason)
            is SchedDecideOutcome.AdmissionUnavailable -> {
                adapter.warn("控制面判不了本次准入作用域（${outcome.reason}），转本地共享池快照决策")
                localFallback(sharedPool(cache), purpose, scope)
            }
            is SchedDecideOutcome.Failed -> localFallback(sharedPool(cache), purpose, scope)
        }

    /**
     * 本地快照降级决策（fail-static）：[pool] 内 highest_score（仅 schedulable 且满足准入作用域）。
     *
     * <p>小区档与两个池档**共用本实现**（差别只在 [pool] 上：候选从哪一段快照来、视图 zone 取什么值），
     * 故三条路不会各长出一套排序与三态判定。</p>
     *
     * <h3>三态在降级路径上怎么分</h3>
     *
     * <ul>
     *   <li>选出来了 → {@link ScheduleState.CHOSEN}（fail-static 照旧）；</li>
     *   <li>池覆盖到了、且（作用域为空或标签可见）→ 真没选出来 →
     *       {@link ScheduleState.NO_CANDIDATE}（`no_candidate` / 作用域非空且全被滤掉时
     *       `no_candidate_in_scope`）；</li>
     *   <li><b>判据看不到</b>（小区档：整体没有快照；池档：本帧没有对应池模型）→
     *       {@link ScheduleState.UNAVAILABLE}（`unavailable`）——我们**看不到任何东西**，
     *       这不是"确实没有候选"；</li>
     *   <li>作用域非空、而快照里没有 `labels` 字段（本帧来源未携带该字段：旧控制面响应，或自旧格式落盘
     *       恢复的快照）→ {@link ScheduleState.UNAVAILABLE}（`admission_scope_unavailable`）——判据看不到，
     *       既不能放行（可能不满足作用域）也不能报"没有"。</li>
     * </ul>
     */
    private fun localFallback(
        pool: CandidatePool,
        purpose: String?,
        scope: AdmissionScope,
    ): ScheduleResult {
        val traceId = newLocalTraceId()
        val candidates = pool.entries
        // 判据看不到的两种情形：池整体不可见（[pool] 给 null）；或作用域非空而快照缺 labels 字段。
        // 后者依赖「控制面只下发有可调度候选的区」这一前提：池内一台候选都没有时 any 为 false，
        // 不会被误判成"标签不可见"（那属于本仓之外的下发约定，不是这里能校验的硬保证）。
        val labelsInvisible = !scope.isEmpty && candidates?.any { !it.labelsPresent } == true
        if (candidates == null || labelsInvisible) {
            val reason = if (candidates == null) "unavailable" else "admission_scope_unavailable"
            // 只看事实、不指认成因：两种成因（旧控制面响应 / 自旧格式落盘恢复）在降级路径上长得一样，
            // 指认成任一方都会把玩家（或运维）引向错误方向。
            val snapshotState = if (candidates == null) pool.missingNote else "缺自声明标签字段"
            adapter.warn(
                "选服降级路径判不了：$reason（候选快照$snapshotState）。" +
                    "这是当前状态（可重试），不按「没有候选」这一稳定结论上报",
            )
            return ScheduleResult(null, traceId, DecisionSource.LOCAL_FALLBACK, reason, ScheduleState.UNAVAILABLE)
        }
        val admitted = candidates.filter { scope.admits(it.labels) }
        val best = admitted.filter { it.schedulable }.maxByOrNull { it.score }
        val chosen = best?.toCandidateView(pool.viewZone)
        val failReason =
            when {
                best != null -> null
                // 作用域把候选滤掉了（而不是本来就没有可调度候选）：诊断要能分开这两种。
                !scope.isEmpty && candidates.isNotEmpty() && admitted.isEmpty() -> "no_candidate_in_scope"
                else -> "no_candidate"
            }
        if (pool.reportable) {
            reportQueue.offer(
                LocalDecisionReport(
                    localTraceId = traceId,
                    tsMs = now(),
                    zone = pool.viewZone,
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
        } else {
            // 两个池档都不入补报队列：report-local 的校验要求 zone 非空（空 zone 400），
            // 而池档决策没有 zone；塞进去会让该批 400、刷新循环把整批退回队列，从此堵住后续所有小区档补报。
            // 事件本身仍可读（traceId 与结论在返回值里），只是不落控制面决策日表。
            adapter.warn(
                "池档（入口大厅 / 共享游戏池）降级决策（traceId=$traceId，结论=${failReason ?: "chosen"}）" +
                    "按本地快照生成；控制面 report-local 不接受空 zone，本次不入补报队列",
            )
        }
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
 * 一次降级决策的目标候选池（FR-281）：把小区档与两个池档的差别收在此处，
 * 让收窄 / 排序 / 三态判定只有一份实现。
 *
 * @param viewZone    选中候选视图上的 zone 值：小区档是小区名，两个池档都是空串（池成员不属于任何小区）
 * @param entries     池内候选；**null 表示本帧判据看不到**（不是"池里没有机器"——那是非 null 空表）
 * @param missingNote 判据看不到时的报因短语（只述事实，不指认成因）
 * @param reportable  是否把本次降级决策入补报队列（两个池档都为 false，原因见 [SchedulingView.localFallback]）
 */
internal data class CandidatePool(
    val viewZone: String,
    val entries: List<CandidateEntry>?,
    val missingNote: String,
    val reportable: Boolean,
)

/**
 * 小区档候选池：快照整体缺失 ⇒ 判据看不到（null）；快照覆盖该区但区里没候选 ⇒ 空表（稳定事实）。
 */
internal fun zonePool(
    cache: SchedulingCache,
    zone: String,
): CandidatePool =
    CandidatePool(
        viewZone = zone,
        entries = if (cache.current() == null) null else cache.entriesInZone(zone),
        missingNote = "不存在",
        reportable = true,
    )

/**
 * 入口大厅档候选池（FR-281）：本帧没有大厅模型 ⇒ 判据看不到（null，可重试）；
 * 模型在但池里没有成员 ⇒ 空表（稳定事实）。
 */
internal fun lobbyPool(cache: SchedulingCache): CandidatePool =
    CandidatePool(
        viewZone = LOBBY_VIEW_ZONE,
        entries = cache.poolEntries(PoolSection.LOBBY),
        missingNote = "没有大厅模型（本帧来源未下发 lobby 段）",
        reportable = false,
    )

/**
 * 共享游戏池档候选池（FR-281）：本帧没有共享池模型 ⇒ 判据看不到（null，可重试）；
 * 模型在但池里没有成员 ⇒ 空表（稳定事实）。
 *
 * <p>`reportable = false` 与大厅档同因：report-local 要求 zone 非空，而共享池决策没有 zone
 * （见 [SchedulingView.localFallback]）。</p>
 */
internal fun sharedPool(cache: SchedulingCache): CandidatePool =
    CandidatePool(
        viewZone = SHARED_VIEW_ZONE,
        entries = cache.poolEntries(PoolSection.SHARED),
        missingNote = "没有共享池模型（本帧来源未下发 shared 段）",
        reportable = false,
    )

/**
 * 大厅成员不属于任何小区，故大厅档的候选视图 zone 一律为空串（与接口契约「CHOSEN 时 zone() 为空串」一致）。
 */
internal const val LOBBY_VIEW_ZONE = ""

/**
 * 共享游戏池成员同样不属于任何小区，故共享池档的候选视图 zone 也一律为空串
 * （与大厅档同理，见 [LOBBY_VIEW_ZONE]）。
 */
internal const val SHARED_VIEW_ZONE = ""

/**
 * 只保留满足准入作用域的候选（FR-244 / FR-281 三档同一口径）。
 *
 * <p>作用域为空 ⇒ 全量（与不带作用域的重载逐位等价）；**作用域非空而候选缺 `labels` 字段 ⇒ 抛**
 * [IllegalStateException]：判据看不到时返回空表，会把"看不到"冒充成"没有满足条件的候选"。</p>
 *
 * @throws IllegalStateException 作用域非空、而候选快照缺 `labels` 字段（判据看不到）
 */
internal fun List<CandidateEntry>.admittedBy(scope: AdmissionScope?): List<CandidateEntry> {
    val admission = scope ?: AdmissionScope.empty()
    if (admission.isEmpty || !any { !it.labelsPresent }) {
        return filter { admission.admits(it.labels) }
    }
    // 判不了时的报因只说事实、不指认成因：旧控制面响应与自旧格式落盘恢复的快照在下游长得一样，
    // 指认成任一方都会把玩家（或运维）引向错误方向。
    throw IllegalStateException(
        "候选快照里没有节点自声明标签（labels 字段缺失，本帧来源未携带该字段）：本次按作用域收窄判不了。" +
            "这是当前状态（可重试），不是「没有满足条件的候选」这一稳定结论",
    )
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
 * 池档（入口大厅 / 共享游戏池）的控制面失败结果（FR-281）：控制面回 4xx 一律归
 * {@link ScheduleState.UNAVAILABLE}。
 *
 * <p>池档的"问到了"只有一条路（200；池空也是 200 + 无候选），所以 4xx 只能是**没问成**
 * （对端不认识该池作用域 / 参数被拒）。把它印成"确实没有候选"会让调用方永久放弃一次
 * 本来只是环境未就绪的派房；[ScheduleResult.failReason] 原样带出对端码。</p>
 */
internal fun controlPlaneUnavailable(reason: String): ScheduleResult =
    ScheduleResult(null, newLocalTraceId(), DecisionSource.CONTROL_PLANE, reason, ScheduleState.UNAVAILABLE)

/**
 * 控制面成功决策 → [ScheduleResult]：以服务端 traceId 为准，chosen 视图从本地快照补全 zone/level/online 字段。
 *
 * <p>控制面做成了决策：选了 → {@link ScheduleState.CHOSEN}；没选 → {@link ScheduleState.NO_CANDIDATE}
 * （**稳定事实**，failReason 区分"没候选"还是"候选都被作用域滤掉"）。作用域排除台数原样带出（FR-244）。</p>
 *
 * <p>三档（小区 / 入口大厅池 / 共享游戏池）共用本实现（FR-281）：差异由 [viewZone] 与 [findEntry] 表达，
 * 池档给空串 zone 并在自己那一段里找条目。</p>
 *
 * @param viewZone  选中视图上的 zone 值（小区名；池档为空串）
 * @param findEntry 按 serverId 从本地快照补全展示字段（小区档查该区、池档查对应池段）
 */
internal fun controlPlaneResult(
    viewZone: String,
    findEntry: (String) -> CandidateEntry?,
    decided: SchedDecideOutcome.Decided,
): ScheduleResult {
    val chosen =
        decided.chosen?.let { choice ->
            val entry = findEntry(choice.serverId)
            CandidateView(
                choice.serverId,
                viewZone,
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

package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.ScheduleResult
import java.io.File
import java.util.concurrent.TimeUnit

/**
 * FR-147 健康视图 + FR-244 准入作用域端到端探针（Bukkit 壳）。
 *
 * 两者合并在一个探针，是因为它们读的是**同一份候选快照**、且准入收窄的结果直接
 * 体现在 `CandidateView.labels()` 上——分开写会重复取快照且无法保证两次观测同源。
 *
 * 验证四件事：
 *  1. **健康视图**：`selfHealth()` 返回自身健康（score/level/schedulable/reasons/sampledAt），
 *     其 score/level/schedulable 与外部 admin `/admin/v2/health/{serverId}` 同源可比；
 *  2. **健康上界**：`healthOf()` 返回候选的健康集合，自身必在其中；
 *  3. **准入命中**：带 scope 且标签匹配 → `CHOSEN`，`admissionExcludedCount == 0`；
 *  4. **准入全排除**：带一个绝无节点声明的 scope → `NO_CANDIDATE` 且 `failReason=no_candidate_in_scope`，
 *     `admissionExcludedCount > 0`。
 *
 * ⚠️ 准入作用域的标签真源是**节点自声明**（`registry.Metadata`），不是 admin `server_tag`。
 * 故本探针的 scope 标签键必须与 [SelfDeclarationE2EProbe] 声明的键一致（`BEACON_E2E_DECL_LABEL_KEY`），
 * 用 admin server_tag 打标签不会让这里命中。
 *
 * ⚠️ 带非空 scope 但控制面标签真源未装配时，控制面返回 503，agent 侧表现为该次决策不可用
 * （`UNAVAILABLE` 或抛错），与「确实无候选」是两回事——外部断言必须区分，不得混读。
 *
 * 观测写到 `e2e-health-admission.log`，第 2 段取值即行语义：
 *  - `HEALTH_SELF`        自身健康
 *  - `HEALTH_ALL`         候选健康集合
 *  - `ADMIT_HIT`          带 scope 命中
 *  - `ADMIT_NONE`         带 scope 全部排除
 *  - `ADMIT_EMPTY_SCOPE`  空 scope（不按标签收窄，等价老语义）
 *  - `ADMIT_ERROR`        调用抛异常（三态契约下不应出现）
 *
 * 由环境变量 `BEACON_E2E_HEALTH_ADMISSION` 门控（非空才启用）。
 */
object SchedulingHealthE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-health-admission.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒刷新一次。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** 决策 future 短等上限（秒）：fail-static 下应快速完成。 */
    private const val ACQUIRE_TIMEOUT_SEC = 3L

    /** 目标小区名（环境注入）；空则不启用（决策必须带合法 zone）。 */
    private val zone: String = System.getenv("BEACON_E2E_SCHED_ZONE") ?: ""

    /** 与自声明探针共用的可区分标签键。 */
    private val declLabelKey: String = System.getenv("BEACON_E2E_DECL_LABEL_KEY")?.takeIf { it.isNotBlank() } ?: "e2e-round"

    /**
     * 绝无节点会声明的标签键值对：用来构造「作用域内无候选」。
     *
     * 刻意用一个语义上不可能出现在本工程里的键，避免与真实部署的标签撞车导致误命中。
     */
    private const val IMPOSSIBLE_LABEL_KEY = "e2e-never-declared"

    private const val IMPOSSIBLE_LABEL_VALUE = "impossible"

    /**
     * 启动探针：清旧标记 → 启异步周期刷新。
     * 由 BeaconE2EBukkit 在 ENABLE 阶段调用。
     */
    fun start() {
        if (System.getenv("BEACON_E2E_HEALTH_ADMISSION").isNullOrBlank()) {
            info("未配置 BEACON_E2E_HEALTH_ADMISSION，跳过健康/准入探针")
            return
        }
        if (zone.isBlank()) {
            info("未配置 BEACON_E2E_SCHED_ZONE，健康/准入探针缺少决策小区，跳过")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        info("Beacon E2E 健康/准入探针已启用，小区=$zone，标签键=$declLabelKey，标记文件=${markFile.absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /** 刷新一轮健康与三种准入场景的观测。 */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val scheduling = BeaconAgentProvider.get().scheduling()
        val self = BeaconAgentProvider.get().identity()

        runCatching {
            // healthOf 是「按 serverId 取单个健康」，不是集合；selfHealth 取自身（无参）。
            // 两者都是可空返回：缓存未就绪时为空是合法状态，落 null 标记而非当作异常。
            recordHealth(markFile, "HEALTH_SELF", scheduling.selfHealth())
            recordHealth(markFile, "HEALTH_OF_SELF", scheduling.healthOf(self.serverId()))
        }.onFailure {
            E2EObservation.append(markFile, "HEALTH_ERROR", "health", "-", "err=${it.javaClass.simpleName}: ${it.message}")
        }

        // 三种准入场景：空 scope（不过滤）/ 命中 scope / 绝不可能命中的 scope。
        recordDecision(markFile, "ADMIT_EMPTY_SCOPE", AdmissionScope.empty())
        // 命中：声明标签里存的就是本服当前的轮次值，故直接取最近一次声明的值构造精确匹配。
        //
        // 为什么不用「枚举若干轮次值」的 anyOf：控制面对备选个数有硬上限（本版 8），
        // 枚举到第 9 个就会被判 INVALID_PARAM 而整次决策失败——这会把「作用域不合法」
        // 误报成「准入不命中」。故只用单个备选，标签值从本探针自己声明的轮次取。
        val hitScope = currentHitScope()
        if (hitScope != null) {
            recordDecision(markFile, "ADMIT_HIT", hitScope)
        } else {
            // 尚未声明过 → 无法构造命中场景，落标记让外部知道这是时序未就绪而非失败。
            E2EObservation.append(markFile, "ADMIT_SKIP", "ADMIT_HIT", "-", "尚未完成首次自声明，无可匹配标签")
        }
        recordDecision(markFile, "ADMIT_NONE", AdmissionScope.of(IMPOSSIBLE_LABEL_KEY, IMPOSSIBLE_LABEL_VALUE))
    }

    /**
     * 用本探针自己刚声明成功的标签值构造「精确命中」的作用域。
     *
     * ## 为什么要自己声明而不是读别的探针的轮次
     *
     * 两个探针各自的循环节奏不同，读对方"当前轮次"必然有时序竞态：读到的是它上一轮的值，
     * 而标签已被它推进到下一轮 —— 作用域精确匹配就落空，得到「准入不命中」的假失败，
     * 真正的缺陷（准入不生效）反而被掩盖。故本探针每轮先自己声明一次，
     * 用**自己刚刚写进去的值**构造作用域，因果关系是闭合的。
     *
     * 单点返回（detekt ReturnCount）：声明失败与成功两种结局都收敛到一个表达式，
     * 返回 null 表示「构造不出可信的命中场景」，由调用方落 ADMIT_SKIP 标记。
     */
    private fun currentHitScope(): AdmissionScope? {
        val declaration = BeaconAgentProvider.get().declaration()
        val roundValue = "hit${System.nanoTime()}"
        val outcome =
            runCatching {
                declaration.declare(NodeDeclaration.ofLabels(mapOf(declLabelKey to roundValue)))
            }.getOrNull()
        // 只有真的 APPLIED 才算数：声明没成功时作用域里的值并不存在于控制面，用它构造命中是自欺。
        return outcome
            ?.takeIf { it.status() == DeclarationOutcome.Status.APPLIED }
            ?.let { AdmissionScope.of(declLabelKey, roundValue) }
    }

    /**
     * 落一条健康视图观测。
     *
     * 空视图是**合法状态**（候选快照未就绪），落 `null` 标记供外部区分
     * 「取到了但是空」与「调用抛异常」——后者走 HEALTH_ERROR，二者绝不能混读。
     */
    private fun recordHealth(
        markFile: File,
        source: String,
        view: HealthView?,
    ) {
        if (view == null) {
            E2EObservation.append(markFile, source, "-", "-", "null")
            return
        }
        E2EObservation.append(
            markFile,
            source,
            view.serverId(),
            view.score().toString(),
            "level=${view.level()};schedulable=${view.schedulable()};sampledAtMs=${view.sampledAtMs()};" +
                "reasons=${view.reasons().joinToString(",").ifEmpty { "-" }}",
        )
    }

    /**
     * 按给定 scope 做一次决策并落观测。
     *
     * 决策走 future，必须短等上限；fail-static 契约要求它正常完成——
     * 抛异常即记为 ADMIT_ERROR 供外部暴露缺陷。
     */
    private fun recordDecision(
        markFile: File,
        source: String,
        scope: AdmissionScope,
    ) {
        val scheduling = BeaconAgentProvider.get().scheduling()
        val result =
            try {
                scheduling.acquireCandidate(zone, "beacon-e2e", scope).get(ACQUIRE_TIMEOUT_SEC, TimeUnit.SECONDS)
            } catch (t: Throwable) {
                E2EObservation.append(markFile, "ADMIT_ERROR", source, "-", "err=${t.javaClass.simpleName}: ${t.message}")
                return
            }
        E2EObservation.append(markFile, source, zone, renderChosen(result), renderDecision(result))
    }

    /** 选中项渲染（无选中给 '-'），便于外部直接比对 serverId。 */
    private fun renderChosen(result: ScheduleResult): String = result.chosen()?.serverId() ?: "-"

    /** 决策明细：状态/来源/失败原因/被作用域排除台数/候选标签。 */
    private fun renderDecision(result: ScheduleResult): String {
        val chosen: CandidateView? = result.chosen()
        val labels = chosen?.labels()?.takeIf { it.isNotEmpty() }?.let { renderMap(it) } ?: "-"
        return "state=${result.state()};source=${result.source()};failReason=${result.failReason() ?: "-"};" +
            "admissionExcludedCount=${result.admissionExcludedCount()};traceId=${result.traceId()};labels=$labels"
    }

    /** 键值对渲染为稳定排序文本。 */
    private fun renderMap(map: Map<String, String>): String = map.entries.sortedBy { it.key }.joinToString(",") { "${it.key}=${it.value}" }
}

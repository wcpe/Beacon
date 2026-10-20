package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import java.io.File
import java.util.concurrent.atomic.AtomicInteger

/**
 * FR-243 节点自声明端到端探针（Bukkit 壳）。
 *
 * 验证 agent-api 里**唯一的写入面** `SelfDeclaration.declare(NodeDeclaration)` 的三态：
 *  - `APPLIED`：容量与标签被控制面接受，回带生效值，且 admin 实例视图可读回；
 *  - `REJECTED`：非法声明（如负容量）被控制面拒绝并带原因；
 *  - `UNAVAILABLE`：控制面不可达时优雅降级，**不抛异常**（fail-static）。
 *
 * 观测写到 `e2e-declaration.log`，单行格式沿用 [E2EObservation]；第 2 段取值即行语义：
 *  - `DECLARE_APPLIED`    声明被接受（第 3 段 = 声明用途标签，第 4/5 段 = 回带容量/标签）
 *  - `DECLARE_REJECTED`   声明被拒绝（第 5 段含 rejectReason）
 *  - `DECLARE_UNAVAILABLE` 声明因控制面不可达降级
 *  - `DECLARE_ERROR`      门面抛异常（三态契约下不应出现，出现即缺陷）
 *
 * ## 为什么声明内容要靠环境变量逐轮更换
 *
 * 声明是**部分刷新**语义：只带容量则只改容量，只带标签则只改标签。
 * 若每轮都声明同一组值，外部无法区分「这一轮真的生效了」与「上一轮的残留」。
 * 故用轮次号（[round]）拼进标签值，让每次声明都产生可区分的新值，
 * 外部据此断言「本轮声明的值确实被接受并回带」。
 *
 * ## 与 admin server_tag 的关系（重要）
 *
 * 自声明标签落的是控制面**内存注册表**的 `Instance.Metadata`，与 admin 的 `server_tag` 表
 * 是两套互不连通的标签系统。本探针声明的标签**只能**用 admin 实例视图的 `metadata` 字段核实，
 * 不能用 `server_tag` 相关接口——否则会得到「声明成功但查不到」的假失败。
 *
 * 由环境变量 `BEACON_E2E_DECLARATION` 门控（非空才启用）。
 * 第 4 段的可区分标签键由 `BEACON_E2E_DECL_LABEL_KEY` 指定（默认 `e2e-round`），
 * 与准入作用域探针共用时必须一致，供其构造「scope 命中」场景。
 */
object SelfDeclarationE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-declaration.log"

    /** 轮询周期（tick，20 tick/秒）：约每 4 秒推进一轮声明。 */
    private const val POLL_INTERVAL_TICKS = 80L

    /** 声明持久化的目标容量（外部可按此期望值断言 admin 实例视图）。 */
    private const val TARGET_CAPACITY = 120

    /** 可区分标签键：准入作用域探针要引用同一个键构造命中场景。 */
    private val labelKey: String = System.getenv("BEACON_E2E_DECL_LABEL_KEY")?.takeIf { it.isNotBlank() } ?: "e2e-round"

    /** 轮次号：让每轮声明值可区分，避免「上一轮残留」冒充本轮生效。 */
    private val round = AtomicInteger(0)

    /**
     * 启动探针：清旧标记 → 启异步周期声明。
     * 由 BeaconE2EBukkit 在 ENABLE 阶段调用。
     */
    fun start() {
        if (System.getenv("BEACON_E2E_DECLARATION").isNullOrBlank()) {
            info("未配置 BEACON_E2E_DECLARATION，跳过节点自声明探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        round.set(0)
        info("Beacon E2E 自声明探针已启用，可区分标签键=$labelKey，标记文件=${markFile.absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /**
     * 推进一轮声明：先合法声明（APPLIED），再非法声明（REJECTED）。
     *
     * 两者每轮都做，是因为外部对不同相位的等待时机不同：
     *  - APPLIED 要在正常相位断言（控制面在线）；
     *  - REJECTED 与在线状态无关，随时可测。
     * UNAVAILABLE 只在杀控制面相位才会自然出现，探针不主动制造——它由「合法声明 + CP 不可达」自然落到。
     */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val n = round.incrementAndGet()
        val declaration = BeaconAgentProvider.get().declaration()

        // 合法声明：容量 + 可区分标签。两个都带，验证部分刷新语义下「同时刷新两者」也成立。
        runCatching {
            declaration.declare(NodeDeclaration.of(TARGET_CAPACITY, mapOf(labelKey to "r$n")))
        }.onSuccess { outcome ->
            record(markFile, "APPLIED", n, outcome)
        }.onFailure { e ->
            E2EObservation.append(markFile, "DECLARE_ERROR", "applied", "-", "err=${e.javaClass.simpleName}: ${e.message}")
        }

        // 非法声明：负容量必须被拒。用独立的一轮标签，避免与合法声明的观测混淆。
        runCatching {
            declaration.declare(NodeDeclaration.ofCapacity(-1))
        }.onSuccess { outcome ->
            record(markFile, "REJECTED", n, outcome)
        }.onFailure { e ->
            E2EObservation.append(markFile, "DECLARE_ERROR", "rejected", "-", "err=${e.javaClass.simpleName}: ${e.message}")
        }
    }

    /** 把三态结果落成观测行；行首语义段按声明用途（applied/rejected）区分。 */
    private fun record(
        markFile: File,
        purpose: String,
        round: Int,
        outcome: DeclarationOutcome,
    ) {
        val detail =
            when (outcome.status()) {
                DeclarationOutcome.Status.APPLIED -> {
                    val applied = outcome.applied().orElse(null)
                    val capacity = applied?.capacity()?.orElse(null)?.toString() ?: "-"
                    val labels = applied?.labels()?.takeIf { it.isNotEmpty() }?.let { renderMap(it) } ?: "-"
                    "round=$round;capacity=$capacity;labels=$labels"
                }
                DeclarationOutcome.Status.REJECTED ->
                    "round=$round;rejectReason=${outcome.rejectReason().orElse("-")}"
                DeclarationOutcome.Status.UNAVAILABLE ->
                    "round=$round;detail=控制面不可达，声明优雅降级"
                else ->
                    "round=$round;detail=未知状态"
            }
        // 第 2 段用 DECLARE_<状态> 而非直接复用 purpose：外部断言时既要区分「这次声明想干什么」，
        // 也要能看到「实际回来了什么状态」，两者都落盘才不会把「声明成功」误读成「拒绝了」。
        E2EObservation.append(markFile, "DECLARE_${outcome.status().name}", purpose, round.toString(), detail)
    }

    /** 键值对渲染为稳定排序文本。 */
    private fun renderMap(map: Map<String, String>): String = map.entries.sortedBy { it.key }.joinToString(",") { "${it.key}=${it.value}" }
}

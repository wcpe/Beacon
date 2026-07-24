package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import java.io.File
import java.util.concurrent.TimeUnit

/**
 * FR-148 调度门面端到端探针（Bukkit 壳）。
 *
 * 作为「业务插件」经 agent 的纯 Java 只读 API `BeaconAgentProvider.get().scheduling()` 周期取候选，把每次
 * acquireCandidate 结果（决策来源 / 选中 serverId / traceId / 数据源新鲜度 / 候选数）追加写到数据目录下的
 * `e2e-scheduling.log`，供外部 Go 驱动断言：
 *  - 正常路径：source=CONTROL_PLANE 且选中该服；
 *  - 杀控制面 fail-static：source=LOCAL_FALLBACK 仍返回快照候选、不阻断、无未捕获异常；
 *  - 恢复：自动回 CONTROL_PLANE。
 *
 * 目标小区名经环境变量 `BEACON_E2E_SCHED_ZONE` 注入（agent 启动早于建区，故不能靠自身 zone 回填）。
 */
object SchedulingE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-scheduling.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒取一次候选。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** acquireCandidate future 短等上限（秒）：fail-static 下应快速完成（不阻塞玩家链路）。 */
    private const val ACQUIRE_TIMEOUT_SEC = 3L

    /** 目标小区名（环境注入）；空则不启用本探针。 */
    private val zone: String = System.getenv("BEACON_E2E_SCHED_ZONE") ?: ""

    fun start() {
        if (zone.isBlank()) {
            info("未配置 BEACON_E2E_SCHED_ZONE，跳过 FR-148 调度探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        info("Beacon E2E 调度探针已启用，目标小区=$zone，标记文件=${markFile.absolutePath}")
        // 异步周期取候选（绝不在主线程；acquireCandidate 内部亦异步，本处仅短等其 future）。
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /** 取一次候选并写观测；任何异常都写为观测行（fail-static 契约下不应出现未捕获异常）。 */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val scheduling = BeaconAgentProvider.get().scheduling()
        val dataSource = scheduling.dataSource()
        val candidateCount = scheduling.candidatesInZone(zone).size
        val result =
            try {
                scheduling.acquireCandidate(zone).get(ACQUIRE_TIMEOUT_SEC, TimeUnit.SECONDS)
            } catch (t: Throwable) {
                // fail-static 契约要求 future 正常完成；走到这里即缺陷，如实记录供断言暴露。
                E2EObservation.append(markFile, "ACQUIRE_ERROR", zone, "-", "err=${t.message}")
                return
            }
        val chosen = result.chosen()?.serverId() ?: "-"
        val raw =
            "traceId=${result.traceId()};dataSource=${dataSource.source()};fresh=${dataSource.fresh()};" +
                "candidates=$candidateCount;failReason=${result.failReason() ?: "-"}"
        // 观测行：来源 | 小区 | 选中 serverId | 明细（外部驱动按 | 分割解析）。
        E2EObservation.append(markFile, result.source().name, zone, chosen, raw)
    }
}

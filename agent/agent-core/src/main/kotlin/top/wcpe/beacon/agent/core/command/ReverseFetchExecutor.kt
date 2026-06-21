package top.wcpe.beacon.agent.core.command

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 反向抓取执行器（FR-39，见 ADR-0027）：拉待办命令 → 读真实 plugins 树 → 过滤校验（纯函数）→ 回传 ingest。
 *
 * 由 [top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle] 在 async 线程触发（SSE command-pending 事件 / READY 对账）；
 * 本类不自调度、不碰 MC 主线程；读盘委托 [PlatformAdapter.readPluginsTree]（壳层实现，已做 FS 级路径安全）。
 *
 * 安全与上限全交 core 纯函数 [PluginsTreeFilter]（路径字符串安全 + 排除 jar/二进制 + 上限）；
 * 任一上限超标 → 整体失败、不部分上传（命令在控制面侧标 failed）。agent 为最终权威，控制面入库前再同口径校验。
 *
 * **不主动、不常驻**（ADR-0027 决策 8）：仅被触发时拉一次命令、有命令才读一次盘上传，无命令立即返回。
 * **单飞**：command-pending 与 READY 可能并发触发，[running] 门保证任意时刻只跑一条抓取流，避免重复读盘上传。
 *
 * @param identity  本 agent 身份（拉命令 / 回传携带 namespace + serverId）
 * @param apiClient REST 客户端（拉命令 / 回传 ingest）
 * @param adapter   平台适配（读真实 plugins 树 + 日志）
 */
class ReverseFetchExecutor(
    private val identity: AgentIdentity,
    private val apiClient: BeaconApiClient,
    private val adapter: PlatformAdapter,
) {

    /** 单飞门：任意时刻只允许一条抓取流在跑（command-pending 与 READY 并发触发时去重）。 */
    private val running = AtomicBoolean(false)

    /**
     * 触发一次「拉取并执行待办命令」流程。**须在 async 线程调用**（内部读盘 + HTTP 均阻塞 IO）。
     *
     * 单飞：已有一条在跑则本次直接返回（no-op）——command-pending 与 READY 并发触发只跑一条。
     * 排空：单飞期间可能又有命令排进来（其 command-pending 被单飞门挡掉），故本轮**循环拉到无待办（204）为止**，
     * 不遗留排队命令；带迭代上限兜底，杜绝控制面异常下的无限循环。
     */
    fun trigger() {
        if (!running.compareAndSet(false, true)) return // 已有抓取在跑，去重
        try {
            var iterations = 0
            // 循环排空：每轮拉一条并执行，拉到无待办（runOnce 返回 false）或达迭代上限即止。
            while (runOnce() && ++iterations < MAX_DRAIN_PER_TRIGGER) {
                // 继续拉下一条待办命令。
            }
        } finally {
            running.set(false)
        }
    }

    /**
     * 拉一条命令并执行。
     *
     * @return true 表示本轮确实取到一条命令（已处理 / 已忽略未知类型，应继续排空）；false 表示无待办命令（停止排空）。
     */
    private fun runOnce(): Boolean {
        val command = apiClient.fetchPendingCommand(identity) ?: return false // 无待办，停止排空
        if (command.type != AgentCommand.TYPE_INGEST_PLUGINS) {
            // 本期只接 ingest-plugins；未知类型不处理（不预埋多命令空壳，守 scope-discipline）。命令已被控制面 CAS fetched、不会重现，继续排空。
            adapter.warn("收到未知命令类型（忽略）：id=${command.id}，type=${command.type}")
            return true
        }
        adapter.info(
            "执行反向抓取命令：id=${command.id}，scope=${command.payload.scope}，" +
                "group=${command.payload.group}，target=${command.payload.target.ifEmpty { "-" }}",
        )

        // 读真实 plugins 树（壳层在此实现 FS 级路径安全 + 符号链接逃逸判定）。读盘失败 / 桩未实现得空映射。
        val tree = try {
            adapter.readPluginsTree()
        } catch (e: Exception) {
            // 读盘失败属本地异常：放弃本命令、不回传（命令交由控制面超时清理为 expired，绝不误标 done）；仍继续排空其余命令。
            adapter.error("读 plugins 目录失败，放弃本次反向抓取：id=${command.id}", e)
            return true
        }

        // 过滤 + 上限校验（纯函数）：排除剔除项后判上限，任一超限整体失败、不部分上传。
        when (val outcome = PluginsTreeFilter.filter(tree)) {
            is FilterOutcome.Rejected -> {
                adapter.warn("反向抓取超限，整体失败、不上传：id=${command.id}，原因=${outcome.reason}")
                // 不上传：命令由控制面超时清理为 expired（不构造空回传，免误标 done）。
            }

            is FilterOutcome.Accepted -> {
                val ok = apiClient.uploadIngest(command.id, outcome.files)
                if (ok) {
                    adapter.info("反向抓取回传成功：id=${command.id}，文本文件=${outcome.files.size}")
                } else {
                    adapter.warn("反向抓取回传失败（命令态不符 / 控制面校验拒 / 连接失败）：id=${command.id}")
                }
            }
        }
        return true // 本命令已处理，继续排空下一条
    }

    companion object {
        /** 单次 trigger 排空命令的迭代上限（兜底：控制面异常下杜绝无限循环；正常场景待办命令极少）。 */
        private const val MAX_DRAIN_PER_TRIGGER = 64
    }
}

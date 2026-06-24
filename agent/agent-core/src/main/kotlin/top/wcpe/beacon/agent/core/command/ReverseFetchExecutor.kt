package top.wcpe.beacon.agent.core.command

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.log.AgentLogBuffer
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
 * 取日志命令（FR-88，见 ADR-0040）复用同一命令通路与单飞排空：tail-logs 命令拉到后读
 * [logBuffer] 自身日志环形缓冲快照（已脱敏）回传，**绝不读任何磁盘日志文件**。
 *
 * 强制重同步命令（FR-91）同样复用此通路：resync-config 命令拉到后调 [onResyncConfig] 回调
 * （重拉有效配置/文件树/覆盖集并 apply），再经命令结果端点回传 done/failed，**不读 plugins 树**。
 *
 * @param identity  本 agent 身份（拉命令 / 回传携带 namespace + serverId）
 * @param apiClient REST 客户端（拉命令 / 回传 ingest / 回传日志 / 回传命令结果）
 * @param adapter   平台适配（读真实 plugins 树 + 日志）
 * @param logBuffer          agent 自身日志环形缓冲（FR-88，可选；为 null 时不响应 tail-logs，按未知类型忽略）
 * @param onResyncConfig     强制重同步回调（FR-91，可选；为 null 时不响应 resync-config，按未知类型忽略）。
 *                           延迟调用、命令期才触发——故由装配方以 lambda 闭包注入（打破与 lifecycle 的构造顺序）。
 *                           返回 true=已执行三条重拉；false=因 agent 未运行/正在停机跳过（runResync 据此回传 failed，不误报 done）。
 * @param reverseFetchEnabled 是否启用反向抓取读盘路径（plugins 基目录有效时 true；无效时 fail-closed 关闭，
 *                            收到 ingest-plugins 命令仅记 warn、不读盘——但 tail-logs / resync-config 不受此守卫影响）
 */
class ReverseFetchExecutor(
    private val identity: AgentIdentity,
    private val apiClient: BeaconApiClient,
    private val adapter: PlatformAdapter,
    private val logBuffer: AgentLogBuffer? = null,
    private val onResyncConfig: (() -> Boolean)? = null,
    private val reverseFetchEnabled: Boolean = true,
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
     * 拉一条命令并执行。按 payload.mode 两段式分路（FR-58，见 ADR-0037）：
     * - scan：只读元信息清单 → uploadScan（永不失败）；
     * - submit / 空（旧整树兼容）：读内容 → uploadIngest。
     *
     * @return true 表示本轮确实取到一条命令（已处理 / 已忽略未知类型，应继续排空）；false 表示无待办命令（停止排空）。
     */
    private fun runOnce(): Boolean {
        val command = apiClient.fetchPendingCommand(identity) ?: return false // 无待办，停止排空
        // 取日志命令（FR-88）：读自身日志环形缓冲快照回传，不读任何磁盘文件。
        if (command.type == AgentCommand.TYPE_TAIL_LOGS) {
            runTailLogs(command)
            return true
        }
        // 强制重同步命令（FR-91）：调重同步回调重拉有效配置/文件树/覆盖集，回传命令结果，不读 plugins 树。
        if (command.type == AgentCommand.TYPE_RESYNC_CONFIG) {
            runResync(command)
            return true
        }
        if (command.type != AgentCommand.TYPE_INGEST_PLUGINS) {
            // 只接 ingest-plugins / tail-logs / resync-config；未知类型不处理（不预埋多命令空壳，守 scope-discipline）。命令已被控制面 CAS fetched、不会重现，继续排空。
            adapter.warn("收到未知命令类型（忽略）：id=${command.id}，type=${command.type}")
            return true
        }
        // 反向抓取读盘路径 fail-closed 守卫：plugins 基目录无效时不读盘上传（避免从错误目录读），仅记 warn 放弃本命令。
        if (!reverseFetchEnabled) {
            adapter.warn("plugins 基目录无效，反向抓取读盘已关闭（忽略命令）：id=${command.id}")
            return true
        }
        adapter.info(
            "执行反向抓取命令：id=${command.id}，mode=${command.payload.mode.ifEmpty { "-" }}，scope=${command.payload.scope}，" +
                "group=${command.payload.group}，target=${command.payload.target.ifEmpty { "-" }}",
        )

        when (command.payload.mode) {
            IngestCommandPayload.MODE_SCAN -> runScan(command)
            // submit 与旧空 mode 都走读内容回传：submit 限定选定子集，空 mode 维持旧整树行为（向后兼容）。
            else -> runSubmit(command)
        }
        return true // 本命令已处理，继续排空下一条
    }

    /**
     * scan 阶段（FR-58）：只 stat 取元信息清单 → uploadScan，**永不因超限失败**（超阈值文件以红标列出）。
     *
     * 读元信息失败（本地 IO 异常）→ 回传错误到控制面（FR-87），令任务转 failed 记 lastError，
     * 不再让任务静默卡在 scanning 等过期清理；随后放弃本命令、不回传清单（绝不误标 done）。
     */
    private fun runScan(command: AgentCommand) {
        // 只 stat 取大小（壳层在此实现 FS 级路径安全 + 符号链接逃逸判定，不读内容）。失败 / 桩未实现得空映射。
        val metadata = try {
            adapter.readPluginsTreeMetadata()
        } catch (e: Exception) {
            adapter.error("扫描 plugins 目录元信息失败，放弃本次扫描：id=${command.id}", e)
            reportError(command, "扫描 plugins 目录元信息失败：${e.javaClass.simpleName}: ${e.message ?: "无错误信息"}")
            return
        }
        // 纯函数过滤 + 标注：排除不安全路径 / jar，超阈值仅红标，永不失败。
        val files = PluginsTreeFilter.scan(metadata)
        val ok = apiClient.uploadScan(command.id, files)
        if (ok) {
            val over = files.count { it.overThreshold }
            adapter.info("反向抓取扫描回传成功：id=${command.id}，清单文件=${files.size}，超阈值=$over")
        } else {
            adapter.warn("反向抓取扫描回传失败（命令态不符 / 控制面校验拒 / 连接失败）：id=${command.id}")
        }
    }

    /**
     * submit 阶段（FR-58）/ 旧整树兼容：读内容回传 ingest。
     *
     * submit（selectedPaths 非空）限定只回传选定子集；空 mode 且无 selectedPaths → 维持旧整树读内容回传（向后兼容）。
     * 读盘失败 → submit 模式下回传错误到控制面（FR-87），令任务转 failed 记 lastError；随后放弃本命令、不回传内容
     * （绝不误标 done）。旧整树兼容（无 mode、非受管任务）下回传虽无对应任务，控制面按命令态不符拒、无副作用。
     */
    private fun runSubmit(command: AgentCommand) {
        // 读真实 plugins 树（壳层在此实现 FS 级路径安全 + 符号链接逃逸判定）。读盘失败 / 桩未实现得空映射。
        val tree = try {
            adapter.readPluginsTree()
        } catch (e: Exception) {
            adapter.error("读 plugins 目录失败，放弃本次反向抓取：id=${command.id}", e)
            // 仅 submit 模式回传错误（其所属任务处 fetching）；旧整树兼容无受管任务，回传也只会被控制面按命令态拒。
            if (command.payload.mode == IngestCommandPayload.MODE_SUBMIT) {
                reportError(command, "读 plugins 目录失败：${e.javaClass.simpleName}: ${e.message ?: "无错误信息"}")
            }
            return
        }

        val selected = command.payload.selectedPaths
        // submit（有选定集）：限定只回传选定子集，超阈值由控制面已确认门控，仅文件数/总字节兜底。
        // 旧整树兼容（mode 空且无选定集）：沿用 filter（含单文件超限整批失败口径），不改旧 agent 行为。
        val outcome = if (selected.isNotEmpty()) {
            PluginsTreeFilter.submitFilter(tree, selected)
        } else {
            PluginsTreeFilter.filter(tree)
        }

        when (outcome) {
            is FilterOutcome.Rejected -> {
                adapter.warn("反向抓取回传超限，整体失败、不上传：id=${command.id}，原因=${outcome.reason}")
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
    }

    /**
     * 取日志阶段（FR-88，见 ADR-0040）：读 agent 自身日志环形缓冲快照（已脱敏）回传 uploadLogs。
     *
     * **绝不读任何磁盘日志文件**——只取内存环形缓冲的当前快照（有界、落缓冲即脱敏）。
     * 未注入 logBuffer（旧装配 / 测试桩）则按未知能力忽略：记 warn、不回传（控制面超时清理）。
     */
    private fun runTailLogs(command: AgentCommand) {
        val buffer = logBuffer
        if (buffer == null) {
            adapter.warn("收到取日志命令但未启用日志缓冲（忽略）：id=${command.id}")
            return
        }
        val lines = buffer.snapshot()
        val ok = apiClient.uploadLogs(command.id, lines)
        if (ok) {
            adapter.info("agent 日志回传成功：id=${command.id}，行数=${lines.size}")
        } else {
            adapter.warn("agent 日志回传失败（命令态不符 / 连接失败）：id=${command.id}")
        }
    }

    /**
     * 强制重同步阶段（FR-91）：调 [onResyncConfig] 回调重拉控制面权威的有效配置/文件树/覆盖集并 apply，
     * 再经命令结果端点回传 done。**绝不读 plugins 树**（重同步语义是重拉权威配置、非反向抓盘）。
     *
     * 回调内部各 applier 的 md5 幂等守卫兜底：已是最新则无害 no-op。回调抛异常 → 回传 failed（带原因摘要）。
     * 回调返回 false（agent 未运行 / 正在停机的极窄窗口跳过了重拉）→ 回传 failed（比误报 done 诚实，控制面 CAS 转 failed）。
     * 未注入回调（旧装配 / 测试桩）则按未知能力忽略：记 warn、不回传（控制面超时清理）。
     */
    private fun runResync(command: AgentCommand) {
        val callback = onResyncConfig
        if (callback == null) {
            adapter.warn("收到强制重同步命令但未启用重同步回调（忽略）：id=${command.id}")
            return
        }
        val executed = try {
            callback()
        } catch (e: Exception) {
            adapter.error("强制重同步执行失败：id=${command.id}", e)
            val ok = apiClient.uploadCommandResult(command.id, ok = false, reason = "${e.javaClass.simpleName}: ${e.message ?: "无错误信息"}")
            if (!ok) adapter.warn("强制重同步失败结果回传失败（命令态不符 / 连接失败）：id=${command.id}")
            return
        }
        // 回调跳过（agent 未运行 / 正在停机）→ 回传 failed，不误报 done（真实未执行重拉）。
        if (!executed) {
            adapter.warn("强制重同步被跳过（agent 未运行/正在停机）：id=${command.id}")
            val ok = apiClient.uploadCommandResult(command.id, ok = false, reason = "agent 未运行/正在停机，跳过强制重同步")
            if (!ok) adapter.warn("强制重同步跳过结果回传失败（命令态不符 / 连接失败）：id=${command.id}")
            return
        }
        val ok = apiClient.uploadCommandResult(command.id, ok = true, reason = "")
        if (ok) {
            adapter.info("强制重同步完成并回传：id=${command.id}")
        } else {
            adapter.warn("强制重同步完成结果回传失败（命令态不符 / 连接失败）：id=${command.id}")
        }
    }

    /**
     * 回传一条执行错误到控制面（FR-87）：best-effort，回传失败仅记 warn、不重试（交控制面超时清理兜底）。
     */
    private fun reportError(command: AgentCommand, reason: String) {
        val ok = apiClient.uploadError(command.id, reason)
        if (ok) {
            adapter.info("已回传反向抓取执行错误：id=${command.id}")
        } else {
            adapter.warn("回传反向抓取执行错误失败（命令态不符 / 连接失败）：id=${command.id}")
        }
    }

    companion object {
        /** 单次 trigger 排空命令的迭代上限（兜底：控制面异常下杜绝无限循环；正常场景待办命令极少）。 */
        private const val MAX_DRAIN_PER_TRIGGER = 64
    }
}

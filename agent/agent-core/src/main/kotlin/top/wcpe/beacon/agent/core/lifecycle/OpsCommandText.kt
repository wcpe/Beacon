package top.wcpe.beacon.agent.core.lifecycle

/**
 * 运维命令的纯文本与回显构造（无平台依赖，守 ADR-0005）。
 *
 * 壳层 status/reload/reconnect/resync/help 命令只负责把 sender 与 lifecycle 接起来，
 * 文案与「调哪个动作」的拼装收敛在此，两端壳共用、杜绝复制粘贴。
 *
 * 帮助与提示文案（FR-54，增强 FR-17）：各子命令的「一行用法」单一来源于 [SUBCOMMANDS]，
 * USAGE_LINES / HELP_LINES / 错参提示均由它派生，新增子命令只改一处。
 */
object OpsCommandText {

    /** 根命令名（拼装用法行用，避免散落硬编码 "beacon"）。 */
    private const val ROOT = "beacon"

    /**
     * 单个子命令的帮助条目：名称 + 一行中文用途。
     *
     * @param name  子命令字面量
     * @param usage 用法说明（一行中文，讲清这条命令干什么、有何前置条件）
     */
    data class Subcommand(val name: String, val usage: String)

    /**
     * 全部子命令的权威清单（顺序即帮助展示顺序）。
     *
     * 与壳层注册的 literal 子命令一一对应；新增 / 调整子命令时同步改此处与壳层注册。
     */
    val SUBCOMMANDS: List<Subcommand> = listOf(
        Subcommand("status", "查看 agent 接入与有效配置状态（生命周期 / 是否连上 / 有效配置 md5 / 心跳周期 / endpoint）"),
        Subcommand("reload", "强制立刻重拉有效配置并应用，不等长轮询超时（不影响已生效配置）"),
        Subcommand("reconnect", "打断退避、重置并重新接入控制面（保留当前有效配置，玩家不掉线）"),
        Subcommand("resync", "强制立刻重新同步文件树（需在 config.yml 开启 file-tree.enabled）"),
        Subcommand("help", "查看本帮助（各子命令用法）"),
    )

    /** 子命令名按竖线拼成的可选集，如 status|reload|reconnect|resync|help。 */
    private val SUBCOMMAND_CHOICE: String = SUBCOMMANDS.joinToString("|") { it.name }

    /** 根命令用法首行（如 用法：/beacon <status|reload|reconnect|resync|help>）。 */
    val USAGE_HEADER: String = "用法：/$ROOT <$SUBCOMMAND_CHOICE>"

    /** 单条子命令对齐缩进展示行，如 "  status    查看 ..."。 */
    private fun subcommandLine(sub: Subcommand): String {
        // 名称右补空格对齐到 9 列（最长子命令 reconnect 为 9 字），再接用法说明。
        val padded = sub.name.padEnd(9)
        return "  $padded${sub.usage}"
    }

    /** status：把 LifecycleSnapshot 渲染成多行中文，供壳层逐行回显。 */
    fun statusLines(snapshot: LifecycleSnapshot): List<String> = listOf(
        "Beacon agent 状态：",
        "  生命周期=${snapshot.state}",
        "  已连控制面=${if (snapshot.connected) "是" else "否"}",
        "  有效配置 md5=${snapshot.effectiveMd5 ?: "（暂无）"}",
        "  心跳周期=${snapshot.heartbeatIntervalSec}s",
        "  控制面 endpoint=${snapshot.endpoint}",
    )

    /** reload 命令触发回显（动作本身异步执行，日志由 lifecycle 打）。 */
    const val RELOAD_TRIGGERED: String = "已触发强制重拉有效配置（reload），稍后在控制台日志查看结果"

    /** reconnect 命令触发回显。 */
    const val RECONNECT_TRIGGERED: String = "已触发重新接入控制面（reconnect），保留当前有效配置"

    /** resync 命令触发回显（动作本身异步执行，日志由 lifecycle 打）。 */
    const val RESYNC_TRIGGERED: String = "已触发文件树重新同步（resync），稍后在控制台日志查看结果"

    /** resync 在文件树子系统未启用时的回显（config.yml 未开 file-tree.enabled）。 */
    const val RESYNC_DISABLED: String = "文件树子系统未启用，resync 不可用（请在 config.yml 开启 file-tree.enabled）"

    /**
     * 按文件树子系统是否已触发选取 resync 回显文案。
     *
     * @param triggered AgentLifecycle.forceSyncFileTreeNow 的返回：true=已触发同步，false=子系统未启用
     */
    fun resyncReply(triggered: Boolean): String = if (triggered) RESYNC_TRIGGERED else RESYNC_DISABLED

    /** 根命令无子命令时的用法提示（首行 + 各子命令一行用法）。 */
    val USAGE_LINES: List<String> = buildList {
        add(USAGE_HEADER)
        SUBCOMMANDS.forEach { add(subcommandLine(it)) }
    }

    /** help 子命令的完整帮助（含权限提示的标题 + 用法首行 + 各子命令用法）。 */
    val HELP_LINES: List<String> = buildList {
        add("Beacon agent 运维命令（仅本地，权限 beacon.admin）：")
        add(USAGE_HEADER)
        SUBCOMMANDS.forEach { add(subcommandLine(it)) }
    }

    /**
     * 无参 / 错参 / 未知子命令时的友好提示：先点明输入有误（错参时带回显），再给完整用法。
     *
     * @param input 用户实际输入的子命令片段；null / 空表示无参（仅给用法、不报「未知」）
     */
    fun incorrectInputLines(input: String?): List<String> = buildList {
        if (!input.isNullOrBlank()) {
            add("未知子命令：$input")
        }
        addAll(USAGE_LINES)
    }
}

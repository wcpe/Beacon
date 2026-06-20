package top.wcpe.beacon.agent.core.lifecycle

/**
 * 运维命令的纯文本与回显构造（无平台依赖，守 ADR-0005）。
 *
 * 壳层 status/reload/reconnect/resync 命令只负责把 sender 与 lifecycle 接起来，
 * 文案与「调哪个动作」的拼装收敛在此，两端壳共用、杜绝复制粘贴。
 */
object OpsCommandText {

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

    /** 根命令无子命令时的用法提示。 */
    val USAGE_LINES: List<String> = listOf(
        "用法：/beacon <status|reload|reconnect|resync>",
        "  status    查看 agent 接入与有效配置状态",
        "  reload    强制立刻重拉有效配置并 apply（不等长轮询超时）",
        "  reconnect 打断退避、重置并重新接入控制面",
        "  resync    强制立刻重同步文件树（需开启 file-tree.enabled）",
    )
}

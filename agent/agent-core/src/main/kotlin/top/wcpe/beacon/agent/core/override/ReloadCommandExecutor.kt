package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.platform.PlatformAdapter

/**
 * 受限重载命令执行器（ADR-0011 决策 3 / 6）。
 *
 * 把「控制面下发的重载命令」经 **agent 本地白名单**（[CommandWhitelist]）校验后，交平台适配派发为
 * 控制台命令。物理边界：派发只能走 [PlatformAdapter.dispatchConsoleCommand]，core 与适配器无任何
 * 进程 / shell 执行 API，落不到 OS shell。
 *
 * 派发**经 adapter.runAsync 异步进行，不在 MC 主线程同步等结果**（很多插件 reload 主线程阻塞）。
 *
 * **回滚绝不调用本执行器**（命令本身可能就是失败根因，见 ADR-0011 决策 5）——重放禁令由调用方保证：
 * 回滚路径只走 [BackupManager.restore] 还原文件，不触碰本类。
 */
class ReloadCommandExecutor(
    private val whitelist: CommandWhitelist,
    private val adapter: PlatformAdapter,
) {

    /**
     * 校验并派发一条重载命令。
     *
     * @return true 表示已通过白名单校验并提交异步派发；false 表示被拒（空 / 越白名单 / 含注入字符），未派发。
     */
    fun execute(command: String): Boolean {
        if (!whitelist.isAllowed(command)) {
            if (whitelist.isEmpty()) {
                adapter.warn("重载命令被拒：本地白名单为空（默认），不派发任何命令：$command")
            } else {
                adapter.warn("重载命令被拒：越本地白名单或含注入字符，不派发：$command")
            }
            return false
        }
        // 不在主线程同步等结果：提交异步派发即返回。
        adapter.runAsync {
            adapter.info("派发受限重载命令：$command")
            adapter.dispatchConsoleCommand(command)
        }
        return true
    }
}

package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.command.PluginsTreeReader
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.submit
import taboolib.common.platform.function.submitAsync
import java.io.File
import taboolib.common.platform.function.info as tabooInfo
import taboolib.common.platform.function.severe as tabooSevere
import taboolib.common.platform.function.warning as tabooWarning

/**
 * BungeeCord 平台适配：调度走 TabooLib submit / submitAsync，事件派发走 API 监听器回调。
 *
 * 所有 HTTP / 文件 IO 经 runAsync / runAsyncDelayed 落异步线程，绝不阻塞主线程。
 */
class BungeePlatformAdapter(
    private val effectiveConfigView: EffectiveConfigView,
) : PlatformAdapter {

    override fun runAsync(task: () -> Unit) {
        submitAsync { task() }
    }

    override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) {
        // TabooLib 调度延迟单位为 tick（20 tick/秒）；ms→tick 取整，至少 1 tick。
        val ticks = (delayMs / 50).coerceAtLeast(1)
        submit(async = true, delay = ticks) { task() }
    }

    override fun runSync(task: () -> Unit) {
        // 代理端无 tick 主线程概念，TabooLib 统一抽象为非异步提交即可。
        submit(async = false) { task() }
    }

    override fun dataFolder(): File = getDataFolder()

    override fun readPluginsTree(): Map<String, ByteArray> {
        // 反向抓取（FR-39）：读真实 plugins 根（dataFolder 的父目录）整棵子树为相对路径→原始字节。
        // 委托 core 的 PluginsTreeReader 做 FS 级路径安全（Path 容纳 + 符号链接逃逸判定）；
        // 由 lifecycle 在 async 线程触发（代理端无 tick 主线程，仍走 async 不阻塞），文本/二进制判别与上限交 core 纯函数。
        return PluginsTreeReader.read(pluginsBaseFolder())
    }

    override fun readPluginsTreeMetadata(): Map<String, Long> {
        // 反向抓取 scan 阶段（FR-58）：只 stat 取真实 plugins 树各文件大小（不读内容、永不失败）。
        // 委托 core 的 PluginsTreeReader.readMetadata 做同样的 FS 级路径安全；由 lifecycle 在 async 线程触发（不阻塞）。
        return PluginsTreeReader.readMetadata(pluginsBaseFolder())
    }

    override fun publishConfigChanged(changed: Set<String>, newMd5: String) {
        // MVP：经 API 监听器派发（业务插件通过 EffectiveConfig.onChange 订阅）。
        effectiveConfigView.fireChanged(changed, newMd5)
    }

    override fun dispatchConsoleCommand(command: String) {
        // BungeeCord 经 PluginManager 派发控制台命令；代理端无 tick 主线程概念，直接派发、不收集结果
        // （core 与本类均无 Runtime.exec/ProcessBuilder，物理上落不到 OS shell，ADR-0011 决策 2/6）。
        val proxy = ProxyServer.getInstance()
        proxy.pluginManager.dispatchCommand(proxy.console, command)
    }

    override fun info(msg: String) = tabooInfo(msg)

    override fun warn(msg: String) = tabooWarning(msg)

    override fun error(msg: String, t: Throwable?) {
        if (t != null) {
            tabooSevere("$msg：${t.message}")
        } else {
            tabooSevere(msg)
        }
    }
}

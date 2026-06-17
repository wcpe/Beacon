package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
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

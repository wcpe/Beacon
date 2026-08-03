package top.wcpe.beacon.agent.bukkit

import taboolib.common.platform.function.console
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.submit
import taboolib.common.platform.function.submitAsync
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import java.io.File
import taboolib.common.platform.function.info as tabooInfo
import taboolib.common.platform.function.severe as tabooSevere
import taboolib.common.platform.function.warning as tabooWarning

/**
 * Bukkit 平台适配：调度走 TabooLib submit / submitAsync，事件派发走 API 监听器回调。
 *
 * 所有 HTTP / 文件 IO 经 runAsync / runAsyncDelayed 落异步线程，绝不阻塞主线程。
 * 文件系统浏览与插件树读取（6 个 override）抽取到 [BukkitFsAdapter] 基类。
 */
class BukkitPlatformAdapter(
    private val effectiveConfigView: EffectiveConfigView,
) : BukkitFsAdapter() {
    override fun runAsync(task: () -> Unit) {
        submitAsync { task() }
    }

    override fun runAsyncDelayed(
        delayMs: Long,
        task: () -> Unit,
    ) {
        // TabooLib 调度延迟单位为 tick（20 tick/秒）；ms→tick 取整，至少 1 tick。
        val ticks = (delayMs / 50).coerceAtLeast(1)
        submit(async = true, delay = ticks) { task() }
    }

    override fun runSync(task: () -> Unit) {
        submit(async = false) { task() }
    }

    override fun dataFolder(): File = getDataFolder()

    override fun publishConfigChanged(
        changed: Set<String>,
        newMd5: String,
    ) {
        // MVP：经 API 监听器派发（业务插件通过 EffectiveConfig.onChange 订阅）。
        effectiveConfigView.fireChanged(changed, newMd5)
    }

    override fun dispatchConsoleCommand(command: String) {
        // Bukkit 命令派发须在主线程；切回主线程经 TabooLib 跨平台控制台执行命令，但不收集 / 不等待结果
        // （ADR-0011 决策 6 选项二：显式接受重载命令可能造成主线程卡顿；core 与本类均无 Runtime.exec/ProcessBuilder）。
        submit(async = false) {
            console().performCommand(command)
        }
    }

    override fun gracefulShutdown(reason: String) {
        // restart 生效（FR-171，见 ADR-0070）：切主线程广播关服提示 + 全 world save-all + Bukkit.shutdown()，
        // 存档落盘后再停避免丢档；进程重启交宿主自启脚本，本类无 Runtime.exec/ProcessBuilder（ADR-0011 决策 2 铁律）。
        // 经反射调 Bukkit 导出 API（本模块一贯不硬链 org.bukkit，见 BukkitTickInstrumentation）——反射目标均为
        // 导出接口 / API 类（Bukkit / World），不碰 CraftBukkit 实现类，规避 JPMS 封装拦截。
        submit(async = false) {
            val bukkit = Class.forName("org.bukkit.Bukkit")
            bukkit.getMethod("broadcastMessage", String::class.java)
                .invoke(null, "§e[Beacon] 服务器即将重启以生效交付变更：$reason")
            // save-all：先存玩家数据，再逐 world 存档（经 World 接口反射，不碰 CraftWorld 实现类）。
            bukkit.getMethod("savePlayers").invoke(null)
            val saveWorld = Class.forName("org.bukkit.World").getMethod("save")
            (bukkit.getMethod("getWorlds").invoke(null) as List<*>).forEach { world ->
                if (world != null) saveWorld.invoke(world)
            }
            bukkit.getMethod("shutdown").invoke(null)
        }
    }

    override fun info(msg: String) = tabooInfo(msg)

    override fun warn(msg: String) = tabooWarning(msg)

    override fun error(
        msg: String,
        t: Throwable?,
    ) {
        if (t != null) {
            tabooSevere("$msg：${t.message}")
        } else {
            tabooSevere(msg)
        }
    }
}

package top.wcpe.beacon.agent.bukkit

import taboolib.common.platform.function.console
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.submit
import taboolib.common.platform.function.submitAsync
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.browse.AssetContent
import top.wcpe.beacon.agent.core.browse.DirListing
import top.wcpe.beacon.agent.core.browse.FileContent
import top.wcpe.beacon.agent.core.browse.FsBrowseReader
import top.wcpe.beacon.agent.core.browse.TreeNode
import top.wcpe.beacon.agent.core.command.PluginsTreeReader
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import taboolib.common.platform.function.info as tabooInfo
import taboolib.common.platform.function.severe as tabooSevere
import taboolib.common.platform.function.warning as tabooWarning

/**
 * Bukkit 平台适配：调度走 TabooLib submit / submitAsync，事件派发走 API 监听器回调。
 *
 * 所有 HTTP / 文件 IO 经 runAsync / runAsyncDelayed 落异步线程，绝不阻塞主线程。
 */
class BukkitPlatformAdapter(
    private val effectiveConfigView: EffectiveConfigView,
) : PlatformAdapter {
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

    override fun readPluginsTree(): Map<String, ByteArray> {
        // 反向抓取（FR-39）：读真实 plugins 根（dataFolder 的父目录）整棵子树为相对路径→原始字节。
        // 委托 core 的 PluginsTreeReader 做 FS 级路径安全（Path 容纳 + 符号链接逃逸判定）；
        // 由 lifecycle 在 async 线程触发（绝不上主线程），文本/二进制判别与上限交 core 纯函数。
        return PluginsTreeReader.read(pluginsBaseFolder())
    }

    override fun readPluginsTreeMetadata(): Map<String, Long> {
        // 反向抓取 scan 阶段（FR-58）：只 stat 取真实 plugins 树各文件大小（不读内容、永不失败）。
        // 委托 core 的 PluginsTreeReader.readMetadata 做同样的 FS 级路径安全；由 lifecycle 在 async 线程触发（绝不上主线程）。
        return PluginsTreeReader.readMetadata(pluginsBaseFolder())
    }

    override fun browseListDir(
        relPath: String,
        offset: Int,
        limit: Int,
    ): DirListing? {
        // 只读浏览（FR-109）：懒列真实 plugins 根下目录直接子项，分页。委托 core FsBrowseReader 做
        // path traversal + 符号链接逃逸校验；由 FR-110 命令在 async 线程触发（绝不上主线程）。
        return FsBrowseReader.listDir(pluginsBaseFolder(), relPath, offset, limit)
    }

    override fun browseReadTree(
        relPath: String,
        maxDepth: Int,
    ): TreeNode? {
        // 只读浏览（FR-109）：按需展开子树，逐层有界。委托 core，安全口径同 browseListDir，async 触发。
        return FsBrowseReader.readTree(pluginsBaseFolder(), relPath, maxDepth)
    }

    override fun browseReadFile(relPath: String): FileContent? {
        // 只读浏览（FR-109）：读单文本文件内容，受单文件上限、排除 jar/二进制。委托 core，async 触发。
        return FsBrowseReader.readFile(pluginsBaseFolder(), relPath)
    }

    override fun browseReadAsset(
        relPath: String,
        maxBytes: Int,
    ): AssetContent? {
        // 文件资产预览（FR-164）：读单文件内容，二进制回元数据、文本可预览、超限截断。委托 core，async 触发。
        // 根须与 FR-163 扫描根一致（服务器工作目录 = pluginsBaseFolder 的父目录，见 AgentAssembly 装配 AssetScanScope）；
        // 否则 preview 传来的清单 path（plugins/xxx、根配置如 bukkit.yml）会拼错根致读失败。
        return FsBrowseReader.readAsset(pluginsBaseFolder().parentFile ?: pluginsBaseFolder(), relPath, maxBytes)
    }

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

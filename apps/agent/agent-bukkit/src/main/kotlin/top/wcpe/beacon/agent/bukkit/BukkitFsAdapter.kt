package top.wcpe.beacon.agent.bukkit

import top.wcpe.beacon.agent.core.browse.AssetContent
import top.wcpe.beacon.agent.core.browse.DirListing
import top.wcpe.beacon.agent.core.browse.FileContent
import top.wcpe.beacon.agent.core.browse.FsBrowseReader
import top.wcpe.beacon.agent.core.browse.TreeNode
import top.wcpe.beacon.agent.core.command.PluginsTreeReader
import top.wcpe.beacon.agent.core.platform.PlatformAdapter

/**
 * Bukkit 平台文件系统浏览与插件树读取的共享基类（FR-39 / FR-58 / FR-109 / FR-164）。
 *
 * 从 [BukkitPlatformAdapter] 抽取的 6 个文件系统相关 override，均委托 core 的 FsBrowseReader / PluginsTreeReader
 * 做 FS 级路径安全（Path 容纳 + 符号链接逃逸判定）；由 lifecycle 在 async 线程触发（绝不上主线程）。
 * 抽取为独立基类以控制 [BukkitPlatformAdapter] 的函数数量（TooManyFunctions）。
 */
abstract class BukkitFsAdapter : PlatformAdapter {
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
}

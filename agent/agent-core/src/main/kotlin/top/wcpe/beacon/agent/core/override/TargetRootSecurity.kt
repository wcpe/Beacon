package top.wcpe.beacon.agent.core.override

import java.io.File
import java.nio.file.Path

/**
 * 覆盖集目标根 [OverrideSetEntry.targetRoot] 的 agent 侧最终校验（ADR-0011 决策 4，agent 为最终权威）。
 *
 * 控制面发布时已早校验 target_root，但**控制面可能被攻破下发恶意 targetRoot**（如 `../../etc` 逃逸 plugins）。
 * 本类对每条投递的 targetRoot 独立再校验：限定 `plugins/<plugin>/` 内、Path 级落在 plugins 基目录下，
 * 不合规则**整集拒绝落盘**——把"控制面被攻破"的最坏后果挡在 agent 物理边界。
 *
 * 与成员 path 校验（[OverridePathSecurity]）分工：本类管"集落在哪个根"，那类管"成员落在根内何处"。
 *
 * @param pluginsBaseFolder 插件 plugins 基目录（targetRoot 必须解析后仍在其下，且至少一级插件子目录）
 */
class TargetRootSecurity(
    pluginsBaseFolder: File,
) {

    // plugins 基目录的规范化绝对 Path（比较基准）。
    private val pluginsPath: Path = pluginsBaseFolder.toPath().toAbsolutePath().normalize()

    // 服务器根（plugins 的父级）：targetRoot 形如 plugins/<plugin>，相对它解析。
    private val serverRootPath: Path = (pluginsBaseFolder.parentFile ?: pluginsBaseFolder)
        .toPath().toAbsolutePath().normalize()

    /**
     * 判断 targetRoot（相对服务器根，形如 plugins/AllinCore）是否可安全作为覆盖集落盘根。
     */
    fun isSafe(targetRoot: String): Boolean {
        // 注意：不整体 trim——段尾点 / 空格正是 Windows 落盘会剥离的绕过手段，须逐段拒，不能先 trim 掉。
        val raw = targetRoot
        if (raw.isBlank()) return false
        // 反斜杠 / 冒号（盘符 / ADS）规范化前即拒，避免平台差异绕过。
        if (raw.contains('\\') || raw.contains(':')) return false
        if (raw.startsWith('/')) return false // 绝对路径

        val segments = raw.trimEnd('/').split('/')
        // 必须以 plugins 开头且至少一级插件子目录（不止于 plugins 根本身）。
        if (segments.size < 2 || segments[0].lowercase() != "plugins") return false
        for (seg in segments) {
            if (seg == "..") return false // 任一段穿越即拒（双保险，下方 Path 级再兜底）
            // 段尾的点 / 空格会被 Windows 落盘剥离，借此绕过判定，一律拒。
            if (seg != "." && seg.trimEnd(' ', '.') != seg) return false
            if (isWindowsReserved(seg)) return false
        }

        // Path 级最终判定：解析到服务器根下后规范化，必须落在 plugins 基目录之内（且深于 plugins 本身）。
        val resolved = serverRootPath.resolve(raw.trimEnd('/')).normalize()
        return resolved.startsWith(pluginsPath) && resolved != pluginsPath
    }

    /** 段名是否为 Windows 保留设备名（取点号前主名，不区分大小写）。 */
    private fun isWindowsReserved(segment: String): Boolean {
        if (segment.isEmpty()) return false
        val base = segment.substringBefore('.').lowercase()
        return base in RESERVED_NAMES
    }

    companion object {
        /** Windows 保留设备名。 */
        private val RESERVED_NAMES: Set<String> = buildSet {
            addAll(listOf("con", "prn", "aux", "nul"))
            for (i in 1..9) {
                add("com$i")
                add("lpt$i")
            }
        }
    }
}

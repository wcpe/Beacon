package top.wcpe.beacon.agent.core.override

import java.io.File
import java.nio.file.Path

/**
 * 覆盖落盘路径 Path 级安全校验（ADR-0011 决策 4，agent 为最终权威）。
 *
 * 与 FR-14 的字符串级 [top.wcpe.beacon.agent.core.filetree.RelativePathGuard] 不同，本类用
 * `Path.normalize().startsWith(rootPath)`（Path 级，非字符串前缀）确保解析后仍落在目标根内，
 * 杜绝符号穿越与前缀伪装（如 `/rootevil` 字符串前缀命中但实际逃逸）。
 *
 * 额外拒绝（覆盖三方插件目录的高风险面）：
 * - `..` / 绝对路径 / 盘符 / UNC / 反斜杠 / 冒号（盘符 / Windows ADS）；
 * - 任一段为 Windows 保留设备名（不区分大小写）；
 * - `.jar` 后缀（防越界进 P3 发布编排）与 server 关键文件（server.properties / *.yml 引导 / eula.txt）。
 *
 * @param root 覆盖集目标根目录（如 plugins/AllinCore 的绝对 File）
 */
class OverridePathSecurity(
    root: File,
) {

    // 目标根的规范化绝对 Path（比较基准）。
    private val rootPath: Path = root.toPath().toAbsolutePath().normalize()

    /**
     * 判断相对 path 是否可安全落盘到目标根内。
     */
    fun isSafe(relativePath: String): Boolean {
        val raw = relativePath.trim()
        if (raw.isEmpty()) return false
        // 反斜杠 / 冒号（盘符 / ADS）规范化前即拒，避免平台差异绕过。
        if (raw.contains('\\') || raw.contains(':')) return false
        if (raw.startsWith('/')) return false // 绝对路径

        val segments = raw.split('/')
        for (seg in segments) {
            if (seg == "..") return false // 任一段穿越即拒（双保险，下方 Path 级再兜底）
            // 段尾的点 / 空格会被 Windows 落盘剥离（"x.jar."→"x.jar"、"con "→"con"），借此绕过禁覆盖判定，一律拒。
            if (seg != "." && seg.trimEnd(' ', '.') != seg) return false
            if (isWindowsReserved(seg)) return false
        }
        if (isForbiddenTarget(raw)) return false

        // Path 级最终判定：解析到目标根下后规范化，必须仍以目标根为前缀。
        val resolved = rootPath.resolve(raw).normalize()
        return resolved.startsWith(rootPath) && resolved != rootPath
    }

    /** 段名是否为 Windows 保留设备名（取点号前主名，不区分大小写）。 */
    private fun isWindowsReserved(segment: String): Boolean {
        if (segment.isEmpty()) return false
        val base = segment.substringBefore('.').lowercase()
        return base in RESERVED_NAMES
    }

    /** 是否为禁止覆盖的目标：.jar 后缀或 server 关键文件（按规范化小写比较）。 */
    private fun isForbiddenTarget(relativePath: String): Boolean {
        val lower = relativePath.lowercase()
        if (lower.endsWith(".jar")) return true
        return lower in FORBIDDEN_FILES
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

        /** 禁覆盖的 server 关键文件（相对根的精确小写名）。 */
        private val FORBIDDEN_FILES: Set<String> = setOf(
            "server.properties",
            "bukkit.yml",
            "spigot.yml",
            "paper.yml",
            "eula.txt",
        )
    }
}

package top.wcpe.beacon.agent.core.command

/**
 * 反向抓取相对路径的字符串级安全校验（FR-39，见 ADR-0027）。纯函数、无 FS 依赖，便于穷举单测。
 *
 * 与 FR-14/FR-15 落盘侧的 [top.wcpe.beacon.agent.core.override.OverridePathSecurity] 字符串段口径一致
 * （只是方向相反：那边管「能否落到根内」，这边管「读上来的相对路径是否合法、能否安全回传给控制面再落盘」）。
 *
 * 拒绝（沿 ADR-0011 / ADR-0027 路径口径）：
 * - 空 / 绝对（以 `/` 开头）/ 反斜杠 `\` / 冒号 `:`（盘符 / Windows ADS / UNC 残形）；
 * - 任一段为 `..`（穿越）；
 * - 段尾的点 / 空格（Windows 落盘会被剥离，借此绕过禁覆盖判定）；
 * - 任一段为 Windows 保留设备名（不区分大小写，取点号前主名）。
 *
 * 注意：真实 plugins 根内的 Path 级 `normalize().startsWith` 与符号链接逃逸判定，须在平台壳读盘时用
 * 真实根目录做（无法纯函数化）；本类是字符串级前置闸，与壳侧 Path 级判定双保险。
 */
object PluginsPathGuard {

    /** 判断相对路径是否安全（可纳入反向抓取回传）。 */
    fun isSafe(path: String): Boolean {
        if (path.isEmpty()) return false
        // 反斜杠 / 冒号（盘符 / ADS / UNC）规范化前即拒，避免平台差异绕过。
        if (path.contains('\\') || path.contains(':')) return false
        if (path.startsWith('/')) return false // 绝对路径

        val segments = path.split('/')
        for (seg in segments) {
            if (seg.isEmpty()) return false // 连续斜杠 / 末尾斜杠产生空段，拒
            if (seg == "..") return false // 任一段穿越即拒
            // 段尾的点 / 空格会被 Windows 落盘剥离（"x.jar."→"x.jar"、"con "→"con"），借此绕过判定，一律拒。
            if (seg != "." && seg.trimEnd(' ', '.') != seg) return false
            if (isWindowsReserved(seg)) return false
        }
        return true
    }

    /** 段名是否为 Windows 保留设备名（取点号前主名，不区分大小写）。 */
    private fun isWindowsReserved(segment: String): Boolean {
        if (segment.isEmpty()) return false
        val base = segment.substringBefore('.').lowercase()
        return base in RESERVED_NAMES
    }

    /** Windows 保留设备名。 */
    private val RESERVED_NAMES: Set<String> = buildSet {
        addAll(listOf("con", "prn", "aux", "nul"))
        for (i in 1..9) {
            add("com$i")
            add("lpt$i")
        }
    }
}

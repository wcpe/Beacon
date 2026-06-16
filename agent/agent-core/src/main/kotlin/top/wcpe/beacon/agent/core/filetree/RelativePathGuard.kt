package top.wcpe.beacon.agent.core.filetree

/**
 * 落盘相对路径安全校验：只允许落在目标根内，拒绝绝对路径 / `..` 穿越 / 反斜杠，杜绝目录逃逸。
 *
 * 纯函数，无副作用；与控制面 admin 侧 path 校验同口径（见 docs/API.md INVALID_PATH）。
 */
object RelativePathGuard {

    /**
     * 判断相对路径是否安全（可落盘到目标根内）。
     *
     * 拒绝：空 / 绝对路径（以 `/` 开头或含盘符如 `C:`）/ 含反斜杠 `\` / 任一段为 `..`。
     */
    fun isSafe(path: String): Boolean {
        if (path.isEmpty()) return false
        if (path.contains('\\')) return false
        if (path.startsWith('/')) return false
        // Windows 盘符（如 C:foo / C:/foo）：含冒号一律拒绝。
        if (path.contains(':')) return false
        // 逐段检查，任一段为 `..` 即拒绝（含开头 ../、中间 a/../b）。
        val segments = path.split('/')
        for (seg in segments) {
            if (seg == "..") return false
        }
        return true
    }
}

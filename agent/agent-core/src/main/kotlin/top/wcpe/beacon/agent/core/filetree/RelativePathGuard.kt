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

    /**
     * 判断相对路径的顶段是否落入"agent 自身 dataFolder"受保护集合。
     *
     * 用于 [FileTreeApplier]：当运维误把 `BeaconAgent/config.yml` 之类 agent 自管文件经
     * FR-14 文件树或 FR-38 导入塞进有效树时，applier 据此跳过该 path（不取、不写、不删），
     * 防止 agent 覆写自身配置 / 快照（与 FR-41 env 注入身份的设计相辅相成：env 兜身份事实，
     * 自我保护兜本地 dataFolder 不被外部接管）。
     *
     * 严格"顶段相等"匹配：`BeaconAgent/x` 命中 `BeaconAgent`，但 `BeaconAgentX/x` 不命中。
     * core 不硬编码 plugin 名；具体保护集合由壳层（bukkit / bungee）按自身 plugin 名注入。
     * 空集合视为未启用保护，永远返回 false（保留旧装配的兼容路径）。
     */
    fun isReservedSelfPath(path: String, reservedSegments: Set<String>): Boolean {
        if (reservedSegments.isEmpty() || path.isEmpty()) return false
        val top = path.substringBefore('/')
        return reservedSegments.contains(top)
    }
}

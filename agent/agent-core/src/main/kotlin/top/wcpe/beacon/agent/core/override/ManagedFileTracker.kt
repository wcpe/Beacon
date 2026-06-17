package top.wcpe.beacon.agent.core.override

import java.util.concurrent.ConcurrentHashMap

/**
 * 受管文件标记与外部改动检测（ADR-0011 决策 7，反馈环防护）。
 *
 * 三方插件常在启动 / 运行时重写自己的 config，会与 agent 覆盖形成
 * 「插件写 → agent 盖 → reload → 插件又写」的震荡环。
 *
 * 防护：agent 每次把某 path 落盘后，[markWritten] 记下它写入的 md5。下一轮覆盖前先用
 * [isExternallyModified] 比对磁盘现状——若磁盘 md5 与「agent 上次写入的 md5」不同，
 * 说明被外部改过，调用方应**告警而非盲盖**，由人工裁决，避免无限互覆盖。
 *
 * 仅记内存（agent 进程内），重启后基准重建——重启后首轮按无基准处理（交由正常落盘流程），不误报。
 * 线程安全（文件同步在异步线程）。
 */
class ManagedFileTracker {

    // path → agent 上次写入该 path 时的内容 md5。
    private val lastWrittenMd5 = ConcurrentHashMap<String, String>()

    /** 记录 agent 刚把 [path] 落盘为 md5=[writtenMd5] 的那一版（落盘成功后调用）。 */
    fun markWritten(path: String, writtenMd5: String) {
        lastWrittenMd5[path] = writtenMd5
    }

    /**
     * 判断 [path] 的磁盘现状是否被外部改过。
     *
     * 受管（agent 写过）且磁盘 md5 与上次写入的不同 → true（外部改动，应告警）。
     * 未受管（无基准）→ false（交由首次落盘处理，不误报）。
     */
    fun isExternallyModified(path: String, currentDiskMd5: String): Boolean {
        val baseline = lastWrittenMd5[path] ?: return false
        return baseline != currentDiskMd5
    }

    /** 移除某 path 的受管标记（path 从覆盖集移除 / 删除时）。 */
    fun forget(path: String) {
        lastWrittenMd5.remove(path)
    }
}

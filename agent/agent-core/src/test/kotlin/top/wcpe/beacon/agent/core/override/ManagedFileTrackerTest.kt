package top.wcpe.beacon.agent.core.override

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 受管文件标记 / 外部改动检测单测（ADR-0011 决策 7 反馈环防护）。
 *
 * agent 落盘后记下它写入的 md5。下一轮覆盖前若磁盘现状 md5 与「agent 上次写入的 md5」不同，
 * 说明被外部（插件自身重写 config）改过 → 应告警而非盲盖，打破「插件写→agent 盖→reload→插件又写」震荡环。
 */
class ManagedFileTrackerTest {

    @Test
    fun `agent 写入后磁盘未变 不算外部改动`() {
        val tracker = ManagedFileTracker()
        tracker.markWritten("config.yml", "md5-agent")
        // 磁盘现状仍是 agent 写的那一版。
        assertFalse(tracker.isExternallyModified("config.yml", currentDiskMd5 = "md5-agent"))
    }

    @Test
    fun `磁盘被外部改动 检测为外部修改`() {
        val tracker = ManagedFileTracker()
        tracker.markWritten("config.yml", "md5-agent")
        // 插件自身重写了 config，磁盘 md5 变了。
        assertTrue(tracker.isExternallyModified("config.yml", currentDiskMd5 = "md5-plugin-rewrote"))
    }

    @Test
    fun `未受管文件 不视为外部改动`() {
        val tracker = ManagedFileTracker()
        // 从未由 agent 写过的 path，无基准可比，不报外部改动（交由首次落盘处理）。
        assertFalse(tracker.isExternallyModified("never.yml", currentDiskMd5 = "whatever"))
    }

    @Test
    fun `重新标记后以最新写入为基准`() {
        val tracker = ManagedFileTracker()
        tracker.markWritten("config.yml", "v1")
        tracker.markWritten("config.yml", "v2")
        assertFalse(tracker.isExternallyModified("config.yml", currentDiskMd5 = "v2"))
        assertTrue(tracker.isExternallyModified("config.yml", currentDiskMd5 = "v1"))
    }
}

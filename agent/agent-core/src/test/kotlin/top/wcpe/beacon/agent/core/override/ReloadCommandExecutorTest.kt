package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 受限重载命令执行单测（ADR-0011 决策 3 / 6）：
 * - 命中本地白名单才派发；越白名单 / 注入字符一律拒绝且不派发；
 * - 派发经 adapter.runAsync（不在主线程同步等结果）；
 * - 空白名单（默认）下任何命令都不派发。
 */
class ReloadCommandExecutorTest {

    /** 记录派发的命令与是否经异步线程的测试桩适配器。 */
    private class RecordingAdapter : PlatformAdapter {
        val dispatched = mutableListOf<String>()
        var dispatchedViaAsync = false
        private var inAsync = false

        override fun runAsync(task: () -> Unit) {
            inAsync = true
            try {
                task()
            } finally {
                inAsync = false
            }
        }

        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = File(System.getProperty("java.io.tmpdir"))
        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun dispatchConsoleCommand(command: String) {
            if (inAsync) dispatchedViaAsync = true
            dispatched.add(command)
        }

        override fun info(msg: String) {}
        override fun warn(msg: String) {}
        override fun error(msg: String, t: Throwable?) {}
    }

    @Test
    fun `命中白名单的命令经异步派发`() {
        val adapter = RecordingAdapter()
        val exec = ReloadCommandExecutor(CommandWhitelist(setOf("allin")), adapter)
        val ok = exec.execute("allin reload")
        assertTrue(ok, "应派发成功")
        assertEquals(listOf("allin reload"), adapter.dispatched)
        assertTrue(adapter.dispatchedViaAsync, "派发须经异步线程，不在主线程同步等结果")
    }

    @Test
    fun `越白名单的命令拒绝且不派发`() {
        val adapter = RecordingAdapter()
        val exec = ReloadCommandExecutor(CommandWhitelist(setOf("allin")), adapter)
        val ok = exec.execute("lp sync")
        assertTrue(!ok, "越白名单应拒")
        assertTrue(adapter.dispatched.isEmpty(), "拒绝的命令不应派发")
    }

    @Test
    fun `注入字符命令拒绝且不派发`() {
        val adapter = RecordingAdapter()
        val exec = ReloadCommandExecutor(CommandWhitelist(setOf("allin")), adapter)
        assertTrue(!exec.execute("allin reload; rm -rf /"))
        assertTrue(adapter.dispatched.isEmpty())
    }

    @Test
    fun `空白名单默认 任何命令都不派发`() {
        val adapter = RecordingAdapter()
        val exec = ReloadCommandExecutor(CommandWhitelist(emptySet()), adapter)
        assertTrue(!exec.execute("allin reload"))
        assertTrue(adapter.dispatched.isEmpty())
    }

    @Test
    fun `空命令不派发`() {
        val adapter = RecordingAdapter()
        val exec = ReloadCommandExecutor(CommandWhitelist(setOf("allin")), adapter)
        assertTrue(!exec.execute(""))
        assertTrue(adapter.dispatched.isEmpty())
    }
}

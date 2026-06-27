package top.wcpe.beacon.agent.core.log

import top.wcpe.beacon.agent.core.browse.DirListing
import top.wcpe.beacon.agent.core.browse.FileContent
import top.wcpe.beacon.agent.core.browse.TreeNode
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull

/**
 * [BufferingPlatformAdapter] 委托回归（FR-109/110，见 ADR-0049）。
 *
 * 背景（真机暴露的缺陷）：日志缓冲装饰器曾**漏委托只读浏览 browse***——经装饰器时落到 [PlatformAdapter]
 * 默认 null 实现，壳层（Bukkit/Bungee）的真实委托永不可达，导致真机浏览全部返回 null（结果=false）。
 * 本测试包裹一个 browse* 返回已知值的桩，断言装饰器把三个 browse* 原样透传（不为 null）。
 */
class BufferingPlatformAdapterTest {

    // 桩：browse* 返回已知值，其余抽象能力空实现
    private class StubAdapter : PlatformAdapter {
        override fun info(msg: String) {}
        override fun warn(msg: String) {}
        override fun error(msg: String, t: Throwable?) {}
        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = File(System.getProperty("java.io.tmpdir"))
        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun dispatchConsoleCommand(command: String) {}
        override fun browseListDir(relPath: String, offset: Int, limit: Int): DirListing =
            DirListing(path = relPath, entries = emptyList(), offset = offset, limit = limit, total = 0, hasMore = false)
        override fun browseReadTree(relPath: String, maxDepth: Int): TreeNode =
            TreeNode(name = "plugins", relPath = relPath, dir = true, size = 0, text = false, children = emptyList(), truncated = false)
        override fun browseReadFile(relPath: String): FileContent =
            FileContent(path = relPath, content = "x", truncated = false)
    }

    @Test
    fun `browse 透传给被包裹 adapter 不落默认 null`() {
        val wrapped = BufferingPlatformAdapter(StubAdapter(), AgentLogBuffer(16))
        assertNotNull(wrapped.browseListDir("", 0, 50), "browseListDir 应透传被包裹 adapter")
        assertNotNull(wrapped.browseReadTree("", 3), "browseReadTree 应透传被包裹 adapter")
        assertNotNull(wrapped.browseReadFile("a.yml"), "browseReadFile 应透传被包裹 adapter")
        assertEquals("plugins", wrapped.browseReadTree("", 3)!!.name)
    }
}

package top.wcpe.beacon.agent.core.lifecycle

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** OpsCommandText 纯文本渲染单测：status 行包含关键字段、md5 缺失有兜底、帮助 / 错参提示（FR-54）。 */
class OpsCommandTextTest {

    @Test
    fun `status 渲染含状态 连接 md5 心跳与 endpoint`() {
        val lines = OpsCommandText.statusLines(
            LifecycleSnapshot(
                state = AgentState.RUNNING,
                connected = true,
                effectiveMd5 = "abc123",
                heartbeatIntervalSec = 10,
                endpoint = "http://localhost:8848",
            ),
        )
        val text = lines.joinToString("\n")
        assertTrue(text.contains("RUNNING"))
        assertTrue(text.contains("是"))
        assertTrue(text.contains("abc123"))
        assertTrue(text.contains("10s"))
        assertTrue(text.contains("http://localhost:8848"))
    }

    @Test
    fun `resync 已触发时回真触发文案而非占位`() {
        val reply = OpsCommandText.resyncReply(triggered = true)
        assertTrue(reply.contains("已触发"), "resync 已触发应回真触发文案")
        assertTrue(reply.contains("文件树"), "文案应点明文件树同步")
        assertTrue(!reply.contains("未启用"), "已触发不得再回未启用占位")
    }

    @Test
    fun `resync 子系统未启用时回未启用文案`() {
        val reply = OpsCommandText.resyncReply(triggered = false)
        assertTrue(reply.contains("未启用"), "子系统未启用应回未启用文案")
    }

    @Test
    fun `md5 缺失时显示兜底文案`() {
        val lines = OpsCommandText.statusLines(
            LifecycleSnapshot(
                state = AgentState.DEGRADED,
                connected = false,
                effectiveMd5 = null,
                heartbeatIntervalSec = 0,
                endpoint = "http://x",
            ),
        )
        assertTrue(lines.any { it.contains("暂无") }, "md5 缺失应有兜底文案")
        assertTrue(lines.any { it.contains("否") }, "未连应显示否")
    }

    @Test
    fun `用法首行含根命令与全部子命令选项`() {
        val header = OpsCommandText.USAGE_HEADER
        assertTrue(header.contains("/beacon"), "用法首行应含根命令 /beacon")
        OpsCommandText.SUBCOMMANDS.forEach { sub ->
            assertTrue(header.contains(sub.name), "用法首行的选项集应含子命令 ${sub.name}")
        }
    }

    @Test
    fun `无参用法逐行覆盖每个子命令及其说明`() {
        val lines = OpsCommandText.USAGE_LINES
        assertEquals(OpsCommandText.USAGE_HEADER, lines.first(), "首行应为用法首行")
        OpsCommandText.SUBCOMMANDS.forEach { sub ->
            assertTrue(
                lines.any { it.contains(sub.name) && it.contains(sub.usage) },
                "用法应有一行同时含 ${sub.name} 及其说明",
            )
        }
    }

    @Test
    fun `help 帮助含权限提示与全部子命令`() {
        val lines = OpsCommandText.HELP_LINES
        assertTrue(lines.any { it.contains("beacon.admin") }, "help 应点明所需权限 beacon.admin")
        OpsCommandText.SUBCOMMANDS.forEach { sub ->
            assertTrue(lines.any { it.contains(sub.name) }, "help 应覆盖子命令 ${sub.name}")
        }
    }

    @Test
    fun `子命令清单与壳层注册的命令集一致`() {
        // 防漂移：core 文案清单须与双端壳注册的 literal 子命令完全对齐（顺序无关）。
        val expected = setOf("status", "reload", "reconnect", "resync", "help")
        assertEquals(expected, OpsCommandText.SUBCOMMANDS.map { it.name }.toSet())
    }

    @Test
    fun `错参提示带未知片段回显并附完整用法`() {
        val lines = OpsCommandText.incorrectInputLines("foo")
        assertTrue(lines.any { it.contains("未知子命令") && it.contains("foo") }, "应回显未知子命令片段")
        // 附带的完整用法（USAGE_LINES 全部行）应在其后。
        assertTrue(lines.containsAll(OpsCommandText.USAGE_LINES), "错参提示应附完整用法")
    }

    @Test
    fun `无参时提示只给用法不报未知子命令`() {
        val nullInput = OpsCommandText.incorrectInputLines(null)
        val blankInput = OpsCommandText.incorrectInputLines("  ")
        assertEquals(OpsCommandText.USAGE_LINES, nullInput, "无参应只给用法")
        assertEquals(OpsCommandText.USAGE_LINES, blankInput, "空白参同样只给用法")
        assertFalse(nullInput.any { it.contains("未知子命令") }, "无参不应报未知子命令")
    }
}

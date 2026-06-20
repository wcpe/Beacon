package top.wcpe.beacon.agent.core.lifecycle

import kotlin.test.Test
import kotlin.test.assertTrue

/** OpsCommandText 纯文本渲染单测：status 行包含关键字段、md5 缺失有兜底。 */
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
}

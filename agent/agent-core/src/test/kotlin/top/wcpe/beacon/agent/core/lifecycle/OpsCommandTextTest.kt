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

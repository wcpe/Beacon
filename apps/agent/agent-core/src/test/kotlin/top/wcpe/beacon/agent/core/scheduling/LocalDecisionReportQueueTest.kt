package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.LocalDecisionReport
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/** [LocalDecisionReportQueue] 单测（FR-148 §4.6）：有界容量、满丢最旧、drain 清空。 */
class LocalDecisionReportQueueTest {
    private fun report(id: String): LocalDecisionReport =
        LocalDecisionReport(
            localTraceId = id,
            tsMs = 1L,
            zone = "z-a",
            plugin = null,
            purpose = null,
            candidateCount = 1,
            excluded = emptyList(),
            chosenServerId = "lobby-1",
            failReason = null,
        )

    @Test
    fun `写满丢最旧且保留最新一批`() {
        val queue = LocalDecisionReportQueue(capacity = 3)
        queue.offer(report("a"))
        queue.offer(report("b"))
        queue.offer(report("c"))
        // 第 4 条入队应挤掉最旧的 a。
        queue.offer(report("d"))

        assertEquals(3, queue.size())
        val drained = queue.drain()
        assertEquals(listOf("b", "c", "d"), drained.map { it.localTraceId }, "满丢最旧：a 被挤出，保留 b/c/d")
    }

    @Test
    fun `drain 取走并清空`() {
        val queue = LocalDecisionReportQueue(capacity = 8)
        queue.offer(report("x"))
        queue.offer(report("y"))
        val drained = queue.drain()
        assertEquals(listOf("x", "y"), drained.map { it.localTraceId })
        assertEquals(0, queue.size(), "drain 后应清空")
        assertTrue(queue.drain().isEmpty(), "空队列 drain 返回空表")
    }

    @Test
    fun `localTraceId 幂等键各不相同`() {
        // 补报幂等以 localTraceId 为键：不同决策必须不同 id（由 newLocalTraceId 生成，此处验证 UUID 唯一）。
        val ids = (1..100).map { newLocalTraceId() }.toSet()
        assertEquals(100, ids.size, "本地 traceId 应各不相同")
    }
}

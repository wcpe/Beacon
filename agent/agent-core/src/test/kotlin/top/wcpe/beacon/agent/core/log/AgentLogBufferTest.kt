package top.wcpe.beacon.agent.core.log

import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * agent 自身日志环形缓冲 [AgentLogBuffer] 单测（FR-88，见 ADR-0040）：
 * - 有界：满则挤出最旧，容量恒定；
 * - 顺序：snapshot 从最旧到最新；
 * - 落缓冲即脱敏：敏感串入缓冲前已掩码；
 * - 线程安全：多线程并发 append 不丢行、不抛异常、不越界。
 */
class AgentLogBufferTest {

    @Test
    fun `未满时按追加顺序返回全部行`() {
        val buf = AgentLogBuffer(capacity = 5)
        buf.append("INFO", "第一行")
        buf.append("WARN", "第二行")
        val lines = buf.snapshot()
        assertEquals(2, lines.size)
        assertEquals("INFO", lines[0].level)
        assertEquals("第一行", lines[0].text)
        assertEquals("第二行", lines[1].text)
    }

    @Test
    fun `超容量时挤出最旧只保留最近 N 行`() {
        val buf = AgentLogBuffer(capacity = 3)
        for (i in 1..5) {
            buf.append("INFO", "行$i")
        }
        val lines = buf.snapshot()
        // 容量 3：只保留最近 3 行（行3/行4/行5），最旧的行1/行2被挤出。
        assertEquals(3, lines.size)
        assertEquals(listOf("行3", "行4", "行5"), lines.map { it.text })
    }

    @Test
    fun `落缓冲即脱敏——敏感串在 snapshot 中已被掩码`() {
        val buf = AgentLogBuffer(capacity = 10)
        buf.append("INFO", "bootstrap-token=super-secret-value-123")
        val text = buf.snapshot().single().text
        // 缓冲里存的就是脱敏后文本，绝不含原始敏感值。
        assertTrue(!text.contains("super-secret-value-123"), "敏感值不应出现在缓冲中：$text")
        assertTrue(text.contains("***"), "应有掩码标记：$text")
    }

    @Test
    fun `并发追加线程安全——不丢行不越界`() {
        val capacity = 1000
        val buf = AgentLogBuffer(capacity = capacity)
        val threads = 8
        val perThread = 500
        val pool = Executors.newFixedThreadPool(threads)
        val start = CountDownLatch(1)
        val done = CountDownLatch(threads)
        repeat(threads) { t ->
            pool.submit {
                start.await()
                for (i in 0 until perThread) {
                    buf.append("INFO", "t$t-i$i")
                }
                done.countDown()
            }
        }
        start.countDown()
        assertTrue(done.await(10, TimeUnit.SECONDS), "并发追加应在限时内完成")
        pool.shutdown()
        // 总写入 threads*perThread > capacity，缓冲恒定容量上限、不越界、不抛异常。
        val lines = buf.snapshot()
        assertEquals(capacity, lines.size)
    }
}

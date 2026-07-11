package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.SelfHealth
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** [SelfHealthHolder] 单测（FR-148）：刷新、null 保留上一次、接收时刻。 */
class SelfHealthHolderTest {
    @Test
    fun `初始为 null`() {
        assertNull(SelfHealthHolder().get())
    }

    @Test
    fun `set 刷新并记录接收时刻`() {
        val holder = SelfHealthHolder(now = { 5_000L })
        holder.set(SelfHealth(score = 80, level = "healthy", schedulable = true, reasons = emptyList()))
        val timed = holder.get()
        assertEquals(80, timed?.self?.score)
        assertEquals(5_000L, timed?.atMs)
    }

    @Test
    fun `set null 保留上一次已知值`() {
        val holder = SelfHealthHolder(now = { 1L })
        holder.set(SelfHealth(score = 60, level = "degraded", schedulable = true, reasons = listOf("x")))
        holder.set(null)
        assertEquals(60, holder.get()?.self?.score, "null 不应清空已知自身健康")
    }
}

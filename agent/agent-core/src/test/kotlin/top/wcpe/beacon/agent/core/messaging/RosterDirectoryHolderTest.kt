package top.wcpe.beacon.agent.core.messaging

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * RosterDirectoryHolder 单测（FR-31 / ADR-0022）：
 * 可选名册实现的可变持有者——默认空、set 后转发、reset 复位空。优雅降级：未注入即返空 Map、不抛。
 */
class RosterDirectoryHolderTest {

    private class FixedRoster(private val table: Map<String, String>) : RosterDirectory {
        override fun snapshot(): Map<String, String> = table
    }

    @Test
    fun `默认未注入时 snapshot 返空 Map`() {
        assertTrue(RosterDirectoryHolder().snapshot().isEmpty(), "默认（未 set）应返空 Map")
    }

    @Test
    fun `set 后 snapshot 转发到注入实现`() {
        val holder = RosterDirectoryHolder()
        holder.set(FixedRoster(mapOf("Alice" to "lobby-1")))
        assertEquals(mapOf("Alice" to "lobby-1"), holder.snapshot(), "set 后应转发到注入实现")
    }

    @Test
    fun `reset 后 snapshot 复位返空 Map`() {
        val holder = RosterDirectoryHolder()
        holder.set(FixedRoster(mapOf("Alice" to "lobby-1")))
        holder.reset()
        assertTrue(holder.snapshot().isEmpty(), "reset 后应复位为空")
    }

    @Test
    fun `注入实现抛异常时降级返空 Map 不外抛`() {
        val holder = RosterDirectoryHolder()
        holder.set(object : RosterDirectory {
            override fun snapshot(): Map<String, String> = throw IllegalStateException("名册读取失败")
        })
        assertTrue(holder.snapshot().isEmpty(), "实现抛异常时应降级返空、不外抛")
    }
}

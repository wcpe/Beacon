package top.wcpe.beacon.agent.core.proxy

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

class DirectorySyncLoopGateTest {
    @Test
    fun `同代重复注册不会启动第二条同步链`() {
        val gate = DirectorySyncLoopGate()
        var scheduled = 0

        val generation = assertNotNull(gate.start { scheduled++ })
        assertNull(gate.start { scheduled++ })

        assertTrue(gate.isCurrent(generation))
        assertEquals(1, scheduled)
    }

    @Test
    fun `停止再启动后旧代延迟回调失效且新代回调生效`() {
        val gate = DirectorySyncLoopGate()
        val delayedChecks = mutableListOf<() -> Boolean>()
        assertNotNull(gate.start { generation -> delayedChecks += { gate.isCurrent(generation) } })

        gate.stop()
        assertNotNull(gate.start { generation -> delayedChecks += { gate.isCurrent(generation) } })

        assertFalse(delayedChecks[0]())
        assertTrue(delayedChecks[1]())
    }
}

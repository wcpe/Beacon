package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.ScheduleState
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 未装配占位实现的口径单测（FR-244）。
 *
 * 锁三件事：
 * ① 取候选走"结论不可用"（当前状态、可重试）而不是"确实没有候选"（稳定事实），且不外抛；
 * ② 健康 / 数据来源同样降级返回，门面从未就绪也不外抛；
 * ③ 候选读侧的按作用域重载在**判不了**时如实抛 [IllegalStateException]——
 *    返回空表就是把"看不到"冒充"没有"，会让调用方永久放弃一次派房；空作用域不涉及判据，照 1 参重载。
 */
class UnavailableSchedulingTest {
    private val zone = "z-a"

    @Test
    fun `取候选立即以结论不可用正常完成`() {
        val scheduling = UnavailableScheduling

        val future = scheduling.acquireCandidate(zone, null)

        assertTrue(future.isDone, "占位实现必须立即完成，绝不阻塞玩家链路")
        val result = future.get()
        assertNull(result.chosen(), "没有任何候选缓存")
        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "「看不到」是当前状态，可重试")
        assertEquals("unavailable", result.failReason(), "与 no_candidate（稳定事实）区分开")
        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
    }

    @Test
    fun `带作用域取候选同样以结论不可用正常完成`() {
        val scheduling = UnavailableScheduling

        val result = scheduling.acquireCandidate(zone, "lobby-transfer", AdmissionScope.of("k", "v")).get()

        assertNull(result.chosen())
        assertEquals(
            ScheduleState.UNAVAILABLE,
            result.state(),
            "作用域非空也改变不了「看不到」这个事实，不得改抛异常或报成没有候选",
        )
        assertEquals("unavailable", result.failReason())
    }

    @Test
    fun `空作用域与非空作用域在取候选路径上结论一致`() {
        val scheduling = UnavailableScheduling

        val byEmpty = scheduling.acquireCandidate(zone, null, AdmissionScope.empty()).get()
        val byNull = scheduling.acquireCandidate(zone, null, null).get()

        assertEquals(ScheduleState.UNAVAILABLE, byEmpty.state())
        assertEquals(ScheduleState.UNAVAILABLE, byNull.state())
        assertEquals(byEmpty.failReason(), byNull.failReason())
    }

    @Test
    fun `空作用域读候选返回空表——判据不参与，等同不带作用域`() {
        val scheduling = UnavailableScheduling

        assertTrue(scheduling.candidatesInZone(zone).isEmpty())
        assertTrue(scheduling.candidatesInZone(zone, AdmissionScope.empty()).isEmpty())
        assertTrue(scheduling.candidatesInZone(zone, null).isEmpty())
    }

    @Test
    fun `非空作用域读候选如实抛判据不可用`() {
        val scheduling = UnavailableScheduling

        val failure =
            assertFailsWith<IllegalStateException> {
                scheduling.candidatesInZone(zone, AdmissionScope.of("k", "v"))
            }

        assertFalse(
            failure.message.isNullOrBlank(),
            "异常要带说明（可重试的当前状态 vs 稳定结论），不能是无说明的 ISE",
        )
    }

    @Test
    fun `健康与数据来源同样降级返回且不外抛`() {
        val scheduling = UnavailableScheduling

        assertNull(scheduling.healthOf("srv-1"))
        assertNull(scheduling.selfHealth())
        val dataSource = scheduling.dataSource()
        assertEquals(DataSource.LOCAL_SNAPSHOT, dataSource.source())
        assertFalse(dataSource.fresh(), "从未就绪的快照不可能是新鲜的")
    }
}

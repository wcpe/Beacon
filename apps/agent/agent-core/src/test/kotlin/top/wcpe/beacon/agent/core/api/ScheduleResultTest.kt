package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.api.ScheduleState
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull

/**
 * 决策结果三态一致性的单测（FR-244）。
 *
 * 锁三件事：
 * ① 三态"取值即事实"——CHOSEN 必带选中候选、NO_CANDIDATE / UNAVAILABLE 必不带，
 *    自相矛盾的组合在构造时就被拒（否则调用方按 state 与按 chosen 会读出两个打架的结论）；
 * ② 既有 4 参构造器行为逐位不变（按 chosen 兜底推导），且表达不出 UNAVAILABLE；
 * ③ 作用域排除台数负数归 0。
 */
class ScheduleResultTest {
    private val chosen = CandidateView("srv-1", "z-a", 90, HealthLevel.HEALTHY, 10, 200)

    @Test
    fun `结论为选中时必须带选中候选`() {
        assertFailsWith<IllegalArgumentException> {
            ScheduleResult(null, "trace-1", DecisionSource.CONTROL_PLANE, null, ScheduleState.CHOSEN)
        }
        assertFailsWith<IllegalArgumentException> {
            ScheduleResult(null, "trace-1", DecisionSource.CONTROL_PLANE, null, ScheduleState.CHOSEN, 3)
        }
    }

    @Test
    fun `结论为没有候选或不可用时不得带选中候选`() {
        assertFailsWith<IllegalArgumentException> {
            ScheduleResult(chosen, "trace-1", DecisionSource.CONTROL_PLANE, "no_candidate", ScheduleState.NO_CANDIDATE)
        }
        assertFailsWith<IllegalArgumentException> {
            ScheduleResult(chosen, "trace-1", DecisionSource.LOCAL_FALLBACK, "unavailable", ScheduleState.UNAVAILABLE)
        }
    }

    @Test
    fun `合法组合按原样取值`() {
        val ok =
            ScheduleResult(
                chosen,
                "trace-1",
                DecisionSource.CONTROL_PLANE,
                null,
                ScheduleState.CHOSEN,
                2,
            )

        assertEquals(ScheduleState.CHOSEN, ok.state())
        assertEquals("srv-1", ok.chosen()?.serverId())
        assertEquals(2, ok.admissionExcludedCount())

        val unavailable =
            ScheduleResult(null, "trace-2", DecisionSource.LOCAL_FALLBACK, "unavailable", ScheduleState.UNAVAILABLE)
        assertEquals(ScheduleState.UNAVAILABLE, unavailable.state())
        assertNull(unavailable.chosen())
    }

    @Test
    fun `四参构造器按选中与否推导结论且行为不变`() {
        val hit = ScheduleResult(chosen, "trace-1", DecisionSource.CONTROL_PLANE, null)
        assertEquals(ScheduleState.CHOSEN, hit.state())
        assertEquals(0, hit.admissionExcludedCount(), "不带排除台数时归 0")

        val miss = ScheduleResult(null, "trace-2", DecisionSource.LOCAL_FALLBACK, "no_candidate")
        assertEquals(ScheduleState.NO_CANDIDATE, miss.state())
        assertNull(miss.chosen())

        assertFailsWith<IllegalArgumentException> {
            // 既有构造器报"没做成"本来就是错的用法：它只会推出 CHOSEN / NO_CANDIDATE。
            ScheduleResult(null, "trace-3", DecisionSource.LOCAL_FALLBACK, null, ScheduleState.CHOSEN)
        }
    }

    @Test
    fun `state 为 null 时按选中与否兜底推导`() {
        val hit = ScheduleResult(chosen, "trace-1", DecisionSource.CONTROL_PLANE, null, null)
        assertEquals(ScheduleState.CHOSEN, hit.state())

        val miss = ScheduleResult(null, "trace-2", DecisionSource.LOCAL_FALLBACK, "no_candidate", null)
        assertEquals(ScheduleState.NO_CANDIDATE, miss.state())

        val missWithCount =
            ScheduleResult(null, "trace-3", DecisionSource.LOCAL_FALLBACK, "no_candidate_in_scope", null, 5)
        assertEquals(ScheduleState.NO_CANDIDATE, missWithCount.state())
        assertEquals(5, missWithCount.admissionExcludedCount())
    }

    @Test
    fun `作用域排除台数负数归零`() {
        val result =
            ScheduleResult(
                null,
                "trace-1",
                DecisionSource.CONTROL_PLANE,
                "no_candidate_in_scope",
                ScheduleState.NO_CANDIDATE,
                -3,
            )

        assertEquals(0, result.admissionExcludedCount(), "台数没有负数这种取值")
    }
}

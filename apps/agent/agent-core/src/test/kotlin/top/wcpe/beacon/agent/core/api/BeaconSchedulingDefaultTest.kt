package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.api.ScheduleState
import java.util.concurrent.CompletableFuture
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertSame

/**
 * 门面两条带作用域 default 重载的缺省行为单测（FR-244）。
 *
 * 锁两件事：
 * ① 缺省实现按"会不会放宽准入"分两路——空作用域（含 null）委派给不带作用域的重载（逐位等价、不放宽），
 *    非空作用域抛 [UnsupportedOperationException]（能力缺失）而非静默忽略；
 * ② 老实现方"配置缺失就传 empty()"的写法不会因缺省实现而变成抛异常或静默收紧。
 */
class BeaconSchedulingDefaultTest {
    private val zone = "z-a"

    /** 只覆写既有三个方法的老实现（不覆写两条新 default）——这正是 FR-244 要兼容的形态。 */
    private class LegacyScheduling : BeaconScheduling {
        var twoArgCalls = 0
        var oneArgZoneCalls = 0

        /** 复用同一个快照元素，便于断言"委派拿到的就是原方法的产物"。 */
        private val candidate =
            CandidateView("srv-1", "z-a", 90, HealthLevel.HEALTHY, 10, 200, mapOf("k" to "v"))

        override fun acquireCandidate(zone: String): CompletableFuture<ScheduleResult> = acquireCandidate(zone, null)

        override fun acquireCandidate(
            zone: String,
            purpose: String?,
        ): CompletableFuture<ScheduleResult> {
            twoArgCalls++
            return CompletableFuture.completedFuture(
                ScheduleResult(
                    null,
                    "legacy-trace",
                    DecisionSource.LOCAL_FALLBACK,
                    "unavailable",
                    ScheduleState.UNAVAILABLE,
                ),
            )
        }

        override fun candidatesInZone(zone: String): List<CandidateView> {
            oneArgZoneCalls++
            return listOf(candidate)
        }

        override fun healthOf(serverId: String): HealthView? = null

        override fun selfHealth(): HealthView? = null

        override fun dataSource(): DataSourceState = DataSourceState(DataSource.LOCAL_SNAPSHOT, false, Long.MAX_VALUE)
    }

    private val legacy = LegacyScheduling()

    /** 以接口类型调用：走的就是调用方实际看到的门面契约。 */
    private val scheduling: BeaconScheduling = legacy

    @Test
    fun `非空作用域必须抛不支持——静默忽略等于静默放宽准入`() {
        assertFailsWith<UnsupportedOperationException> {
            scheduling.acquireCandidate(zone, "lobby-transfer", AdmissionScope.of("k", "v"))
        }
        assertFailsWith<UnsupportedOperationException> {
            scheduling.candidatesInZone(zone, AdmissionScope.of("k", "v"))
        }
        assertEquals(0, legacy.twoArgCalls, "非空作用域不得偷偷委派出去")
        assertEquals(0, legacy.oneArgZoneCalls)
    }

    @Test
    fun `空作用域与 null 作用域委派给两参重载且不外抛`() {
        val byEmpty = scheduling.acquireCandidate(zone, "lobby-transfer", AdmissionScope.empty())
        val byNull = scheduling.acquireCandidate(zone, "lobby-transfer", null)

        assertEquals("legacy-trace", byEmpty.get().traceId(), "委派后拿到的是两参重载的结果")
        assertEquals(ScheduleState.UNAVAILABLE, byNull.get().state())
        assertEquals(2, legacy.twoArgCalls, "两次调用都应落到两参重载（空作用域不排除任何候选）")
    }

    @Test
    fun `空作用域与 null 作用域在候选读侧同样委派给一参重载`() {
        val byEmpty = scheduling.candidatesInZone(zone, AdmissionScope.empty())
        val byNull = scheduling.candidatesInZone(zone, null)

        assertEquals(1, byEmpty.size, "空作用域 = 全量候选")
        assertEquals(1, byNull.size, "null 视为空作用域")
        assertSame(byEmpty.first(), byNull.first(), "两次都应拿到一参重载的快照本身")
        assertEquals(2, legacy.oneArgZoneCalls, "两次调用都应落到一参重载")
    }

    @Test
    fun `委派不影响原有方法的语义`() {
        val threeArgFallback = scheduling.acquireCandidate(zone, null, AdmissionScope.empty())
        val twoArg = scheduling.acquireCandidate(zone, null)

        assertEquals(twoArg.get().chosen(), threeArgFallback.get().chosen())
        assertEquals(twoArg.get().source(), threeArgFallback.get().source())
        assertEquals(2, legacy.twoArgCalls)
    }
}

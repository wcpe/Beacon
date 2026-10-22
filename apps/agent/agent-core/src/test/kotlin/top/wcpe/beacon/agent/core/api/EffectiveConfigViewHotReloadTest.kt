package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * 视图层的热重载透传链路（FR-278 #1 / #4）：`updateReloadTimeout` / `updateReloadPoolSize`
 * → [ReloadDispatcher]。
 *
 * ## 为什么这一层的测试不能省
 *
 * [ReloadDispatcher] 自己的热更新已有用例，但「控制面下发值 → 视图 → 派发器」这三跳里，
 * **中间那一跳最容易断**：视图多一个自己的字段就会把新值吃掉（读口还对得上、行为却不变）。
 * 因此这里只断言**行为**：改完之后插件到底被判超时还是没有、并发度到底是 1 还是 2。
 *
 * ## 可观测点
 *
 * 并发位有只读口 [EffectiveConfigView.effectiveReloadPoolSize]（诊断与测试共用）；
 * 超时**没有**只读口——它是派发器内部的等待预算，只能靠「慢插件是否被判超时」验证。
 * 这也正是本文件把超时用例写成前后对照（改之前超时、改之后成功）的原因。
 */
class EffectiveConfigViewHotReloadTest {
    /** 跑满指定时长的假监听器：用于从行为上区分「等待预算」是新值还是旧值。 */
    private class SlowListener(
        private val blockMs: Long,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            sleepIgnoringInterrupt(blockMs)
        }
    }

    /**
     * 会卡到被放行的假监听器：用于观察**并发度**。
     *
     * @param twoStarted  前两个监听器开始时归零（=「应有 2 个同时在跑」）
     * @param threeStarted 三个监听器都开始时归零（并发位大于 2 才会归零）
     * @param release      测试放行；带超时是安全阀，断言失败也不让测试永久挂住
     */
    private class GatedListener(
        private val twoStarted: CountDownLatch,
        private val threeStarted: CountDownLatch,
        private val release: CountDownLatch,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            twoStarted.countDown()
            threeStarted.countDown()
            release.await(10, TimeUnit.SECONDS)
        }
    }

    /**
     * `updateReloadTimeout` 之后，同一个 300ms 的插件由「超时」转为「成功」。
     *
     * 初始预算刻意取 100ms（< 300ms），新值取 1500ms（> 300ms）：
     * 两侧都留 3 倍以上余量，避免机器负载把断言变成偶发。
     */
    @Test
    fun `热更新超时后慢插件不再被判超时`() {
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 100L)
        try {
            val before = view.onChange(SlowListener(blockMs = 300L))
            val timedOut = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single()
            before.remove()

            assertTrue(timedOut.timeout, "预算 100ms 下 300ms 的插件必须判超时，实际：${timedOut.reason}")

            view.updateReloadTimeout(1_500L)
            view.onChange(SlowListener(blockMs = 300L))
            val applied = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single()

            assertTrue(applied.ok, "视图热更新为 1500ms 后同一档慢插件应成功，实际：${applied.reason}")
            assertFalse(applied.timeout, "已跑完的插件不该被判超时")
        } finally {
            view.close()
        }
    }

    /**
     * `updateReloadPoolSize(2)` 之后并发度真的变成 2（不是「读口显示 2、执行仍串行」）。
     *
     * 判定方式：3 个会卡住的监听器同时派发——恰好 2 个开始执行，第 3 个必须还进不来。
     * - 若热更新没生效（仍是构造时的 1 位）：`twoStarted` 永远等不到；
     * - 若并发位被忽略成「不限」：第 3 个会立刻开始，`threeStarted` 归零。
     */
    @Test
    fun `热更新并发位后按新并发度执行`() {
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 10_000L, reloadPoolSize = 1)
        val twoStarted = CountDownLatch(2)
        val threeStarted = CountDownLatch(3)
        val release = CountDownLatch(1)
        val report = AtomicReference<ConfigReloadReport>()
        try {
            view.updateReloadPoolSize(2)
            assertEquals(2, view.effectiveReloadPoolSize, "视图的只读口须如实反映热更新的并发位")

            repeat(3) { view.onChange(GatedListener(twoStarted, threeStarted, release)) }
            // 派发放到独立线程：fireChanged 会等到阻塞插件跑完才返回，主线程得先观察并发度。
            val dispatch = Thread { report.set(view.fireChanged(setOf("liveops/schedule.json"), "md5-new")) }
            dispatch.start()
            try {
                assertTrue(
                    twoStarted.await(5, TimeUnit.SECONDS),
                    "并发位为 2 时应有 2 个监听器同时开始执行（仍按 1 位串行？）",
                )
                assertFalse(
                    threeStarted.await(300, TimeUnit.MILLISECONDS),
                    "并发位为 2 时第 3 个监听器不得开始（说明并发度没有按新值收口）",
                )
            } finally {
                release.countDown()
            }
            dispatch.join(10_000)
            val finished = assertNotNull(report.get(), "派发应已产出报告")
            assertEquals(3, finished.outcomes.size, "被挡住的第 3 个在放行后同样要被执行")
            assertTrue(finished.outcomes.all { it.ok }, "放行后三个监听器都应成功：${finished.outcomes.map { it.reason }}")
        } finally {
            release.countDown()
            view.close()
        }
    }

    /** 视图的只读口与构造注入 / 热更新的值一致，且越界值按派发器的收口口径生效。 */
    @Test
    fun `并发位只读口与注入值一致`() {
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L, reloadPoolSize = 8)
        try {
            assertEquals(8, view.effectiveReloadPoolSize, "构造注入的并发位应如实反映")

            view.updateReloadPoolSize(3)
            assertEquals(3, view.effectiveReloadPoolSize, "热更新的并发位应如实反映")

            view.updateReloadPoolSize(9_999)
            assertEquals(ReloadDispatcher.MAX_POOL_SIZE, view.effectiveReloadPoolSize, "上界应收口为 256")

            view.updateReloadPoolSize(0)
            assertEquals(ReloadDispatcher.MIN_POOL_SIZE, view.effectiveReloadPoolSize, "下界应收口为 1")
        } finally {
            view.close()
        }
    }
}

/**
 * 睡满 [totalMs]，被中断时继续睡满。
 *
 * 单次 `Thread.sleep` 不够：超时取消会中断插件线程并让 sleep 提前抛异常，
 * 而这里要模拟的正是「不理会中断、就是卡这么久」的插件。每次至少睡 1ms，
 * 避免 `Thread.sleep(0)` 立刻返回把循环变成忙等。
 */
private fun sleepIgnoringInterrupt(totalMs: Long) {
    val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(totalMs)
    while (true) {
        val remainNanos = deadline - System.nanoTime()
        if (remainNanos <= 0L) {
            return
        }
        try {
            Thread.sleep(TimeUnit.NANOSECONDS.toMillis(remainNanos).coerceAtLeast(1L))
        } catch (_: InterruptedException) {
            // 刻意忽略：本用例要的正是「中断也拦不住它跑满」。
        }
    }
}

package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Semaphore
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNotSame
import kotlin.test.assertSame
import kotlin.test.assertTrue

/**
 * 重载超时热更新（FR-278 #1 / #7）的行为契约。
 *
 * ## 为什么只能靠行为断言，不能靠「读一下字段」
 *
 * 控制面改 `delivery.config-reload-timeout-ms` 之后，唯一有意义的验收是
 * 「同一个卡了 300ms 的插件，改之前判超时、改之后判成功」。派发器**没有**超时的读口
 * （预算只在 [ReloadDispatcher.dispatchAll] 内部读一次），所以这里的断言无法被
 * 「加个 getter 再断言它」的空洞实现满足——只能让慢插件真的跑一遍。
 *
 * ## 为什么同时锁「不重建线程池」
 *
 * 超时只是 `future.get` 的等待时长，与池容量无关。若热更新顺手重建池，在途 reload 会被
 * 打断——一次「调参数」就变成「一批插件重载失败」，正是 FR-278 要消灭的形态。
 * 这里用**执行线程身份**判定：自建池会复用同一个核心线程（空闲 60s 才回收，测试窗口内必然
 * 复用），故「线程变了」= 换了池。这是确定性判据，不用并发度去猜时序。
 */
class ReloadDispatcherTimeoutHotReloadTest {
    /**
     * 会**跑满指定时长**的假监听器。
     *
     * 用固定时长而不是「等测试放行」：被测行为本身就是「预算有多长」，需要一个与预算无关的
     * 确定耗时。中断被刻意忽略——超时会 `cancel(true)` 中断插件线程，而现实中的卡死插件
     * 往往不理会中断，吞掉中断才如实还原该场景，也避免线程带着中断标记回到池里干扰下一轮。
     */
    private class SlowListener(
        private val blockMs: Long,
    ) : ConfigChangeListener {
        /** 每次进入回调时自增（供「确实跑过」的断言）。 */
        val runs = AtomicInteger(0)

        /** 每次执行所在的线程，按调用顺序记录（用于判定池是否被重建）。 */
        val threads = CopyOnWriteArrayList<Thread>()

        /** 每跑满一轮释放一个许可；[awaitFinished] 用它累积等待，不靠 sleep 猜时序。 */
        private val done = Semaphore(0)

        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            runs.incrementAndGet()
            threads += Thread.currentThread()
            sleepIgnoringInterrupt(blockMs)
            done.release()
        }

        /** 等到已有 [count] 轮跑满时长并退场（每轮各消耗一个许可）。 */
        fun awaitFinished(count: Int) {
            assertTrue(done.tryAcquire(count, 5, TimeUnit.SECONDS), "应已有 $count 轮跑满 ${blockMs}ms 并退场")
        }
    }

    /** 只记录「在哪个线程执行」「执行了几次」的监听器：用于区分走池与内联。 */
    private class ThreadRecordingListener(
        private val ranOn: AtomicReference<Thread>,
        private val runs: AtomicInteger,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            ranOn.set(Thread.currentThread())
            runs.incrementAndGet()
        }
    }

    /**
     * **本组核心验收**：预算是 100ms 时阻塞 300ms 的插件判超时；改成 2000ms 后同一个插件成功。
     *
     * 两段断言缺一不可：
     *  - 只验「改之后成功」→ 若实现从一开始就忽略超时（永远不判超时）也能过；
     *  - 只验「改之前超时」→ 若 `updateTimeoutMs` 是空实现也能过。
     * 合起来才锁住「新值真的对下一轮派发生效」。
     */
    @Test
    fun `超时热重载后同一慢插件由超时转为成功`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 100L, poolSize = 1)
        val listener = SlowListener(blockMs = 300L)
        try {
            val first = dispatcher.dispatch(listener, setOf("liveops/schedule.json"), "md5-new")
            assertTrue(first.timeout, "初始预算 100ms 下阻塞 300ms 的插件必须判超时，实际：${first.reason}")
            assertFalse(first.ok, "超时不得被当成成功")
            assertTrue(first.reason.contains("重载超时"), "原因须指明是执行超时而非排队超时，实际：${first.reason}")

            // 等这一轮真的退场：池只有 1 位，抢不到线程就没法用线程身份判定有没有重建池。
            listener.awaitFinished(1)

            dispatcher.updateTimeoutMs(2_000L)
            val second = dispatcher.dispatch(listener, setOf("liveops/schedule.json"), "md5-new")

            assertTrue(second.ok, "预算改为 2000ms 后同一插件应成功（超时值未生效？），实际：${second.reason}")
            assertFalse(second.timeout, "已跑完的插件不该被判超时")
            assertEquals(2, listener.runs.get(), "两轮都应真的执行过")
            assertSame(
                listener.threads[0],
                listener.threads[1],
                "改超时不得重建线程池：执行线程变了说明热更新换了池，会打断在途 reload",
            )
        } finally {
            dispatcher.close()
        }
    }

    /**
     * 预算归零（或负数）必须**退回内联执行**，且结果仍然正确。
     *
     * 0 是既有调用点的默认值（config.yml 不写 / 显式关掉保护），语义是「不设超时、就地执行」。
     * 若热更新把 0 当成「非法」收口成某个正数，agent 会莫名其妙开始建线程池并开始判超时——
     * 对一个明确要求关闭保护的部署来说，这是行为反转。
     */
    @Test
    fun `超时归零后退回内联执行且结果仍正确`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 2_000L, poolSize = 4)
        val caller = Thread.currentThread()
        val ranOn = AtomicReference<Thread>()
        val runs = AtomicInteger(0)
        try {
            // 先确认「启用超时」时确实走池：否则下面的内联断言可能本来就在调用线程上，等于没验。
            dispatcher.dispatch(ThreadRecordingListener(ranOn, runs), setOf("liveops/schedule.json"), "md5-new")
            val poolThread = ranOn.get()
            assertNotNull(poolThread, "启用超时时监听器应已执行")
            assertNotSame(caller, poolThread, "启用超时时监听器应在派发器线程池执行")

            ranOn.set(null)
            dispatcher.updateTimeoutMs(0L)
            val outcome = dispatcher.dispatch(ThreadRecordingListener(ranOn, runs), setOf("liveops/schedule.json"), "md5-new")

            assertSame(caller, ranOn.get(), "超时归零必须退回内联执行（不建池），这才是既有调用点的行为")
            assertTrue(outcome.ok, "内联执行的结果同样必须正确：${outcome.reason}")

            // 负数同样收口为 0：运维手滑写负值时不该退化成「每轮都判超时」。
            ranOn.set(null)
            dispatcher.updateTimeoutMs(-5L)
            dispatcher.dispatch(ThreadRecordingListener(ranOn, runs), setOf("liveops/schedule.json"), "md5-new")

            assertSame(caller, ranOn.get(), "负数预算应被收口为 0（内联执行）")
            assertEquals(3, runs.get(), "三次派发都应真的执行一次")
        } finally {
            dispatcher.close()
        }
    }
}

/**
 * 睡满 [totalMs]，被中断时继续睡。
 *
 * 单次 `Thread.sleep` 不够：中断会提前打断它（抛 `InterruptedException`），
 * 而本用例要模拟的正是「不理会中断、就是要跑满这么久」的插件。
 * 每次至少睡 1ms，避免 `Thread.sleep(0)` 立刻返回把循环变成忙等。
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
            // 刻意忽略：超时取消会中断插件线程，而本用例要的正是「中断也拦不住它跑满」。
        }
    }
}

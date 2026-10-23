package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ConfigReloadResult
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertSame
import kotlin.test.assertTrue

/**
 * 并发位热重载（FR-278/FR-280）的行为契约。
 *
 * ## 本组用例锁定的是什么
 *
 * 「改并发位」这件事有两种完全不同的做法，结果天差地别：
 *  - 正确：旧池**优雅退役**（`shutdown`，不中断），在途 reload 跑完，新池接管后续派发；
 *  - 错误：旧池被 `shutdownNow` 打断——一次调参变成「一批插件重载失败」。
 *
 * 二者的功能差异**只在并发时序里显形**：串行测试全会通过，只有让「在途任务正卡在中间时
 * 触发换池」才能把错误实现抓出来。故这里全部用**会阻塞的假监听器**制造确定性时序，
 * 不靠 sleep 猜时序。
 *
 * 另外锁定「值未变化不重建」：周期下发每 30 秒送同一份配置，若每次都换池，
 * 线程组会反复起落——那是纯噪声，且会让在途批次撞上退役。
 *
 * ## 后半段锁定「退役池必须被接管」
 *
 * 优雅退役没有时限（插件可以不响应中断），所以旧池不能换完就丢掉引用：`close()` 必须能
 * 关掉**当前池 + 全部退役池**。否则「改了一次并发位」就等于开了一条停机也关不掉的任务通道，
 * 且线程永久泄漏。这两条用「只会因中断而结束的监听器」断言，实现漏收集时会稳定变红。
 */
class ReloadDispatcherPoolResizeTest {
    /** 可阻塞的监听器：进入即 countDown，直到 [release] 放开才返回。 */
    private class BlockingListener(
        private val entered: CountDownLatch,
        private val release: CountDownLatch,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            entered.countDown()
            release.await()
        }
    }

    /** 会记录「是否被中断」的阻塞监听器：中断会以异常形式逃出 await。 */
    private class InterruptRecordingListener(
        private val entered: CountDownLatch,
        private val release: CountDownLatch,
    ) : ReportingConfigChangeListener {
        override fun onConfigChangedReporting(
            changedDataIds: Set<String>,
            newMd5: String,
        ): ConfigReloadResult {
            entered.countDown()
            // release.await() 在被打断时抛 InterruptedException —— 那正是 shutdownNow 的指纹。
            release.await()
            return ConfigReloadResult.ok()
        }
    }

    private fun requests(
        count: Int,
        listener: ConfigChangeListener,
    ): List<ReloadDispatcher.ReloadRequest> = List(count) { ReloadDispatcher.ReloadRequest(listener, setOf("a"), "md5") }

    /**
     * 换成更大的并发位后，**新派发真的按新容量并发**。
     *
     * 时序：旧池（1 位）先被一个阻塞任务占满 → 热更新到 4 → 再派发 4 个阻塞任务。
     * 若换池生效，4 个都应立刻开始执行（`entered` 归零）；若没换池，1 位的旧池最多让
     * 一个任务开始，其余在两个阻塞任务之后既跑不了也进不了队列（队列容量同为 1）。
     */
    @Test
    fun `热更新并发位后新派发按新并发度执行`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 10_000L, poolSize = 1)
        val oldEntered = CountDownLatch(1)
        val releaseOld = CountDownLatch(1)
        val oldTask =
            Thread { dispatcher.dispatch(BlockingListener(oldEntered, releaseOld), setOf("a"), "md5") }
        oldTask.start()
        try {
            assertTrue(oldEntered.await(5, TimeUnit.SECONDS), "旧池的占位任务应已开始执行")

            dispatcher.updatePoolSize(4)
            assertEquals(4, dispatcher.effectivePoolSize, "并发位应已更新为 4")

            val entered = CountDownLatch(4)
            val release = CountDownLatch(1)
            val outcomes = AtomicReference<List<PluginReloadOutcome>>()
            val batch =
                Thread {
                    outcomes.set(dispatcher.dispatchAll(requests(4, BlockingListener(entered, release))))
                }
            batch.start()
            try {
                assertTrue(
                    entered.await(5, TimeUnit.SECONDS),
                    "并发位热更新为 4 后，4 个监听器应同时开始执行；" +
                        "实际只开始了 ${4 - entered.count} 个（说明仍在用旧池的 1 位）",
                )
            } finally {
                release.countDown()
            }
            batch.join(10_000)
            val result = assertNotNull(outcomes.get(), "批量派发应已产出结果")
            assertEquals(4, result.size)
            assertTrue(result.all { it.ok }, "被放行的阻塞任务都应成功：${result.map { it.reason }}")
        } finally {
            releaseOld.countDown()
            oldTask.join(5_000)
            dispatcher.close()
        }
    }

    /**
     * **本组的核心验收：换池不得丢在途任务。**
     *
     * 让一个 reload 卡在插件回调中间，此时下发新并发位（旧池退役），再放行该任务——
     * 它必须**正常跑完并报成功**。
     *
     * 用 `shutdownNow` 实现换池的话，这里的 `release.await()` 会被中断抛异常，
     * 结果变成「失败」——本用例就是把「改并发位不该丢任务」这条语义钉死的地方。
     */
    @Test
    fun `换池时在途的 reload 仍能跑完并报成功`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 10_000L, poolSize = 2)
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val outcome = AtomicReference<PluginReloadOutcome>()
        val inFlight =
            Thread { outcome.set(dispatcher.dispatch(InterruptRecordingListener(entered, release), setOf("a"), "md5")) }
        inFlight.start()
        try {
            assertTrue(entered.await(5, TimeUnit.SECONDS), "在途任务应已开始执行")

            // 此刻换池：旧池优雅退役，但正在跑的那个不许被打断。
            dispatcher.updatePoolSize(8)

            release.countDown()
            inFlight.join(10_000)
            val result = assertNotNull(outcome.get(), "在途任务应已产出结果（不得因换池而丢失）")
            assertTrue(
                result.ok,
                "换池不得中断在途 reload（用 shutdownNow 会造成这个失败）：${result.reason}",
            )
            assertFalse(result.timeout, "在途任务已跑完，不该被判超时：${result.reason}")
        } finally {
            release.countDown()
            inFlight.join(5_000)
            dispatcher.close()
        }
    }

    /**
     * 值未变化时**不重建池**：同一个并发位再下发一次，仍应复用原池。
     *
     * ## 为什么用「工作线程身份」判定，而不是靠并发度/队列饱和
     *
     * 每个自建池都有**自己的线程工厂**（线程名序号从 1 起），所以「换了池」必然表现为
     * 「执行监听器的 Thread 对象变了」。反过来，同池复用会在同一个核心线程上跑（核心线程
     * 空闲 60s 才回收，测试窗口内必然复用）——这是**确定性**判据。
     *
     * 而用「占满并发位再看新任务能否执行」的不变式会引入时序抖：占位任务一旦超时被 cancel，
     * 会 interrupt 它自己并释放线程，队列里的新任务随即可能被执行，断言就会偶发失败。
     * 用 flaky 手段证明语义契约不可接受。
     */
    @Test
    fun `并发位未变化时不重建线程池`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 5_000L, poolSize = 1)
        val firstThread = AtomicReference<Thread>()
        val secondThread = AtomicReference<Thread>()
        try {
            assertTrue(dispatcher.dispatch(ThreadRecordingListener(firstThread), setOf("a"), "md5").ok)
            assertNotNull(firstThread.get(), "首个任务应记录到执行线程")

            // 同一个值再下发一次：不该换池。
            dispatcher.updatePoolSize(1)
            assertEquals(1, dispatcher.effectivePoolSize)

            assertTrue(dispatcher.dispatch(ThreadRecordingListener(secondThread), setOf("a"), "md5").ok)
            assertNotNull(secondThread.get(), "第二个任务应记录到执行线程")

            assertSame(
                firstThread.get(),
                secondThread.get(),
                "并发位未变化时不该换池：执行线程变了说明重建了新池",
            )
        } finally {
            dispatcher.close()
        }
    }

    /**
     * 「取池之后、提交之前刚好发生热重载」这一竞态下的文案：**必须说退役，不能说停机**。
     *
     * ## 为什么要为一条极窄竞态专门写用例
     *
     * 池被 shutdown 有两个来源——停机的 `shutdownNow` 与热重载的 `shutdown`。两者在
     * 「提交会被拒绝」这一点上完全一样，最容易被合成一句「执行器已关闭」。一旦合成，
     * 一次普通的并发位调参就会在回执里留下「agent 正在停机」，运维会去查一个并不存在的停机事件。
     *
     * 这里用「注入一个已 shutdown 的执行器」把那个竞态状态**确定性地**摆出来
     * （agent 未停机 + 池已退役 = 竞态中 task 看到的正是这个状态），不必去赌线程时序。
     */
    @Test
    fun `池因热重载退役时不得报成停机`() {
        val retired = ReloadDispatcher.newDaemonPool(1)
        retired.shutdown()
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 5_000L, executor = retired)
        try {
            val ran = AtomicInteger(0)
            val outcome = dispatcher.dispatch(CountingListener(ran), setOf("a"), "md5")

            assertEquals(0, ran.get(), "已退役的池不可能执行任务")
            assertFalse(outcome.ok, "未执行不可能成功")
            assertTrue(
                outcome.reason.contains("未执行"),
                "应如实报「未执行」，实际：${outcome.reason}",
            )
            assertTrue(
                outcome.reason.contains("热重载"),
                "原因须指向并发位热重载（运维该做的是重试 / 核对下发），实际：${outcome.reason}",
            )
            assertFalse(
                outcome.reason.contains("停机"),
                "池因热重载退役 ≠ agent 停机，不得这样报，实际：${outcome.reason}",
            )
        } finally {
            dispatcher.close()
        }
    }

    // ---- 退役池必须被 close() 接管（关不掉 = 停机后还在跑插件 + 线程永久漏掉） ----

    /**
     * **本组核心验收：退役池里的在途任务必须被 `close()` 打断。**
     *
     * ## 时序
     *
     * 旧池（1 位）里一个 reload 卡在插件回调中 → 热更新并发位（旧池优雅退役，任务仍在跑）
     * → `close()` 停机。此刻 agent 已停用，那个任务**必须被中断**，不能继续跑完：
     * 目标配置已随本次停用过期，跑完只会把旧配置写进「已经不该工作」的进程。
     *
     * ## 为什么能抓住「不收集退役池」的实现
     *
     * 换池后 `selfBuiltPool` 被置空且新池尚未建出，若退役池没有被任何字段持有，
     * `close()` 的 `shutdownNow` 就**什么都关不到**——任务会一直在监听器里等下去，
     * 于是 `inFlight.isAlive` 为真、`interrupted` 为假，本用例两条断言同时变红。
     */
    @Test
    fun `退役池里的在途任务会被 close 打断`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 10_000L, poolSize = 1)
        val entered = CountDownLatch(1)
        val listener = InterruptOnlyListener(entered)
        val outcome = AtomicReference<PluginReloadOutcome>()
        val inFlight = Thread { outcome.set(dispatcher.dispatch(listener, setOf("a"), "md5")) }
        inFlight.start()
        try {
            assertTrue(entered.await(5, TimeUnit.SECONDS), "旧池里的任务应已开始执行")

            dispatcher.updatePoolSize(4)
            assertFalse(
                listener.interrupted.get(),
                "换并发位是优雅退役，不得打断在途任务（那是 shutdownNow 的指纹）",
            )

            dispatcher.close()

            inFlight.join(5_000)
            assertFalse(inFlight.isAlive, "close() 之后在途任务必须结束；仍在执行说明退役池没被收集")
            assertTrue(listener.interrupted.get(), "退役池里的任务属于已停用的 agent，必须被 interrupt")
            val result = assertNotNull(outcome.get(), "被中断的派发也应产出结果（按执行失败回报）")
            assertFalse(result.ok, "被中断的 reload 不可能成功")
        } finally {
            dispatcher.close()
            inFlight.join(1_000)
        }
    }

    /**
     * 反复换池多次后，`close()` 仍能关掉**全部**退役池。
     *
     * 每次换池漏一个池，就能在停机后留下一批还在跑插件逻辑的线程；这里换两次、留两个卡住的
     * 任务（分别落在两个不同的退役池里），停机后两个都必须被中断。
     */
    @Test
    fun `反复换池后 close 仍能关掉全部退役池`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 10_000L, poolSize = 1)
        val firstEntered = CountDownLatch(1)
        val first = InterruptOnlyListener(firstEntered)
        val firstDispatch = Thread { dispatcher.dispatch(first, setOf("a"), "md5") }
        firstDispatch.start()
        val secondEntered = CountDownLatch(1)
        val second = InterruptOnlyListener(secondEntered)
        val secondDispatch = Thread { dispatcher.dispatch(second, setOf("a"), "md5") }
        try {
            assertTrue(firstEntered.await(5, TimeUnit.SECONDS), "第一个任务应已在首个池里开始执行")
            dispatcher.updatePoolSize(2)
            secondDispatch.start()
            assertTrue(secondEntered.await(5, TimeUnit.SECONDS), "第二个任务应已在换出来的新池里开始执行")

            dispatcher.updatePoolSize(3)
            dispatcher.close()

            firstDispatch.join(5_000)
            secondDispatch.join(5_000)
            assertFalse(firstDispatch.isAlive, "第一个退役池里的任务必须被停机打断")
            assertFalse(secondDispatch.isAlive, "第二个退役池里的任务必须被停机打断")
            assertTrue(first.interrupted.get(), "首个退役池不得因「又换了一次池」而丢失跟踪")
            assertTrue(second.interrupted.get(), "最后一次退役的池同样必须被 close 关掉")
        } finally {
            dispatcher.close()
            firstDispatch.join(1_000)
            secondDispatch.join(1_000)
        }
    }

    /**
     * 只可能因**中断**而结束的监听器：没提供放行口，所以「它跑完了」等价于「它被 interrupt 了」。
     *
     * 用「等一个永远不会 countDown 的闸门」而不是 `sleep`，是为了让失败表现为**确定性的挂起**
     * （被 join 超时抓到），而不是靠时延猜。
     */
    private class InterruptOnlyListener(
        private val entered: CountDownLatch,
    ) : ConfigChangeListener {
        /** 是否被中断过：`close()` 的 `shutdownNow` 与「任务自然跑完」在这里分道扬镳。 */
        val interrupted = AtomicBoolean(false)

        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            entered.countDown()
            try {
                CountDownLatch(1).await()
            } catch (e: InterruptedException) {
                interrupted.set(true)
                throw e
            }
        }
    }

    /**
     * 越界下发被收口到 [ReloadDispatcher.MIN_POOL_SIZE, ReloadDispatcher.MAX_POOL_SIZE]。
     *
     * 三种都验：0 / -1 收口到 1（且**不抛异常**——容量 0 会让线程池直接构造失败），
     * 9999 收口到 256 而不是照原值建出一个千级线程池。
     */
    @Test
    fun `越界并发位被收口到合法区间`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 1_000L, poolSize = 4)
        try {
            dispatcher.updatePoolSize(0)
            assertEquals(ReloadDispatcher.MIN_POOL_SIZE, dispatcher.effectivePoolSize, "0 应收口为下界 1")

            dispatcher.updatePoolSize(-1)
            assertEquals(ReloadDispatcher.MIN_POOL_SIZE, dispatcher.effectivePoolSize, "-1 应收口为下界 1")

            dispatcher.updatePoolSize(9999)
            assertEquals(ReloadDispatcher.MAX_POOL_SIZE, dispatcher.effectivePoolSize, "9999 应收口为上界 256")

            // 收口后仍能正常建池并派发：证明 1 是可用容量（而不是"看起来收口了、建池照样炸"）。
            dispatcher.updatePoolSize(1)
            val ran = AtomicInteger(0)
            val outcome = dispatcher.dispatch(CountingListener(ran), setOf("a"), "md5")
            assertTrue(outcome.ok, "收口后的容量必须可用：${outcome.reason}")
            assertEquals(1, ran.get(), "监听器应被执行一次")
        } finally {
            dispatcher.close()
        }
    }

    /**
     * `close()` 之后下发并发位**不得**建出新池：关闭语义必须单向。
     *
     * 反例的后果很具体：agent 已停用，一次周期下发就能让派发重新跑起来，
     * 而调用方（交付编排 / 平台壳）都以为它已经停了。
     */
    @Test
    fun `关闭后热更新并发位不产生新池`() {
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 5_000L, poolSize = 2)
        dispatcher.close()

        dispatcher.updatePoolSize(8)

        val ran = AtomicInteger(0)
        val outcome = dispatcher.dispatch(CountingListener(ran), setOf("a"), "md5")

        assertEquals(0, ran.get(), "关闭后不得执行监听器（哪怕刚下发过新并发位）")
        assertFalse(outcome.ok, "关闭后派发不可能成功")
        assertTrue(
            outcome.reason.contains("未执行"),
            "关闭后应如实报「未执行」，实际：${outcome.reason}",
        )
        assertTrue(
            outcome.reason.contains("停机"),
            "原因须说清是停机而非容量问题，实际：${outcome.reason}",
        )
    }

    /** 只计数的监听器：用于证明「有没有真的执行」。 */
    private class CountingListener(
        private val ran: AtomicInteger,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            ran.incrementAndGet()
        }
    }

    /** 记录「在哪个线程上被执行」的监听器：用于断言池是否被重建（见对应用例的说明）。 */
    private class ThreadRecordingListener(
        private val recorder: AtomicReference<Thread>,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            recorder.set(Thread.currentThread())
        }
    }
}

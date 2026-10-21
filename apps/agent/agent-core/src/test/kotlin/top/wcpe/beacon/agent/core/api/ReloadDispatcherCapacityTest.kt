package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ConfigReloadResult
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.config.EffectiveResult
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 重载派发器的容量与饱和语义测试。
 *
 * 覆盖两个**会误导运维**的真实风险：
 *  1. 池太小（原为 4）让大量插件在队列里干等，把「没轮到执行」伪装成「执行超时」；
 *  2. 队列无界时同理，且会让第 N 个插件永远等下去。
 *
 * 判定标准：**「未执行」必须与「超时」在结果里可区分**。把两者混成一句话，
 * 排查方向会完全错掉——超时要去查插件为什么慢，未执行要去查并发与卡死线程。
 */
class ReloadDispatcherCapacityTest {
    private fun store(): EffectiveConfigStore =
        EffectiveConfigStore().apply {
            replace(
                EffectiveResult(
                    namespace = "prod",
                    serverId = "lobby-1",
                    group = "area1",
                    zone = "zoneA",
                    md5 = "md5-new",
                    items = listOf(ConfigItem("liveops/schedule.json", "json", "aa", "{}")),
                ),
            )
        }

    /** 一个只关心「跑没跑」的监听器；[ran] 计数即可证明是否真被执行。 */
    private class CountingListener(
        private val ran: java.util.concurrent.atomic.AtomicInteger,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            ran.incrementAndGet()
        }
    }

    /**
     * 单服 60 个插件（常见的大厅服形态）必须能在超时预算内**全部真正执行**，不因池小排队。
     *
     * 这是对「池容量够不够处理现实规模」的直接验收：若池只有 4，会有大量插件排队，
     * 一旦超时预算小就会集体被误判成「超时」。
     */
    @Test
    fun `六十个插件应全部真正执行而非排队到超时`() {
        val ran = java.util.concurrent.atomic.AtomicInteger(0)
        // 超时给 5s：真并发下 60 个瞬时完成的监听器远用不到；若池容量不足排起队，
        // 队列里的会一直等到前一批完成——这里靠「全部执行过」而不是「没超时」来判定。
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 5_000L)
        try {
            repeat(60) { view.onChange(CountingListener(ran)) }
            val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

            // 60 个都必须被执行（计数为 60），且全部成功。
            assertEquals(60, ran.get(), "60 个插件应全部真正执行，实际执行 ${ran.get()} 个")
            assertEquals(60, report.outcomes.size)
            assertTrue(report.outcomes.all { it.ok }, "瞬时完成的监听器不应有失败")
        } finally {
            view.close()
        }
    }

    /**
     * 任务**始终未被执行**时不得报成「重载超时」，而要说「未执行」。
     *
     * ## 这条用注入的执行器而不是真线程池，是刻意的
     *
     * 用真池制造「占满」需要和线程调度抢时间：占位线程可能在你提交之前让出位置，
     * 于是用例时而通过、时而失败。**用 flaky 测试证明一个语义契约是不可接受的**——
     * 它会把真回归淹在噪声里。这里用一个「只入队、永不执行」的执行器，让「没轮到」
     * 成为确定性前提，于是断言可以精确到原因文案本身。
     *
     * 这正是被测语义的核心：没跑 ≠ 跑得慢。运维看到「超时」会去查插件为什么慢，
     * 看到「未执行」才会去查并发与卡死线程——两者绝不可混。
     */
    @Test
    fun `任务从未被取走执行时不得报成超时`() {
        val neverRuns = NeverRunsExecutor()
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 50L, executor = neverRuns)
        val listener = CountingListener(java.util.concurrent.atomic.AtomicInteger(0))

        val outcome = dispatcher.dispatch(listener, setOf("a"), "md5")

        assertTrue(!outcome.ok, "没被执行不可能成功")
        assertTrue(
            outcome.reason.contains("未执行"),
            "从未被取走执行须明确说「未执行」，实际：${outcome.reason}",
        )
        assertTrue(
            !outcome.timeout,
            "未执行不是超时（timeout 标志会给运维错误指向），实际：${outcome.reason}",
        )
    }

    /**
     * 任务**被取走后卡住**时，才报「超时」。
     *
     * 与上一条互补：同样是「没结果」，但这次确实跑过，语义必须有所不同。
     * 两条合起来锁定「跑得慢」与「没轮到」的可区分性。
     */
    @Test
    fun `任务被取走执行但卡住时应报超时`() {
        val release = CountDownLatch(1)
        val pool =
            ReloadDispatcher.newPool(
                java.util.concurrent.ArrayBlockingQueue(ReloadDispatcher.QUEUE_CAPACITY),
                ReloadDispatcher.defaultThreadFactory(),
                java.util.concurrent.ThreadPoolExecutor.AbortPolicy(),
            )
        val dispatcher = ReloadDispatcher(reloadTimeoutMs = 200L, executor = pool)
        try {
            val hanging =
                object : ReportingConfigChangeListener {
                    override fun onConfigChangedReporting(
                        changedDataIds: Set<String>,
                        newMd5: String,
                    ): ConfigReloadResult {
                        release.await()
                        return ConfigReloadResult.ok()
                    }
                }
            val outcome = dispatcher.dispatch(hanging, setOf("a"), "md5")

            assertTrue(!outcome.ok, "卡住不可能成功")
            assertTrue(
                outcome.timeout,
                "真跑过但没跑完应标 timeout=true，实际：${outcome.reason}",
            )
            assertTrue(
                outcome.reason.contains("超时"),
                "真超时的原因须说超时，实际：${outcome.reason}",
            )
        } finally {
            release.countDown()
            pool.shutdownNow()
        }
    }

    /**
     * 只接受提交、**永不执行**的执行器：让「没轮到」成为确定性前提。
     *
     * 不真的跑任务，但返回一个合法的未完成 Future——这正是「在队列里排队」在调用方
     * 眼里的样子（提交成功、取结果时等到超时）。
     */
    private class NeverRunsExecutor : java.util.concurrent.AbstractExecutorService() {
        override fun execute(command: Runnable) {
            // 刻意不执行。
        }

        override fun shutdown() = Unit

        override fun shutdownNow(): MutableList<Runnable> = mutableListOf()

        override fun isShutdown(): Boolean = false

        override fun isTerminated(): Boolean = false

        override fun awaitTermination(
            timeout: Long,
            unit: TimeUnit,
        ): Boolean = false

        override fun <T> submit(task: java.util.concurrent.Callable<T>): java.util.concurrent.Future<T> =
            java.util.concurrent.FutureTask(task)
    }

    /**
     * close 后再次 fireChanged 不得抛异常，且必须把结果如实报成「未执行」。
     *
     * 这是 close 与 fireChanged 的竞态：agent 停用时正在派发，提交会撞上已关闭的池。
     */
    @Test
    fun `关闭后派发应报未执行而不是抛异常`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 5_000L)
        view.onChange(CountingListener(java.util.concurrent.atomic.AtomicInteger(0)))
        view.close()

        // 不抛异常即可（正常返回报告），且结果里应能读出「未执行」而非「超时」。
        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
        assertTrue(report.outcomes.size == 1, "应仍产出 1 条结果，实际 ${report.outcomes.size}")
        assertTrue(!report.outcomes[0].ok, "关闭后派发不可能成功")
        assertTrue(
            report.outcomes[0].reason.contains("未执行"),
            "关闭后应报「未执行」，实际：${report.outcomes[0].reason}",
        )
    }

    /**
     * **并行必须真的并行**：N 个各耗时 T 的插件，总耗时应接近 T 而不是 N×T。
     *
     * ## 为什么用耗时来验，而不是数「同时开始」
     *
     * 「同时开始」难以稳定观测（线程调度会抖）。而耗时的数量级差异是**语义层面**的：
     * 串行是 N×T，并行是 T。取 N=8、T=150ms：串行约 1200ms、并行约 150ms，
     * 阈值取 600ms 就能干净区分，且不受机器快慢的绝对影响太大。
     *
     * 这条测试是「别把 dispatchAll 悄悄改回逐个 dispatch」的回归锁——
     * 那种改动功能上完全正确，只有耗时能暴露它。
     */
    @Test
    fun `多个慢插件应并行执行而非串行叠加`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 10_000L)
        val perPluginMs = 150L
        val pluginCount = 8
        try {
            repeat(pluginCount) {
                view.onChange(
                    object : ConfigChangeListener {
                        override fun onConfigChanged(
                            changedDataIds: Set<String>,
                            newMd5: String,
                        ) {
                            Thread.sleep(perPluginMs)
                        }
                    },
                )
            }
            val startedAt = System.nanoTime()
            val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
            val elapsedMs = (System.nanoTime() - startedAt) / 1_000_000

            assertEquals(pluginCount, report.outcomes.size)
            assertTrue(report.outcomes.all { it.ok }, "慢但成功的插件不应被判失败")

            val serialMs = perPluginMs * pluginCount
            // 阈值：串行的一半以上就说明没有并行（并行约 150ms，串行约 1200ms）。
            assertTrue(
                elapsedMs < serialMs / 2,
                "并行未生效：$pluginCount 个各 ${perPluginMs}ms 的插件耗时 ${elapsedMs}ms，" +
                    "接近串行 ${serialMs}ms；并行应接近 ${perPluginMs}ms",
            )
        } finally {
            view.close()
        }
    }

    /**
     * **单服 60 个插件**的真实规模验收：并发位足够时总耗时约等于单个插件耗时。
     *
     * 这是「服务器多了会卡」的直接对照：串行时 60×T，并行时约 T。
     * 取 T=50ms、60 个插件：串行约 3000ms，并行约 50~200ms（含调度开销）。
     * 阈值取 1000ms——远低于串行的 3000ms，又留足机器抖动的余量。
     */
    @Test
    fun `六十个插件并发总耗时应接近单个插件耗时`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 30_000L)
        val perPluginMs = 50L
        val pluginCount = 60
        try {
            repeat(pluginCount) {
                view.onChange(
                    object : ConfigChangeListener {
                        override fun onConfigChanged(
                            changedDataIds: Set<String>,
                            newMd5: String,
                        ) {
                            Thread.sleep(perPluginMs)
                        }
                    },
                )
            }
            val startedAt = System.nanoTime()
            val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
            val elapsedMs = (System.nanoTime() - startedAt) / 1_000_000

            assertEquals(pluginCount, report.outcomes.size)
            assertTrue(report.outcomes.all { it.ok }, "60 个插件都不该失败")
            val serialMs = perPluginMs * pluginCount
            assertTrue(
                elapsedMs < serialMs / 3,
                "60 个插件未真正并发：耗时 ${elapsedMs}ms，串行会是 ${serialMs}ms，" +
                    "并行应远低于其中的 1/3",
            )
            println("[容量实测] $pluginCount 个插件 × ${perPluginMs}ms：实际 ${elapsedMs}ms（串行约 ${serialMs}ms）")
        } finally {
            view.close()
        }
    }
}

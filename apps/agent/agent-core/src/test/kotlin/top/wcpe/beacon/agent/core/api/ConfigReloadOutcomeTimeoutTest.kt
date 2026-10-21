package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigReloadResult
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.config.EffectiveResult
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * reload 上报结果消费与逐监听器超时的测试（FR-274 §3.2 第 3、4 条）。
 */
class ConfigReloadOutcomeTimeoutTest {
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

    private fun reporting(result: () -> ConfigReloadResult): ReportingConfigChangeListener =
        object : ReportingConfigChangeListener {
            override fun onConfigChangedReporting(
                changedDataIds: Set<String>,
                newMd5: String,
            ): ConfigReloadResult = result()
        }

    /** 上报成功时，插件自报的 md5/版本/条数必须进入聚合结果（供控制面核对版本不符）。 */
    @Test
    fun `上报成功结果被消费`() {
        val view = EffectiveConfigView(store())
        view.onChange(
            reporting {
                ConfigReloadResult.ok()
                    .withAppliedMd5("md5-new")
                    .withAppliedVersion("v7")
                    .withAppliedCount(12)
            },
        )

        val outcome = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single()

        assertTrue(outcome.ok)
        assertEquals("md5-new", outcome.appliedMd5)
        assertEquals("v7", outcome.appliedVersion)
        assertEquals(12, outcome.appliedCount)
        assertFalse(outcome.timeout)
    }

    /** 上报失败时原因进入聚合结果，且计入失败清单。 */
    @Test
    fun `上报失败结果被消费`() {
        val view = EffectiveConfigView(store())
        view.onChange(reporting { ConfigReloadResult.failed("schedule.json 解析失败") })

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(report.hasFailures)
        assertTrue(report.failures.single().reason.contains("解析失败"))
    }

    /** 监听器不返回结果（null）必须按失败处置——缺结果不得被当作成功。 */
    @Test
    fun `返回 null 按失败处置`() {
        val view = EffectiveConfigView(store())
        view.onChange(
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult? = null
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(report.hasFailures)
        assertTrue(report.failures.single().reason.contains("null"))
    }

    /** 未自报 appliedCount 时为 null（区别于「加载了 0 条」）。 */
    @Test
    fun `未自报条数时为 null`() {
        val view = EffectiveConfigView(store())
        view.onChange(reporting { ConfigReloadResult.ok() })

        assertNull(view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single().appliedCount)
    }

    /**
     * 单插件卡死必须被判超时并指明是哪个插件；**同时**正常插件仍照常成功。
     *
     * ## 并行改造后的语义变化（原测试名「独立判定」已不准确）
     *
     * 改造前是串行：提交一个等一个，卡住的那个耗尽超时后，后面那个才开始跑，
     * 于是「卡住的超时、正常的成功」靠的是**先后顺序**。
     *
     * 改造后是真并发：两个同时开始。正常插件会先跑完，卡住的才等到超时。
     * 所以正确断言是「恰好一个超时、恰好一个成功」——而不是靠顺序。
     */
    @Test
    fun `卡死插件超时不影响其余插件成功`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 300)
        val gate = CountDownLatch(1)
        view.onChange(
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult {
                    gate.await(10, TimeUnit.SECONDS) // 远超超时值
                    return ConfigReloadResult.ok()
                }
            },
        )
        view.onChange(reporting { ConfigReloadResult.ok() })

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
        gate.countDown()

        assertEquals(2, report.outcomes.size, "超时不得影响其余监听器被派发")
        // 恰好一个超时（卡死的那个）、恰好一个成功（秒回的那个）。
        assertEquals(1, report.outcomes.count { it.timeout }, "应恰好一个超时；并行下两者同时开始，秒回者不该被误判")
        assertEquals(1, report.outcomes.count { it.ok }, "正常插件必须成功，不得因为别人卡住而被判失败")
        val timedOut = report.outcomes.single { it.timeout }
        assertTrue(timedOut.reason.contains("重载超时"), "超时原因须点明超时，实际：${timedOut.reason}")
        assertEquals(1, report.failures.size)
    }

    @Test
    fun `单监听器超时被独立判定`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 150)
        val gate = CountDownLatch(1)
        view.onChange(
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult {
                    gate.await(10, TimeUnit.SECONDS) // 远超超时值
                    return ConfigReloadResult.ok()
                }
            },
        )
        view.onChange(reporting { ConfigReloadResult.ok() })

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
        gate.countDown()

        assertEquals(2, report.outcomes.size, "超时不得影响其余监听器被派发")
        val timedOut = report.outcomes.single { it.timeout }
        assertTrue(timedOut.reason.contains("重载超时"))
        assertEquals(1, report.failures.size)
        assertEquals(1, report.outcomes.count { it.ok })
    }

    /** 超时值 0（默认）走内联路径：不额外建线程，行为与改造前一致。 */
    @Test
    fun `未配置超时时内联执行`() {
        val view = EffectiveConfigView(store())
        val caller = Thread.currentThread()
        var ranOn: Thread? = null
        view.onChange(
            reporting {
                ranOn = Thread.currentThread()
                ConfigReloadResult.ok()
            },
        )

        assertFalse(view.fireChanged(setOf("liveops/schedule.json"), "md5-new").hasFailures)
        assertEquals(caller, ranOn, "未配置超时时必须在调用线程就地执行")
    }

    /** 配置超时后，监听器在独立线程执行，不占用调用线程。 */
    @Test
    fun `配置超时后切独立线程执行`() {
        val view = EffectiveConfigView(store(), reloadTimeoutMs = 1000)
        val caller = Thread.currentThread()
        var ranOn: Thread? = null
        view.onChange(
            reporting {
                ranOn = Thread.currentThread()
                ConfigReloadResult.ok()
            },
        )

        view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(ranOn != null && ranOn != caller, "启用超时后监听器应在独立线程执行")
        assertTrue(ranOn!!.isDaemon, "重载线程须为守护线程，不得阻止 JVM 退出")
    }
}

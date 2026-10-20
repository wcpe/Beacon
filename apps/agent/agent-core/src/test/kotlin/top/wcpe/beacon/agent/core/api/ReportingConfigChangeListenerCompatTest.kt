package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ConfigReloadResult
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.config.EffectiveResult
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * agent-api 重载结果契约的兼容性测试（FR-274 加性约束的回归锁）。
 *
 * 断言 `ReportingConfigChangeListener` 继承 `ConfigChangeListener` 带来的三点：
 * 老插件的注册路径不变、新插件经旧注册路径仍被派发、默认桥接不破坏既有语义。
 */
class ReportingConfigChangeListenerCompatTest {
    private fun view(): EffectiveConfigView =
        EffectiveConfigView(
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
            },
        )

    /** 新插件可用既有 onChange(ConfigChangeListener) 路径注册并被派发（默认桥接生效）。 */
    @Test
    fun `上报式监听器经旧注册路径仍被派发`() {
        val view = view()
        var reportingCalls = 0

        view.onChange(
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult {
                    reportingCalls++
                    return ConfigReloadResult.ok().withAppliedMd5(newMd5)
                }
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertEquals(1, reportingCalls)
        assertFalse(report.hasFailures)
        assertEquals(1, report.outcomes.size)
    }

    /** 老插件只实现 onConfigChanged，行为与改造前完全一致。 */
    @Test
    fun `老式监听器零改动仍工作`() {
        val view = view()
        var calls = 0

        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    calls++
                }
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertEquals(1, calls)
        assertFalse(report.hasFailures)
    }

    /** 上报式监听器抛异常时按失败计入聚合报告（结果通道不改动异常兜底语义）。 */
    @Test
    fun `上报式监听器抛异常按失败计入`() {
        val view = view()

        view.onChange(
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult = throw UnsupportedOperationException("解析失败")
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(report.hasFailures)
        assertTrue(report.failures.single().reason.contains("解析失败"))
    }

    /** 结果对象为不可变值语义：withXxx 返回新实例，原实例不受影响。 */
    @Test
    fun `结果对象不可变`() {
        val base = ConfigReloadResult.ok().withAppliedMd5("md5-a")
        val enriched = base.withAppliedVersion("v7").withAppliedCount(12)

        assertEquals("md5-a", base.appliedMd5().orElse(null))
        assertFalse(base.appliedVersion().isPresent, "withAppliedVersion 不得改动原实例")
        assertFalse(base.appliedCount().isPresent, "withAppliedCount 不得改动原实例")

        assertEquals("v7", enriched.appliedVersion().orElse(null))
        assertEquals(12, enriched.appliedCount().orElse(-1))
        assertEquals("md5-a", enriched.appliedMd5().orElse(null))
        assertTrue(enriched.isOk)
    }

    /** 失败结果的原因为可读文本，成功结果原因为空串（永不 null）。 */
    @Test
    fun `失败与成功的原因字段语义`() {
        val failed = ConfigReloadResult.failed("schedule.json 解析失败：unexpected token")
        assertFalse(failed.isOk)
        assertEquals("schedule.json 解析失败：unexpected token", failed.reason())

        val ok = ConfigReloadResult.ok()
        assertTrue(ok.isOk)
        assertEquals("", ok.reason())
        assertEquals("", ok.detailJson())
    }
}

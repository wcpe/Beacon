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
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * `EffectiveConfigView` 的前缀过滤派发测试。
 *
 * 锁住三条语义（配套 `docs/SDK.md` §6「关键纪律」）：
 * 1. 过滤发生在**调用监听器之前**——不相关就不调用，这才叫「不被无关唤醒」；
 * 2. 被跳过的监听器**不进 outcomes**——它没执行，不该凭空多出一条成功或失败记录；
 * 3. 前缀为空/null 时行为与老的无前缀注册**完全一致**（含「零变更仍回调」）。
 */
class EffectiveConfigViewPrefixFilterTest {
    private fun store(): EffectiveConfigStore =
        EffectiveConfigStore().apply {
            replace(
                EffectiveResult(
                    namespace = "prod",
                    serverId = "lobby-1",
                    group = "area1",
                    zone = "zoneA",
                    md5 = "md5-new",
                    items =
                        listOf(
                            ConfigItem("plugins/Foo/config.yml", "yaml", "aa", "a: 1"),
                            ConfigItem("plugins/Bar/config.yml", "yaml", "bb", "b: 2"),
                        ),
                ),
            )
        }

    private fun view(
        reloadTimeoutMs: Long = 0,
        store: EffectiveConfigStore = store(),
    ): EffectiveConfigView = EffectiveConfigView(store, reloadTimeoutMs)

    /** 记录每次回调收到的集合。 */
    private class Recorder : ConfigChangeListener {
        val calls = mutableListOf<Set<String>>()

        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            calls += changedDataIds
        }
    }

    /** 前缀命中：回调只拿到匹配项，不夹带别人的路径。 */
    @Test
    fun `前缀命中时只把匹配项交给监听器`() {
        val view = view()
        val recorder = Recorder()
        view.onChange("plugins/Foo/", recorder)

        val report = view.fireChanged(setOf("plugins/Foo/config.yml", "plugins/Bar/config.yml"), "md5-new")

        assertEquals(listOf(setOf("plugins/Foo/config.yml")), recorder.calls)
        assertEquals(1, report.outcomes.size)
        assertFalse(report.hasFailures)
        // 报告层面的变更集合仍是本次全量：报告描述的是「这次变更」，不是「该监听器看到的部分」。
        assertEquals(setOf("plugins/Foo/config.yml", "plugins/Bar/config.yml"), report.changedDataIds)
    }

    /** 前缀不命中：根本不该被调用，也不该产生 outcome。 */
    @Test
    fun `前缀不命中时不调用监听器且不进 outcomes`() {
        val view = view()
        val recorder = Recorder()
        view.onChange("plugins/Foo/", recorder)

        val report = view.fireChanged(setOf("plugins/Bar/config.yml"), "md5-new")

        assertTrue(recorder.calls.isEmpty(), "无关变更不得把插件叫醒")
        assertTrue(report.outcomes.isEmpty(), "没执行过的监听器不该有成功/失败记录")
        assertFalse(report.hasFailures)
        assertEquals("", report.failureSummary())
    }

    /** 前缀命中且该监听器抛异常：仍按单监听器失败计入，且不牵连其余监听器。 */
    @Test
    fun `前缀命中后抛异常仍逐监听器隔离`() {
        val view = view()
        val recorder = Recorder()
        view.onChange(
            "plugins/Foo/",
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    error("Foo 解析失败")
                }
            },
        )
        view.onChange("plugins/Bar/", recorder)

        val report = view.fireChanged(setOf("plugins/Foo/config.yml", "plugins/Bar/config.yml"), "md5-new")

        assertEquals(listOf(setOf("plugins/Bar/config.yml")), recorder.calls, "前者抛异常不得阻断后续监听器")
        assertEquals(2, report.outcomes.size)
        assertEquals(1, report.failures.size)
        assertTrue(report.failures.single().reason.contains("Foo 解析失败"))
    }

    /** 与本次变更无关的监听器若会抛异常，也不该因为「被跳过了」而出现在失败里。 */
    @Test
    fun `被跳过的监听器不产生失败记录`() {
        val view = view()
        view.onChange(
            "plugins/Foo/",
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    error("本不该被调用")
                }
            },
        )

        val report = view.fireChanged(setOf("plugins/Bar/config.yml"), "md5-new")

        assertTrue(report.outcomes.isEmpty())
        assertFalse(report.hasFailures)
    }

    /** 有前缀与无前缀混用：各自拿到该拿的部分，互不影响。 */
    @Test
    fun `前缀监听器与无前缀监听器混用时各取所需`() {
        val view = view()
        val prefixed = Recorder()
        val unfiltered = Recorder()
        view.onChange("plugins/Foo/", prefixed)
        view.onChange(unfiltered)

        view.fireChanged(setOf("plugins/Foo/config.yml", "plugins/Bar/config.yml"), "md5-new")

        assertEquals(listOf(setOf("plugins/Foo/config.yml")), prefixed.calls)
        assertEquals(
            listOf(setOf("plugins/Foo/config.yml", "plugins/Bar/config.yml")),
            unfiltered.calls,
            "无前缀注册的既有行为不变：拿到全量变更集",
        )
    }

    /** 空串前缀 = 不过滤；null 前缀同样不过滤（对齐 `EffectiveConfig` 的契约）。 */
    @Test
    fun `空前缀与 null 前缀等于不过滤`() {
        val view = view()
        val empty = Recorder()
        val nullPrefix = Recorder()
        view.onChange("", empty)
        view.onChange(null, nullPrefix)

        view.fireChanged(setOf("plugins/Bar/config.yml"), "md5-new")

        assertEquals(listOf(setOf("plugins/Bar/config.yml")), empty.calls)
        assertEquals(listOf(setOf("plugins/Bar/config.yml")), nullPrefix.calls)
    }

    /** 老语义回归锁：无前缀注册时即便变更集为空也照常回调一次（前缀过滤不得顺手改掉它）。 */
    @Test
    fun `无前缀注册在零变更时仍照常回调`() {
        val view = view()
        val recorder = Recorder()
        view.onChange(recorder)

        view.fireChanged(emptySet(), "md5-new")

        assertEquals(listOf(emptySet()), recorder.calls)
    }

    /** 有前缀的监听器命中但卡死：超时机制照旧生效（过滤不得绕过执行器）。 */
    @Test
    fun `前缀命中的监听器仍按既有超时机制计失败`() {
        val view = view(reloadTimeoutMs = 50)
        val gate = CountDownLatch(1)
        view.onChange(
            "plugins/Foo/",
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    // 模拟插件卡死；等待被超时 cancel 打断，最长等 5 秒兜底防挂测试进程。
                    gate.await(5, TimeUnit.SECONDS)
                }
            },
        )

        val outcome = view.fireChanged(setOf("plugins/Foo/config.yml"), "md5-new").outcomes.single()
        gate.countDown()

        assertFalse(outcome.ok)
        assertTrue(outcome.timeout)
        assertTrue(outcome.reason.contains("重载超时"))
    }

    /** 上报式监听器同样吃前缀过滤：不相关时不执行，故不会误报「未加载」。 */
    @Test
    fun `上报式监听器不被无关变更触发`() {
        val view = view()
        var calls = 0
        view.onChange(
            "plugins/Foo/",
            object : ReportingConfigChangeListener {
                override fun onConfigChangedReporting(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ): ConfigReloadResult {
                    calls++
                    return ConfigReloadResult.failed("Foo 未加载")
                }
            },
        )

        val report = view.fireChanged(setOf("plugins/Bar/config.yml"), "md5-new")

        assertEquals(0, calls)
        assertFalse(report.hasFailures, "无关变更不得因该插件的失败结果把整批打挂")
    }

    /** 注销句柄对带前缀的注册同样生效。 */
    @Test
    fun `带前缀的注册注销后不再派发`() {
        val view = view()
        val recorder = Recorder()
        val handle = view.onChange("plugins/Foo/", recorder)

        view.fireChanged(setOf("plugins/Foo/config.yml"), "md5-new")
        handle.remove()
        view.fireChanged(setOf("plugins/Foo/config.yml"), "md5-new")

        assertEquals(1, recorder.calls.size)
    }
}

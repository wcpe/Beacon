package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.config.EffectiveResult
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * EffectiveConfigView.fireChanged 的派发语义测试（P0 缺陷回归锁）。
 *
 * 覆盖 `docs/specs/delivery-reload-ack.md` §3.2 的两条刻意设计：
 * 逐监听器隔离、catch Throwable（而非 Exception）。
 */
class EffectiveConfigViewFireChangedTest {
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

    /** 一个监听器抛异常后，其余监听器仍必须收到通知（原裸循环实现的缺陷）。 */
    @Test
    fun `单监听器抛异常不阻断其余监听器`() {
        val view = view()
        val received = mutableListOf<String>()

        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    error("插件 A 解析失败")
                }
            },
        )
        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    received += "b"
                }
            },
        )
        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    received += "c"
                }
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertEquals(3, report.outcomes.size)
        assertEquals(2, received.size, "抛异常者之后的监听器必须仍被派发")
        assertTrue(report.hasFailures)
        assertEquals(1, report.failures.size)
        assertTrue(report.failures.single().reason.contains("插件 A 解析失败"))
    }

    /**
     * Error（非 Exception）同样必须被兜住：插件侧 agent-api 版本不匹配会抛
     * NoClassDefFoundError / LinkageError，只 catch Exception 会让异常逃逸并打死交付线程。
     */
    @Test
    fun `监听器抛 Error 也被兜住`() {
        val view = view()
        val received = mutableListOf<String>()

        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    throw NoClassDefFoundError("top/wcpe/beacon/agent/api/NewType")
                }
            },
        )
        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    received += "b"
                }
            },
        )

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(report.hasFailures)
        assertTrue(report.failures.single().reason.startsWith("NoClassDefFoundError"))
        assertEquals(listOf("b"), received, "Error 不得逃逸阻断后续监听器")
    }

    /** 全部成功时无失败清单、摘要为空串。 */
    @Test
    fun `全部成功时报告无失败`() {
        val view = view()
        repeat(3) {
            view.onChange(
                object : ConfigChangeListener {
                    override fun onConfigChanged(
                        changedDataIds: Set<String>,
                        newMd5: String,
                    ) = Unit
                },
            )
        }

        val report = view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertEquals(3, report.outcomes.size)
        assertFalse(report.hasFailures)
        assertTrue(report.failures.isEmpty())
        assertEquals("", report.failureSummary())
    }

    /** 无监听器时派发为空操作，不报错。 */
    @Test
    fun `无监听器时为空报告`() {
        val report = view().fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertTrue(report.outcomes.isEmpty())
        assertFalse(report.hasFailures)
        assertEquals("md5-new", report.newMd5)
    }

    /** 注销后的监听器不再收到通知（onChange 返回的句柄语义不变）。 */
    @Test
    fun `注销后不再派发`() {
        val view = view()
        var count = 0
        val handle =
            view.onChange(
                object : ConfigChangeListener {
                    override fun onConfigChanged(
                        changedDataIds: Set<String>,
                        newMd5: String,
                    ) {
                        count++
                    }
                },
            )

        view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
        handle.remove()
        view.fireChanged(setOf("liveops/schedule.json"), "md5-new")

        assertEquals(1, count)
    }

    /** 失败摘要含失败计数与插件标识，供回执 error 与日志直接使用。 */
    @Test
    fun `失败摘要含计数与插件标识`() {
        val view = view()
        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) {
                    throw IllegalArgumentException("bad json")
                }
            },
        )
        view.onChange(
            object : ConfigChangeListener {
                override fun onConfigChanged(
                    changedDataIds: Set<String>,
                    newMd5: String,
                ) = Unit
            },
        )

        val summary = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").failureSummary()

        assertTrue(summary.startsWith("1/2 插件重载失败："))
        assertTrue(summary.contains("IllegalArgumentException: bad json"))
    }
}

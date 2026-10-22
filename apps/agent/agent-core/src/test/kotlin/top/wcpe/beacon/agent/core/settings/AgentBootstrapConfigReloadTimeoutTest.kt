package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * AgentBootstrap 读取「单插件配置重载超时」（FR-274）的单测。
 *
 * 这条锁的存在理由：`configReloadTimeoutMs` 曾被壳层读取却**没在 AgentBootstrap 里接上配置键**，
 * 于是它永远取默认值，运维配了也不生效且毫无提示。本测试防该回归。
 *
 * **默认值已从 0 改为 30000（FR-280）**：默认 0 意味着开箱即用时没有任何超时保护——
 * 一个卡死插件会永久卡住交付命令链，只能等控制面整体超时，回执里看不出是哪个插件。
 * 该改动是刻意的行为变更，故本测试的期望值一并更新。
 */
class AgentBootstrapConfigReloadTimeoutTest {
    @Test
    fun `缺省时超时为 30000 即开箱即有保护`() {
        val settings = AgentBootstrap.readSettings(MapConfigReader(emptyMap()))
        assertEquals(30_000L, settings.configReloadTimeoutMs)
    }

    @Test
    fun `显式配置 0 时关闭超时保护`() {
        // 仍允许运维显式关掉（例如插件自己有更强的超时），只是不再是默认。
        val reader = MapConfigReader(mapOf("delivery.config-reload-timeout-ms" to 0L))
        assertEquals(0L, AgentBootstrap.readSettings(reader).configReloadTimeoutMs)
    }

    @Test
    fun `并发数缺省为 64 且被收口到合理范围`() {
        assertEquals(64, AgentBootstrap.readSettings(MapConfigReader(emptyMap())).configReloadPoolSize)
        // 非法值不得让池无法工作（0 / 负数）或放大卡死线程泄漏面（过大）。
        val zero = MapConfigReader(mapOf("delivery.reload-pool-size" to 0))
        assertEquals(1, AgentBootstrap.readSettings(zero).configReloadPoolSize)
        val huge = MapConfigReader(mapOf("delivery.reload-pool-size" to 9999))
        assertEquals(256, AgentBootstrap.readSettings(huge).configReloadPoolSize)
    }

    @Test
    fun `显式配置读取 delivery config-reload-timeout-ms`() {
        val reader =
            MapConfigReader(
                mapOf(
                    "beacon.endpoints" to listOf("http://127.0.0.1:8848"),
                    "delivery.config-reload-timeout-ms" to 30_000L,
                ),
            )
        val settings = AgentBootstrap.readSettings(reader)
        assertEquals(30_000L, settings.configReloadTimeoutMs)
    }

    @Test
    fun `越界值被收口且必须告警`() {
        val warnings = mutableListOf<String>()
        val reader =
            MapConfigReader(
                mapOf(
                    "delivery.config-reload-timeout-ms" to -5L,
                    "delivery.reload-pool-size" to 9999,
                ),
            )

        val settings = AgentBootstrap.readSettings(reader, warnings::add)

        // 回归锁：越界照样收口到合法区间（FR-280），不抛异常、不让 agent 起不来。
        assertEquals(0L, settings.configReloadTimeoutMs, "负超时应收口到下限 0")
        assertEquals(256, settings.configReloadPoolSize, "9999 应收口到上限 256")
        // 但**不能静默收口**（FR-280 #3）：手写的越界值被改掉却不吭一声，运维会照着原值排错。
        assertEquals(2, warnings.size, "两个越界项都该告警，实际：$warnings")
        assertTrue(
            warnings.any { it.contains("delivery.config-reload-timeout-ms") && it.contains("-5") && it.contains("0") },
            "超时告警须含键名、原值、生效值，实际：$warnings",
        )
        assertTrue(
            warnings.any { it.contains("delivery.reload-pool-size") && it.contains("9999") && it.contains("256") },
            "并发告警须含键名、原值、生效值，实际：$warnings",
        )
    }

    @Test
    fun `扫描间隔低于下限时收口并告警`() {
        val warnings = mutableListOf<String>()
        val reader = MapConfigReader(mapOf("assets.scan-interval-sec" to 10L))

        val settings = AgentBootstrap.readSettings(reader, warnings::add)

        assertEquals(300L, settings.assets.scanIntervalSec, "低于下限 300 秒应收口")
        assertEquals(1, warnings.size, "收口必须告警，实际：$warnings")
        // 该项只有下限，不该打印一串天文数字的上限。
        assertTrue(warnings.single().contains("≥300"), "区间文案应只表达下限，实际：${warnings.single()}")
    }

    @Test
    fun `合法值不告警`() {
        val warnings = mutableListOf<String>()
        val reader =
            MapConfigReader(
                mapOf(
                    "delivery.config-reload-timeout-ms" to 15_000L,
                    "delivery.reload-pool-size" to 8,
                    "assets.scan-interval-sec" to 600L,
                ),
            )

        val settings = AgentBootstrap.readSettings(reader, warnings::add)

        // 合法值照原样生效，且**不打扰**：告警只在真越界时才有意义（否则会被训练成无视）。
        assertEquals(15_000L, settings.configReloadTimeoutMs)
        assertEquals(8, settings.configReloadPoolSize)
        assertEquals(600L, settings.assets.scanIntervalSec)
        assertTrue(warnings.isEmpty(), "合法值不该告警，实际：$warnings")
    }

    @Test
    fun `收口区间边界值本身合法且不告警`() {
        val warnings = mutableListOf<String>()
        val atBounds =
            MapConfigReader(
                mapOf(
                    "delivery.config-reload-timeout-ms" to 0L,
                    "delivery.reload-pool-size" to 1,
                    "assets.scan-interval-sec" to 300L,
                ),
            )
        val settings = AgentBootstrap.readSettings(atBounds, warnings::add)

        // 边界值属于合法范围：收口不能把合法值也当成越界（会产生假告警）。
        assertEquals(0L, settings.configReloadTimeoutMs)
        assertEquals(1, settings.configReloadPoolSize)
        assertEquals(300L, settings.assets.scanIntervalSec)
        assertTrue(warnings.isEmpty(), "边界值合法，不该告警，实际：$warnings")
    }

    /** 测试用 ConfigReader：从 map 取值，缺失返回默认。 */
    private class MapConfigReader(private val values: Map<String, Any?>) : ConfigReader {
        override fun string(
            path: String,
            default: String,
        ): String = (values[path] as? String) ?: default

        override fun int(
            path: String,
            default: Int,
        ): Int = (values[path] as? Number)?.toInt() ?: default

        override fun long(
            path: String,
            default: Long,
        ): Long = (values[path] as? Number)?.toLong() ?: default

        override fun double(
            path: String,
            default: Double,
        ): Double = (values[path] as? Number)?.toDouble() ?: default

        override fun boolean(
            path: String,
            default: Boolean,
        ): Boolean = (values[path] as? Boolean) ?: default

        @Suppress("UNCHECKED_CAST")
        override fun stringList(path: String): List<String> = (values[path] as? List<String>) ?: emptyList()

        override fun keys(path: String): Set<String> = emptySet()
    }
}

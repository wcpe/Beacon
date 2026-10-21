package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals

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

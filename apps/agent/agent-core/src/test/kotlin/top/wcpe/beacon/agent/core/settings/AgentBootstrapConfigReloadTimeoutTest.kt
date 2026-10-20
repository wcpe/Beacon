package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * AgentBootstrap 读取「单插件配置重载超时」（FR-274）的单测。
 *
 * 这条锁的存在理由：`configReloadTimeoutMs` 曾被壳层读取却**没在 AgentBootstrap 里接上配置键**，
 * 于是它永远取默认值 0（= 不设超时），运维配了也不生效且毫无提示。本测试防该回归。
 */
class AgentBootstrapConfigReloadTimeoutTest {
    @Test
    fun `缺省时超时为 0 即不设超时`() {
        val settings = AgentBootstrap.readSettings(MapConfigReader(emptyMap()))
        assertEquals(0L, settings.configReloadTimeoutMs)
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

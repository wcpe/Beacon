package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals

/** AgentBootstrap 读取 BC 代理 home-zone（FR-48）本地参数的单测。 */
class AgentBootstrapProxyTest {

    @Test
    fun `缺省时 home-zone 为空走兜底`() {
        val settings = AgentBootstrap.readSettings(MapConfigReader(emptyMap()))
        assertEquals("", settings.proxy.homeGroup)
        assertEquals("", settings.proxy.homeZone)
    }

    @Test
    fun `显式配置读取 home-group 与 home-zone`() {
        val reader = MapConfigReader(
            mapOf(
                "beacon.endpoints" to listOf("http://127.0.0.1:8848"),
                "proxy.home-group" to "area1",
                "proxy.home-zone" to "zoneA",
            ),
        )
        val settings = AgentBootstrap.readSettings(reader)
        assertEquals("area1", settings.proxy.homeGroup)
        assertEquals("zoneA", settings.proxy.homeZone)
    }

    /** 测试用 ConfigReader：从 map 取值，缺失返回默认。 */
    private class MapConfigReader(private val values: Map<String, Any?>) : ConfigReader {
        override fun string(path: String, default: String): String = (values[path] as? String) ?: default
        override fun int(path: String, default: Int): Int = (values[path] as? Number)?.toInt() ?: default
        override fun long(path: String, default: Long): Long = (values[path] as? Number)?.toLong() ?: default
        override fun double(path: String, default: Double): Double = (values[path] as? Number)?.toDouble() ?: default
        override fun boolean(path: String, default: Boolean): Boolean = (values[path] as? Boolean) ?: default

        @Suppress("UNCHECKED_CAST")
        override fun stringList(path: String): List<String> = (values[path] as? List<String>) ?: emptyList()

        override fun keys(path: String): Set<String> = emptySet()
    }
}

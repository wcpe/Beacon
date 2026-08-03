package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** AgentBootstrap 读取跨服消息（FR-26）本地参数的单测。 */
class AgentBootstrapMessagingTest {
    @Test
    fun `缺省时消息模块关闭且取默认值`() {
        val settings = AgentBootstrap.readSettings(MapConfigReader(emptyMap()))
        assertFalse(settings.messaging.enabled)
        assertEquals(5000, settings.messaging.rpcTimeoutMs)
    }

    @Test
    fun `显式配置覆盖消息参数`() {
        val reader =
            MapConfigReader(
                mapOf(
                    "beacon.endpoints" to listOf("http://127.0.0.1:8848"),
                    "messaging.enabled" to true,
                    "messaging.rpc-timeout-ms" to 3000L,
                ),
            )
        val settings = AgentBootstrap.readSettings(reader)
        assertTrue(settings.messaging.enabled)
        assertEquals(3000, settings.messaging.rpcTimeoutMs)
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

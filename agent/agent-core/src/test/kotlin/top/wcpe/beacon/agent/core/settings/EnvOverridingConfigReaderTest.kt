package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * EnvOverridingConfigReader 单测（FR-33）：env 覆盖优先、命名映射、列表分隔、
 * 缺失 / 空串 / 解析失败回落文件值、keys 始终委托文件。
 */
class EnvOverridingConfigReaderTest {

    @Test
    fun `env 存在时覆盖文件标量值`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf(
                "BEACON_AGENT_IDENTITY_SERVER_ID" to "env-server",
                "BEACON_AGENT_IDENTITY_CAPACITY" to "500",
                "BEACON_AGENT_TIMING_POLL_TIMEOUT_MS" to "12345",
                "BEACON_AGENT_BACKOFF_MULTIPLIER" to "3.5",
                "BEACON_AGENT_SNAPSHOT_ENABLED" to "true",
            ),
        )
        // identity.server-id → BEACON_AGENT_IDENTITY_SERVER_ID（验命名：点与连字符转下划线、大写）。
        assertEquals("env-server", reader.string("identity.server-id", "def"))
        assertEquals(500, reader.int("identity.capacity", 0))
        assertEquals(12345L, reader.long("timing.poll-timeout-ms", 30000))
        assertEquals(3.5, reader.double("backoff.multiplier", 2.0))
        assertTrue(reader.boolean("snapshot.enabled", false)) // 文件值为 false，env=true 证明覆盖
    }

    @Test
    fun `env 缺失时回落文件值`() {
        val reader = EnvOverridingConfigReader(FixedConfigReader(), envOf())
        assertEquals("file-value", reader.string("identity.server-id", "def"))
        assertEquals(999, reader.int("identity.capacity", 0))
        assertEquals(999L, reader.long("timing.poll-timeout-ms", 30000))
        assertFalse(reader.boolean("snapshot.enabled", true)) // 文件值 false
    }

    @Test
    fun `env 为空串视为未设置回落文件值`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf("BEACON_AGENT_IDENTITY_SERVER_ID" to ""),
        )
        assertEquals("file-value", reader.string("identity.server-id", "def"))
    }

    @Test
    fun `env 解析失败回落文件值`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf(
                "BEACON_AGENT_TIMING_POLL_TIMEOUT_MS" to "not-a-number",
                "BEACON_AGENT_SNAPSHOT_ENABLED" to "maybe",
            ),
        )
        assertEquals(999L, reader.long("timing.poll-timeout-ms", 30000)) // 文件值
        assertFalse(reader.boolean("snapshot.enabled", true)) // 文件值 false
    }

    @Test
    fun `列表 env 按逗号分隔去空白去空项`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf("BEACON_AGENT_BEACON_ENDPOINTS" to " http://a , http://b ,, http://c "),
        )
        assertEquals(listOf("http://a", "http://b", "http://c"), reader.stringList("beacon.endpoints"))
    }

    @Test
    fun `列表 env 缺失回落文件列表`() {
        val reader = EnvOverridingConfigReader(FixedConfigReader(), envOf())
        assertEquals(listOf("file-a", "file-b"), reader.stringList("beacon.endpoints"))
    }

    @Test
    fun `布尔 env 大小写不敏感`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf("BEACON_AGENT_SNAPSHOT_ENABLED" to "TRUE"),
        )
        assertTrue(reader.boolean("snapshot.enabled", false))
    }

    @Test
    fun `keys 始终委托文件不受 env 影响`() {
        val reader = EnvOverridingConfigReader(
            FixedConfigReader(),
            envOf("BEACON_AGENT_IDENTITY_METADATA_REGION" to "env-region"),
        )
        // metadata 动态键 map 本版不支持 env 覆盖，keys 委托文件。
        assertEquals(setOf("region"), reader.keys("identity.metadata"))
    }

    /** 构造 env 查找函数（大写变量名 → 值）。 */
    private fun envOf(vararg pairs: Pair<String, String>): (String) -> String? {
        val map = pairs.toMap()
        return { map[it] }
    }

    /** 固定返回「文件值」的 delegate，便于断言 env 是否真覆盖了它。 */
    private class FixedConfigReader : ConfigReader {
        override fun string(path: String, default: String): String = "file-value"
        override fun int(path: String, default: Int): Int = 999
        override fun long(path: String, default: Long): Long = 999L
        override fun double(path: String, default: Double): Double = 9.99
        override fun boolean(path: String, default: Boolean): Boolean = false
        override fun stringList(path: String): List<String> = listOf("file-a", "file-b")
        override fun keys(path: String): Set<String> = setOf("region")
    }
}

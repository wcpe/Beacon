package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.identity.AgentCredentials
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import java.nio.file.Files
import java.nio.file.Path
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 身份解析与生效设置的交界（FR-276 #3 回归锁）。
 *
 * 身份收敛后，启动流程会**主动**写出一份「只有身份、没有地址与令牌」的凭据文件。
 * 这里有两条既有行为不能因此改变：
 *  1. config.yml 配好 endpoints + token 的老部署必须照旧能连；
 *  2. 凭据里可连接的地址与令牌必须仍然补进设置（零配置 join 的成果不能被空值覆盖）。
 */
class EffectiveSettingsIdentityTest {
    private fun tempDir(): Path = Files.createTempDirectory("beacon-settings-identity")

    @Test
    fun `身份迁移产生的空凭据不影响 configyml 既有接入`() {
        val dir = tempDir()
        val legacy = AgentIdentityStore(dir).loadOrCreate()
        // 模拟启动：先把旧身份迁进凭据（迁移只搬身份，地址与令牌为空）。
        AgentCredentialsStore(dir, TestJsonCodec).resolveIdentityId(legacy)
        val resolver = resolver(dir, localConnectable)

        val settings = resolver.resolve()

        assertEquals(listOf("https://beacon.example.com"), settings.endpoints)
        assertEquals("bn_local", settings.bootstrapToken)
        assertTrue(resolver.hasConnectionConfig(settings), "config.yml 配好地址与令牌就必须仍可接入")
    }

    @Test
    fun `凭据里的地址与令牌补进未配置的本地设置`() {
        val dir = tempDir()
        val credentials = AgentCredentialsStore(dir, TestJsonCodec)
        credentials.save(AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://joined.example.com", "bn_joined"))
        val resolver = resolver(dir, MapConfigReader(emptyMap()))

        val settings = resolver.resolve()

        // join 的成果靠这一层合并生效；少了它，/beacon join 之后仍然连不上。
        assertEquals(listOf("https://joined.example.com"), settings.endpoints)
        assertEquals("bn_joined", settings.bootstrapToken)
        assertTrue(resolver.hasConnectionConfig(settings))
    }

    @Test
    fun `configyml 显式配置优先于凭据`() {
        val dir = tempDir()
        AgentCredentialsStore(dir, TestJsonCodec)
            .save(AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://joined.example.com", "bn_joined"))
        val resolver = resolver(dir, localConnectable)

        val settings = resolver.resolve()

        // 运维手写的部署参数优先，是旧部署路径的兼容保证。
        assertEquals(listOf("https://beacon.example.com"), settings.endpoints)
        assertEquals("bn_local", settings.bootstrapToken)
    }

    @Test
    fun `既无本地配置也无凭据时只提示不报错`() {
        val dir = tempDir()
        val resolver = resolver(dir, MapConfigReader(emptyMap()))

        val settings = resolver.resolve()

        assertEquals(emptyList(), settings.endpoints)
        assertFalse(resolver.hasConnectionConfig(settings), "未接入是零配置下的正常状态，不进入重连循环")
    }

    private fun resolver(
        path: Path,
        local: ConfigReader,
    ): EffectiveSettingsResolver =
        EffectiveSettingsResolver(
            localReader = local,
            pushedStore = PushedConfigStore(path, TestJsonCodec),
            credentialsStore = AgentCredentialsStore(path, TestJsonCodec),
            warn = {},
            info = {},
        )

    /** 老部署形态：config.yml 手写地址与令牌。 */
    private val localConnectable: ConfigReader =
        MapConfigReader(
            mapOf(
                "beacon.endpoints" to listOf("https://beacon.example.com"),
                "beacon.bootstrap-token" to "bn_local",
            ),
        )

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

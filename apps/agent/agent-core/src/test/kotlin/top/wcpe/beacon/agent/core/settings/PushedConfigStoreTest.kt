package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import java.nio.file.Files
import java.nio.file.Path
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 下发配置缓存与三层优先级的测试（FR-277）。
 *
 * 这两块合起来测，是因为它们的价值只在**配合**时成立：
 * 单测缓存读写只证明"能存能取"，而真正要保证的是
 * 「断连时用缓存而不是退回默认」，以及「下发值非法时不把 agent 弄坏」。
 */
class PushedConfigStoreTest {
    private fun tempDir(): Path = Files.createTempDirectory("beacon-pushed-test")

    @Test
    fun `下发值写入后断连仍可按缓存读出`() {
        val dir = tempDir()
        val store = PushedConfigStore(dir, TestJsonCodec)
        store.update(
            values = mapOf("delivery.config-reload-timeout-ms" to "30000"),
            namespace = "prod",
            serverId = "lobby-1",
        )

        // 模拟「控制面不可达」：只读本地缓存，不碰网络。
        val reloaded = PushedConfigStore(dir, TestJsonCodec).load()

        assertEquals("30000", reloaded.values["delivery.config-reload-timeout-ms"])
        assertEquals("prod", reloaded.namespace)
        assertEquals("lobby-1", reloaded.serverId)
        assertTrue(reloaded.fetchedAt != null, "应记录拉取时刻，便于诊断缓存有多旧")
    }

    @Test
    fun `缓存缺失时视为没下发过而不是报错`() {
        val store = PushedConfigStore(tempDir(), TestJsonCodec)
        val loaded = store.load()

        assertTrue(loaded.values.isEmpty())
        assertEquals("", loaded.namespace)
    }

    @Test
    fun `缓存损坏时降级为空而不是让 agent 起不来`() {
        val dir = tempDir()
        Files.writeString(dir.resolve("pushed-config.json"), "{ 坏掉的")
        val store = PushedConfigStore(dir, TestJsonCodec)

        // 关键：损坏缓存只意味着「没有下发值可用」，绝不抛异常阻止启动。
        assertTrue(store.load().values.isEmpty())
    }

    @Test
    fun `三层优先级为下发大于本地大于默认`() {
        val local = MapConfigReader(mapOf("delivery.config-reload-timeout-ms" to 5000L))
        val reader =
            CascadingConfigReader(
                pushed = mapOf("delivery.config-reload-timeout-ms" to "30000"),
                local = local,
            )

        // 下发生效
        assertEquals(30000L, reader.long("delivery.config-reload-timeout-ms", 0))
        // 本地无该项时用默认
        assertEquals(1L, reader.long("delivery.reload-pool-size", 1L))
    }

    @Test
    fun `下发值为空串时回退到本地`() {
        val local = MapConfigReader(mapOf("delivery.config-reload-timeout-ms" to 5000L))
        val reader =
            CascadingConfigReader(
                pushed = mapOf("delivery.config-reload-timeout-ms" to "   "),
                local = local,
            )

        // 空串等于「没下发」：运维清空该项时应回落到本地，而不是读成 0。
        assertEquals(5000L, reader.long("delivery.config-reload-timeout-ms", 0))
    }

    @Test
    fun `下发值非法时回退并上报而不是抛异常`() {
        val invalid = mutableListOf<String>()
        val local = MapConfigReader(mapOf("delivery.config-reload-timeout-ms" to 5000L))
        val reader =
            CascadingConfigReader(
                pushed = mapOf("delivery.config-reload-timeout-ms" to "三十秒"),
                local = local,
                onInvalidPushed = { key, _, _ -> invalid += key },
            )

        // 关键：一个手误填错的下发值不该让整台服的 agent 变成砖。
        assertEquals(5000L, reader.long("delivery.config-reload-timeout-ms", 0))
        assertEquals(listOf("delivery.config-reload-timeout-ms"), invalid, "非法值必须被告警，不能静默忽略")
    }

    @Test
    fun `下发布尔与列表也遵守三层优先级`() {
        val local = MapConfigReader(emptyMap())
        val reader =
            CascadingConfigReader(
                pushed = mapOf("override.command-whitelist" to "taboolib, reload", "file-tree.enabled" to "false"),
                local = local,
            )

        assertEquals(setOf("taboolib", "reload"), reader.stringList("override.command-whitelist").toSet())
        assertEquals(false, reader.boolean("file-tree.enabled", true))
    }

    /** 测试用的最小 ConfigReader：只需覆盖被读到的几个方法。 */
    private class MapConfigReader(
        private val values: Map<String, Any>,
    ) : ConfigReader {
        override fun string(
            path: String,
            default: String,
        ): String = values[path] as? String ?: default

        override fun int(
            path: String,
            default: Int,
        ): Int = values[path] as? Int ?: default

        override fun long(
            path: String,
            default: Long,
        ): Long = values[path] as? Long ?: default

        override fun double(
            path: String,
            default: Double,
        ): Double = values[path] as? Double ?: default

        override fun boolean(
            path: String,
            default: Boolean,
        ): Boolean = values[path] as? Boolean ?: default

        override fun stringList(path: String): List<String> = (values[path] as? List<*>)?.filterIsInstance<String>() ?: emptyList()

        override fun keys(path: String): Set<String> = emptySet()
    }
}

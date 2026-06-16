package top.wcpe.beacon.agent.core.config

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** EffectiveConfigStore 读写与拷贝语义的单元测试。 */
class EffectiveConfigStoreTest {

    private fun result(md5: String, vararg items: ConfigItem) = EffectiveResult(
        namespace = "prod",
        serverId = "lobby-1",
        group = "area1",
        zone = "zoneA",
        md5 = md5,
        items = items.toList(),
    )

    @Test
    fun `初始为空`() {
        val store = EffectiveConfigStore()
        assertNull(store.currentMd5())
        assertTrue(store.dataIds().isEmpty())
    }

    @Test
    fun `replace 后可读 md5 group zone 与项`() {
        val store = EffectiveConfigStore()
        store.replace(result("abc", ConfigItem("mysql.yml", "yaml", "9f", "url: jdbc")))
        assertEquals("abc", store.currentMd5())
        assertEquals("area1", store.currentGroup())
        assertEquals("zoneA", store.currentZone())
        assertEquals(listOf("mysql.yml"), store.dataIds())
        assertEquals("url: jdbc", store.item("mysql.yml")?.content)
    }

    @Test
    fun `replace 整体替换而非合并`() {
        val store = EffectiveConfigStore()
        store.replace(result("v1", ConfigItem("a.yml", "yaml", "1", "x")))
        store.replace(result("v2", ConfigItem("b.yml", "yaml", "2", "y")))
        // 旧 dataId 应被整体替换掉。
        assertNull(store.item("a.yml"))
        assertEquals(listOf("b.yml"), store.dataIds())
    }

    @Test
    fun `snapshotItems 返回拷贝不随后续 replace 变化`() {
        val store = EffectiveConfigStore()
        store.replace(result("v1", ConfigItem("a.yml", "yaml", "1", "x")))
        val snap = store.snapshotItems()
        store.replace(result("v2", ConfigItem("b.yml", "yaml", "2", "y")))
        // 之前取的快照不应受影响。
        assertEquals(listOf("a.yml"), snap.map { it.dataId })
    }

    @Test
    fun `zone 未指派时为 null`() {
        val store = EffectiveConfigStore()
        store.replace(
            EffectiveResult("prod", "lobby-1", "area1", null, "m", emptyList()),
        )
        assertNull(store.currentZone())
    }
}

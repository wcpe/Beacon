package com.beacon.agent.adapters

import com.beacon.agent.adapters.testutil.RecordingPlatformAdapter
import com.beacon.agent.core.config.ConfigApplier
import com.beacon.agent.core.config.ConfigItem
import com.beacon.agent.core.config.EffectiveConfigStore
import com.beacon.agent.core.config.EffectiveResult
import com.beacon.agent.core.snapshot.SnapshotStore
import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** ConfigApplier md5 守卫：同 md5 不 publish / 不写快照；不同则 publish + 写快照。 */
class ConfigApplierTest {

    private val codec = KotlinxJsonCodec()
    private val dir: File = Files.createTempDirectory("beacon-applier").toFile()
    private val snapshotFile = File(dir, "snap.json")

    @AfterTest
    fun cleanup() {
        dir.deleteRecursively()
    }

    private fun result(md5: String, vararg items: ConfigItem) =
        EffectiveResult("prod", "lobby-1", "area1", "zoneA", md5, items.toList())

    @Test
    fun `首次 apply 发布变更并写快照`() {
        val store = EffectiveConfigStore()
        val adapter = RecordingPlatformAdapter(dir)
        val applier = ConfigApplier(store, SnapshotStore(snapshotFile, codec), adapter)

        val changed = applier.apply(result("md5-a", ConfigItem("a.yml", "yaml", "1", "x")))

        assertTrue(changed)
        assertEquals(1, adapter.published.size)
        assertEquals(setOf("a.yml"), adapter.published[0].first)
        assertEquals("md5-a", adapter.published[0].second)
        assertTrue(snapshotFile.exists(), "应已写快照")
        assertEquals("md5-a", store.currentMd5())
    }

    @Test
    fun `相同 md5 再次 apply 被守卫跳过`() {
        val store = EffectiveConfigStore()
        val adapter = RecordingPlatformAdapter(dir)
        val applier = ConfigApplier(store, SnapshotStore(snapshotFile, codec), adapter)

        applier.apply(result("md5-a", ConfigItem("a.yml", "yaml", "1", "x")))
        snapshotFile.delete() // 删快照以验证第二次不会重写
        val changedAgain = applier.apply(result("md5-a", ConfigItem("a.yml", "yaml", "1", "x")))

        assertFalse(changedAgain)
        // 守卫跳过：不重复发布、不重写快照。
        assertEquals(1, adapter.published.size)
        assertFalse(snapshotFile.exists(), "相同 md5 不应重写快照")
    }

    @Test
    fun `不同 md5 计算变更集（新增 变化 删除）`() {
        val store = EffectiveConfigStore()
        val adapter = RecordingPlatformAdapter(dir)
        val applier = ConfigApplier(store, SnapshotStore(snapshotFile, codec), adapter)

        // 初始：a(1), b(1)
        applier.apply(
            result("m1", ConfigItem("a.yml", "yaml", "1", "x"), ConfigItem("b.yml", "yaml", "1", "y")),
        )
        // 新态：a(1 不变), b(2 变化), c(新增)；删除无（保留 a/b）→ 变更 = b, c
        applier.apply(
            result(
                "m2",
                ConfigItem("a.yml", "yaml", "1", "x"),
                ConfigItem("b.yml", "yaml", "2", "y2"),
                ConfigItem("c.yml", "yaml", "1", "z"),
            ),
        )

        assertEquals(setOf("b.yml", "c.yml"), adapter.published[1].first)
    }

    @Test
    fun `删除的 dataId 计入变更集`() {
        val store = EffectiveConfigStore()
        val adapter = RecordingPlatformAdapter(dir)
        val applier = ConfigApplier(store, SnapshotStore(snapshotFile, codec), adapter)

        applier.apply(
            result("m1", ConfigItem("a.yml", "yaml", "1", "x"), ConfigItem("b.yml", "yaml", "1", "y")),
        )
        // 新态只剩 a → b 被删除。
        applier.apply(result("m2", ConfigItem("a.yml", "yaml", "1", "x")))

        assertTrue(adapter.published[1].first.contains("b.yml"))
    }
}

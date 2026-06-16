package top.wcpe.beacon.agent.core.filetree

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/** FileSyncer manifest 差分纯逻辑穷举单测：增 / 改 / 删 / 无变更。 */
class FileSyncerTest {

    @Test
    fun `首启本地空 全部为新增`() {
        val plan = FileSyncer.diff(
            applied = emptyMap(),
            target = mapOf("a.yml" to "1", "dir/b.js" to "2"),
        )
        assertEquals(setOf("a.yml", "dir/b.js"), plan.toAdd)
        assertTrue(plan.toUpdate.isEmpty())
        assertTrue(plan.toDelete.isEmpty())
    }

    @Test
    fun `md5 不同为更新`() {
        val plan = FileSyncer.diff(
            applied = mapOf("a.yml" to "old"),
            target = mapOf("a.yml" to "new"),
        )
        assertTrue(plan.toAdd.isEmpty())
        assertEquals(setOf("a.yml"), plan.toUpdate)
        assertTrue(plan.toDelete.isEmpty())
    }

    @Test
    fun `目标已无为删除`() {
        val plan = FileSyncer.diff(
            applied = mapOf("a.yml" to "1", "b.yml" to "2"),
            target = mapOf("a.yml" to "1"),
        )
        assertTrue(plan.toAdd.isEmpty())
        assertTrue(plan.toUpdate.isEmpty())
        assertEquals(setOf("b.yml"), plan.toDelete)
    }

    @Test
    fun `md5 相同跳过 无变更`() {
        val plan = FileSyncer.diff(
            applied = mapOf("a.yml" to "1", "b.yml" to "2"),
            target = mapOf("a.yml" to "1", "b.yml" to "2"),
        )
        assertTrue(plan.isEmpty())
    }

    @Test
    fun `增改删混合`() {
        val plan = FileSyncer.diff(
            applied = mapOf("keep.yml" to "1", "change.yml" to "old", "gone.yml" to "3"),
            target = mapOf("keep.yml" to "1", "change.yml" to "new", "added.yml" to "4"),
        )
        assertEquals(setOf("added.yml"), plan.toAdd)
        assertEquals(setOf("change.yml"), plan.toUpdate)
        assertEquals(setOf("gone.yml"), plan.toDelete)
        // toFetch = 增 + 改，不含删与未变。
        assertEquals(setOf("added.yml", "change.yml"), plan.toFetch())
    }

    @Test
    fun `空对空为无变更`() {
        assertTrue(FileSyncer.diff(emptyMap(), emptyMap()).isEmpty())
    }
}

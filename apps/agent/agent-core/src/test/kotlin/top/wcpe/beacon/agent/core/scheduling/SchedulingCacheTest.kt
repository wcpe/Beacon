package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.DataSource
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** [SchedulingCache] 单测（FR-148）：数据源在线态、快照新鲜度（STALE）、跨 zone 查找。 */
class SchedulingCacheTest {
    @Test
    fun `无快照时数据源年龄为 MAX 且非新鲜`() {
        val ds = SchedulingCache().dataSource()
        assertEquals(DataSource.LOCAL_SNAPSHOT, ds.source())
        assertFalse(ds.fresh())
        assertEquals(Long.MAX_VALUE, ds.snapshotAgeMs())
    }

    @Test
    fun `刷新成功数据源为控制面且新鲜`() {
        val now = 10_000L
        val cache = SchedulingCache(now = { now })
        cache.set(snapshotOf("z-a", listOf(candidateEntry("lobby-1", 90)), savedAtMs = now - 1_000L), live = true)
        val ds = cache.dataSource()
        assertEquals(DataSource.CONTROL_PLANE, ds.source())
        assertTrue(ds.fresh())
        assertEquals(1_000L, ds.snapshotAgeMs())
    }

    @Test
    fun `超龄快照标 STALE 仍可用`() {
        val now = 1_000_000L
        val cache = SchedulingCache(now = { now })
        // savedAt 距今 11 分钟 > 10 分钟阈值 → 非新鲜（STALE），但快照仍在（zones 非空）。
        cache.set(snapshotOf("z-a", listOf(candidateEntry("lobby-1", 90)), savedAtMs = now - 11L * 60L * 1000L), live = false)
        val ds = cache.dataSource()
        assertEquals(DataSource.LOCAL_SNAPSHOT, ds.source())
        assertFalse(ds.fresh(), "超 10 分钟应标 STALE")
        assertTrue(cache.entriesInZone("z-a").isNotEmpty(), "STALE 快照仍可用")
    }

    @Test
    fun `markStale 只翻数据源不动快照`() {
        val cache = SchedulingCache(now = { 100L })
        cache.set(snapshotOf("z-a", listOf(candidateEntry("lobby-1", 90))), live = true)
        cache.markStale()
        assertEquals(DataSource.LOCAL_SNAPSHOT, cache.dataSource().source())
        assertEquals(1, cache.entriesInZone("z-a").size, "markStale 不清空快照")
    }

    @Test
    fun `跨 zone 按 serverId 查找`() {
        val cache = SchedulingCache()
        cache.set(
            CandidateSnapshot(
                generatedAtMs = 1L,
                savedAtMs = 1L,
                zones =
                    linkedMapOf(
                        "z-a" to listOf(candidateEntry("lobby-1", 90)),
                        "z-b" to listOf(candidateEntry("lobby-2", 70)),
                    ),
            ),
            live = true,
        )
        assertEquals("lobby-2", cache.findAnywhere("lobby-2")?.serverId)
        assertNull(cache.findAnywhere("nope"))
    }
}

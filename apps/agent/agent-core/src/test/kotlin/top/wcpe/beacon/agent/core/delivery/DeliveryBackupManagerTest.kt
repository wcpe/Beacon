package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.io.IOException
import java.time.Clock
import java.time.Instant
import java.time.ZoneOffset
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 交付备份管理器 [DeliveryBackupManager] 单测（FR-165，spec §4.7.1）：
 * - 备份 manifest 正确（update/delete 复制旧内容 + 记旧哈希；add 仅记标记不复制）；
 * - 保留清理：超 5 个按最旧删、超 30 天删。
 */
class DeliveryBackupManagerTest {
    private val serverRoot: File = DeliveryTestSupport.tempDir("delivery-bk-root")
    private val backupRoot: File = DeliveryTestSupport.tempDir("delivery-bk-store")
    private val dataDir: File = DeliveryTestSupport.tempDir("delivery-bk-data")
    private val adapter = ManualAsyncAdapter(dataDir)
    private val codec = CapturingCodec()
    private val resolver = DeliveryTargetResolver(serverRoot, dataDir)

    @Test
    fun `备份 manifest 记旧哈希并复制旧内容add 项仅记标记`() {
        val upd = "OLD-UPD".toByteArray()
        val del = "OLD-DEL".toByteArray()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", upd)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", del)
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter)

        val present =
            manager.backup(
                1L,
                listOf(
                    op("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE),
                    op("plugins/del.txt", DeliveryFileOp.Kind.DELETE),
                    op("plugins/add.txt", DeliveryFileOp.Kind.ADD),
                ),
            )

        assertTrue(present)
        assertTrue(File(backupRoot, "1/manifest.json").exists())
        val entries = codec.lastEncoded as List<*>
        assertEntry(entries, "plugins/upd.txt", "update", DeliveryTestSupport.sha256(upd), upd.size.toLong())
        assertEntry(entries, "plugins/del.txt", "delete", DeliveryTestSupport.sha256(del), del.size.toLong())
        assertEntry(entries, "plugins/add.txt", "add", "", 0L)
        // update / delete 旧内容复制到 files/；add 无内容可备。
        assertTrue(File(backupRoot, "1/files/plugins/upd.txt").exists())
        assertTrue(File(backupRoot, "1/files/plugins/del.txt").exists())
        assertFalse(File(backupRoot, "1/files/plugins/add.txt").exists())
        assertEquals("OLD-UPD", File(backupRoot, "1/files/plugins/upd.txt").readText())
    }

    @Test
    fun `空工作集不生成备份`() {
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter)
        assertFalse(manager.backup(9L, emptyList()))
        assertFalse(File(backupRoot, "9").exists())
    }

    @Test
    fun `回滚还原update复原del复原add删除`() {
        val roundTrip = RoundTripCodec()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD-UPD".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", "OLD-DEL".toByteArray())
        val manager = DeliveryBackupManager(backupRoot, resolver, roundTrip, adapter)
        // 覆盖前备份工作集。
        manager.backup(
            1L,
            listOf(
                op("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE),
                op("plugins/del.txt", DeliveryFileOp.Kind.DELETE),
                op("plugins/add.txt", DeliveryFileOp.Kind.ADD),
            ),
        )
        // 模拟正推覆盖：upd 改新内容、del 删除、add 新增。
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW-UPD".toByteArray())
        assertTrue(File(serverRoot, "plugins/del.txt").delete())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/add.txt", "NEW-ADD".toByteArray())

        val restored = manager.restore(1L)

        assertEquals(3, restored)
        assertEquals("OLD-UPD", File(serverRoot, "plugins/upd.txt").readText(), "update 项应复原旧内容")
        assertTrue(File(serverRoot, "plugins/del.txt").exists(), "delete 项应复原")
        assertEquals("OLD-DEL", File(serverRoot, "plugins/del.txt").readText())
        assertFalse(File(serverRoot, "plugins/add.txt").exists(), "add 项应删除还原为不存在")
    }

    @Test
    fun `回滚备份缺失抛异常`() {
        val manager = DeliveryBackupManager(backupRoot, resolver, RoundTripCodec(), adapter)
        assertFailsWith<IOException> { manager.restore(999L) }
    }

    @Test
    fun `保留清理超五个按最旧删除`() {
        val now = Instant.parse("2026-07-15T00:00:00Z")
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter, Clock.fixed(now, ZoneOffset.UTC))
        // 建 6 个备份单目录，lastModified 递增（1 最旧、6 最新）。
        for (i in 1..6) backupDir(i.toString(), now.toEpochMilli() - (7 - i) * 1000L)

        manager.enforceRetention()

        assertFalse(File(backupRoot, "1").exists(), "最旧的应被删")
        for (i in 2..6) assertTrue(File(backupRoot, i.toString()).exists())
    }

    @Test
    fun `保留清理超三十天删除`() {
        val now = Instant.parse("2026-07-15T00:00:00Z")
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter, Clock.fixed(now, ZoneOffset.UTC))
        val fresh = backupDir("fresh", now.toEpochMilli())
        val stale = backupDir("stale", now.toEpochMilli() - 31L * MILLIS_PER_DAY)

        manager.enforceRetention()

        assertTrue(fresh.exists())
        assertFalse(stale.exists(), "超 30 天的备份应被删")
    }

    /** 建一个带指定 lastModified 的备份单目录。 */
    private fun backupDir(
        name: String,
        lastModifiedMs: Long,
    ): File {
        val dir = File(backupRoot, name)
        dir.mkdirs()
        dir.setLastModified(lastModifiedMs)
        return dir
    }

    private fun assertEntry(
        entries: List<*>,
        path: String,
        action: String,
        sha256: String,
        size: Long,
    ) {
        @Suppress("UNCHECKED_CAST")
        val entry = entries.map { it as Map<String, Any?> }.first { it["path"] == path }
        assertEquals(action, entry["action"])
        assertEquals(sha256, entry["sha256"])
        assertEquals(size, entry["size"])
    }

    private fun op(
        path: String,
        kind: DeliveryFileOp.Kind,
    ) = DeliveryFileOp(path, kind, "", 0L)

    /** 捕获最近一次 encode 入参（备份条目 List），免依赖真 JSON 编解码即可精确断言条目结构。 */
    private class CapturingCodec : JsonCodec {
        var lastEncoded: Any? = null

        override fun encode(value: Any?): String {
            lastEncoded = value
            return "[]"
        }

        override fun decode(json: String): Any? = emptyMap<String, Any?>()
    }

    /** 往返 codec：encode 记住入参、decode 返回它，免真 JSON 编解码即可测 backup→restore 往返。 */
    private class RoundTripCodec : JsonCodec {
        private var last: Any? = null

        override fun encode(value: Any?): String {
            last = value
            return "round-trip"
        }

        override fun decode(json: String): Any? = last
    }

    private companion object {
        private const val MILLIS_PER_DAY = 24L * 60 * 60 * 1000
    }
}

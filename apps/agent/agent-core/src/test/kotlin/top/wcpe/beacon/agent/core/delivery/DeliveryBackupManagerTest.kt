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
 * 交付备份管理器 [DeliveryBackupManager] 单测（FR-165，spec §4.7.1；FR-267 增量合并与还原校验）：
 * - 备份 manifest 正确（update/delete 复制旧内容 + 记旧哈希；add 仅记标记不复制）；
 * - 同单重推增量合并既有条目（不重备、不清空，重推途中失败也不毁既有回滚点）；
 * - 还原前按 manifest 的 sha256 / size 校验备份内容，不符即失败且不写回目标；
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
    fun `同单重推增量合并保留首次备份且不重备既有条目`() {
        val codec = RecordingRoundTripCodec()
        val original = "OLD".toByteArray()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", original)
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter)
        manager.backup(1L, listOf(op("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE)))

        // 模拟本单已覆盖目标 + 第二次推送新增一个文件。
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/added.txt", "ADDED".toByteArray())
        manager.backup(
            1L,
            listOf(
                op("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE),
                op("plugins/added.txt", DeliveryFileOp.Kind.ADD),
            ),
        )

        // 既有条目必须仍指向「本单改动前」的内容：重备会把已覆盖内容冒充回滚点，回滚反而写坏文件。
        assertEquals("OLD", File(backupRoot, "1/files/plugins/upd.txt").readText(), "既有备份内容绝不被重推覆盖")
        val entries = codec.lastEncoded as List<*>
        assertEquals(2, entries.size, "新工作集条目应追加进同一 manifest")
        assertEntry(entries, "plugins/upd.txt", "update", DeliveryTestSupport.sha256(original), original.size.toLong())
        assertEntry(entries, "plugins/added.txt", "add", "", 0L)
    }

    @Test
    fun `同单重推途中失败保留既有回滚点`() {
        val codec = RecordingRoundTripCodec()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD".toByteArray())
        val manager = DeliveryBackupManager(backupRoot, resolver, codec, adapter)
        manager.backup(1L, listOf(op("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE)))
        val manifestBefore = File(backupRoot, "1/manifest.json").readText()

        // 第二次推送的工作集含非法路径（模拟重推途中失败）：不得清空 / 破坏既有回滚点。
        assertFailsWith<IOException> {
            manager.backup(1L, listOf(op("../evil.txt", DeliveryFileOp.Kind.UPDATE)))
        }

        assertEquals("OLD", File(backupRoot, "1/files/plugins/upd.txt").readText(), "既有备份内容必须仍在盘")
        assertEquals(manifestBefore, File(backupRoot, "1/manifest.json").readText(), "既有 manifest 不得被破坏")
        assertEquals(1, (codec.lastEncoded as List<*>).size, "既有条目不得丢失")
    }

    @Test
    fun `还原校验备份大小不符即失败且不覆盖目标`() {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray())
        val backupContent = "TAMPERED".toByteArray()
        val entry = entry("plugins/upd.txt", "update", DeliveryTestSupport.sha256(backupContent), 3L)
        val manager = DeliveryBackupManager(backupRoot, resolver, FixedManifestCodec(listOf(entry)), adapter)
        seedBackupFile("plugins/upd.txt", backupContent)

        assertFailsWith<IOException> { manager.restore(1L) }

        assertEquals("NEW", File(serverRoot, "plugins/upd.txt").readText(), "校验不符绝不覆盖目标")
    }

    @Test
    fun `还原校验备份哈希不符即失败且不覆盖目标`() {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray())
        val declared = "OLD".toByteArray()
        // 同长度、异内容：大小校验能过，必须由哈希校验挡下。
        val tampered = "OLX".toByteArray()
        val entry = entry("plugins/upd.txt", "update", DeliveryTestSupport.sha256(declared), declared.size.toLong())
        val manager = DeliveryBackupManager(backupRoot, resolver, FixedManifestCodec(listOf(entry)), adapter)
        seedBackupFile("plugins/upd.txt", tampered)

        assertFailsWith<IOException> { manager.restore(1L) }

        assertEquals("NEW", File(serverRoot, "plugins/upd.txt").readText(), "校验不符绝不覆盖目标")
    }

    @Test
    fun `还原目录条目只校验存在与类型不被空哈希误判损坏`() {
        // 真机验收 O4：目录无法哈希，备份时只建空目录，manifest 的 sha256 天生为空（既有形态，非损坏）。
        // 若对目录条目也严格比对 sha256 / size，任何含目录的交付单整单回滚都会被拒（安全但功能退化）。
        val entry = entry("plugins/cfgdir", "update", "", 0L)
        val manager = DeliveryBackupManager(backupRoot, resolver, FixedManifestCodec(listOf(entry)), adapter)
        seedBackupDir("plugins/cfgdir")
        // 目标已被交付覆盖成文件（或缺失），还原应把它恢复为目录而非抛异常。
        DeliveryTestSupport.writeFile(serverRoot, "plugins/cfgdir", "NEW".toByteArray())

        manager.restore(1L)

        assertTrue(File(serverRoot, "plugins/cfgdir").isDirectory, "目录条目应还原为目录，不得被空哈希挡下")
    }

    /** 铺一条**目录**备份条目（备份区里该项是空目录，manifest 的 sha256 为空）。 */
    private fun seedBackupDir(relPath: String) {
        File(backupRoot, "1").mkdirs()
        File(backupRoot, "1/manifest.json").writeText("stub")
        File(backupRoot, "1/files").mkdirs()
        assertTrue(File(File(backupRoot, "1/files"), relPath).mkdirs(), "备份区应铺出目录条目")
    }

    /** 组装一条 manifest 条目（校验用例自定 sha / size）。 */
    private fun seedBackupFile(
        relPath: String,
        content: ByteArray,
    ) {
        File(backupRoot, "1").mkdirs()
        File(backupRoot, "1/manifest.json").writeText("stub")
        DeliveryTestSupport.writeFile(File(backupRoot, "1/files"), relPath, content)
    }

    /** 组装一条 manifest 条目（校验用例自定 sha / size）。 */
    private fun entry(
        path: String,
        action: String,
        sha256: String,
        size: Long,
    ): Map<String, Any?> = mapOf("path" to path, "action" to action, "sha256" to sha256, "size" to size)

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

    /** 往返 codec + 记录最近一次 encode 入参：既支持多轮 backup→restore，也便于精确断言 manifest 条目。 */
    private class RecordingRoundTripCodec : JsonCodec {
        var lastEncoded: Any? = null

        override fun encode(value: Any?): String {
            lastEncoded = value
            return "round-trip"
        }

        override fun decode(json: String): Any? = lastEncoded
    }

    /** 固定 manifest codec：decode 恒返给定条目表（还原校验用例自定 sha / size）。 */
    private class FixedManifestCodec(private val entries: List<Map<String, Any?>>) : JsonCodec {
        override fun encode(value: Any?): String = "[]"

        override fun decode(json: String): Any? = entries
    }

    private companion object {
        private const val MILLIS_PER_DAY = 24L * 60 * 60 * 1000
    }
}

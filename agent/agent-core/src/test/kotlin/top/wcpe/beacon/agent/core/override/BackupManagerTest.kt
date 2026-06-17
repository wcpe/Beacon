package top.wcpe.beacon.agent.core.override

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 覆盖前备份与回滚还原单测（ADR-0011 决策 5）：
 * - 覆盖已存在的真实文件前，备份其原内容并记"原本存在"；
 * - 覆盖原本不存在的文件，记"原本不存在"（回滚时应删除而非还原）；
 * - 回滚按记录：原本存在→还原旧内容；原本不存在→删除。
 */
class BackupManagerTest {

    private val targetRoot: File = Files.createTempDirectory("beacon-target").toFile()
    private val backupDir: File = Files.createTempDirectory("beacon-backup").toFile()
    private val mgr = BackupManager(backupDir)

    @AfterTest
    fun cleanup() {
        targetRoot.deleteRecursively()
        backupDir.deleteRecursively()
    }

    private fun write(path: String, content: String) {
        val f = File(targetRoot, path)
        f.parentFile?.mkdirs()
        f.writeText(content, StandardCharsets.UTF_8)
    }

    @Test
    fun `备份已存在文件 记原本存在`() {
        write("config.yml", "old-content")
        val target = File(targetRoot, "config.yml")
        val record = mgr.backup("set1", "config.yml", target)
        assertTrue(record.existedBefore, "应记原本存在")
        assertEquals("old-content", record.readBackupContent())
    }

    @Test
    fun `备份不存在文件 记原本不存在`() {
        val target = File(targetRoot, "new.yml")
        val record = mgr.backup("set1", "new.yml", target)
        assertFalse(record.existedBefore, "应记原本不存在")
    }

    @Test
    fun `回滚还原原本存在的文件旧内容`() {
        write("config.yml", "old")
        val target = File(targetRoot, "config.yml")
        val record = mgr.backup("set1", "config.yml", target)
        // 模拟覆盖。
        target.writeText("new", StandardCharsets.UTF_8)

        mgr.restore(record, target)
        assertEquals("old", target.readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `回滚删除原本不存在的文件`() {
        val target = File(targetRoot, "new.yml")
        val record = mgr.backup("set1", "new.yml", target)
        // 模拟新增覆盖。
        target.parentFile?.mkdirs()
        target.writeText("created", StandardCharsets.UTF_8)

        mgr.restore(record, target)
        assertFalse(target.exists(), "原本不存在的文件回滚应删除")
    }
}

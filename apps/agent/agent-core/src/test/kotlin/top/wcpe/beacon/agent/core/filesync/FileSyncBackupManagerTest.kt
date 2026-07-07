package top.wcpe.beacon.agent.core.filesync

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.LinkOption
import java.time.Clock
import java.time.Instant
import java.time.ZoneOffset
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/** 文件同步覆盖前目录备份与回滚骨架测试。 */
class FileSyncBackupManagerTest {
    private val root: File = Files.createTempDirectory("beacon-sync-target").toFile()
    private val backupRoot: File = Files.createTempDirectory("beacon-sync-backup").toFile()
    private val fixedClock: Clock = Clock.fixed(Instant.parse("2026-07-04T12:13:14Z"), ZoneOffset.UTC)
    private val manager = FileSyncBackupManager(root, backupRoot, fixedClock)

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
        backupRoot.deleteRecursively()
    }

    @Test
    fun `覆盖前把目标目录移动到带时间戳备份点`() {
        write("plugins/Demo/config.yml", "old")

        val point = manager.backupDirectory("plugins/Demo")

        assertTrue(point.existedBefore)
        val backupPath = assertNotNull(point.backupPath)
        assertTrue(backupPath.name.contains("20260704-121314"))
        assertTrue(File(backupPath, "config.yml").exists(), "旧目录内容应进入备份点")
        assertFalse(File(root, "plugins/Demo").exists(), "原目标目录应被让位")
    }

    @Test
    fun `回滚把备份目录恢复到原路径并替换当前目录`() {
        write("plugins/Demo/config.yml", "old")
        val point = manager.backupDirectory("plugins/Demo")
        write("plugins/Demo/config.yml", "new")

        val restored = manager.rollback(point)

        assertTrue(restored, "存在备份点时回滚应返回 true")
        assertEquals("old", File(root, "plugins/Demo/config.yml").readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `回滚删除当前目录时不会跟随子目录符号链接`() {
        val outside = Files.createTempDirectory("beacon-sync-outside").toFile()
        try {
            val secret = File(outside, "secret.yml").apply { writeText("safe", StandardCharsets.UTF_8) }
            write("plugins/Demo/config.yml", "old")
            val point = manager.backupDirectory("plugins/Demo")
            write("plugins/Demo/config.yml", "new")
            val link = File(root, "plugins/Demo/outside-link").toPath()
            if (!trySymlink(link, outside.toPath())) return

            val restored = manager.rollback(point)

            assertTrue(restored, "存在备份点时回滚应返回 true")
            assertEquals("old", File(root, "plugins/Demo/config.yml").readText(StandardCharsets.UTF_8))
            assertTrue(secret.exists(), "根外文件不应被回滚清理误删")
            assertFalse(Files.exists(link, LinkOption.NOFOLLOW_LINKS), "当前目录内的符号链接本身应被删除")
        } finally {
            outside.deleteRecursively()
        }
    }

    @Test
    fun `原目录不存在时备份记录可回滚为空操作`() {
        val point = manager.backupDirectory("plugins/New")

        assertFalse(point.existedBefore)
        assertEquals(null, point.backupPath)
        assertTrue(manager.rollback(point), "无旧目录的备份点回滚应是幂等空操作")
        assertFalse(File(root, "plugins/New").exists())
    }

    private fun write(
        relativePath: String,
        content: String,
    ) {
        val file = File(root, relativePath)
        file.parentFile.mkdirs()
        file.writeText(content, StandardCharsets.UTF_8)
    }

    /** 尝试创建符号链接；环境不支持时跳过相关断言。 */
    private fun trySymlink(
        link: java.nio.file.Path,
        target: java.nio.file.Path,
    ): Boolean {
        return try {
            Files.createSymbolicLink(link, target)
            true
        } catch (e: Exception) {
            false
        }
    }
}

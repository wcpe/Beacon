package top.wcpe.beacon.agent.core.filesync

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** 文件同步服务器根路径安全校验：拒绝路径穿越、平台差异绕过与符号链接逃逸。 */
class FileSyncPathGuardTest {
    private val root: File = Files.createTempDirectory("beacon-sync-root").toFile()
    private val outside: File = Files.createTempDirectory("beacon-sync-outside").toFile()
    private val guard = FileSyncPathGuard(root)

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
        outside.deleteRecursively()
    }

    @Test
    fun `安全相对目录解析到服务器根内`() {
        val resolved = guard.resolveDirectory("plugins/LuckPerms")
        assertNotNull(resolved)
        assertTrue(resolved.startsWith(root.toPath().toRealPath()))
    }

    @Test
    fun `拒绝穿越 绝对路径 盘符 UNC 与反斜杠`() {
        assertNull(guard.resolveDirectory("../escape"))
        assertNull(guard.resolveDirectory("plugins/../../escape"))
        assertNull(guard.resolveDirectory("/etc"))
        assertNull(guard.resolveDirectory("C:/Windows"))
        assertNull(guard.resolveDirectory("C:Windows"))
        assertNull(guard.resolveDirectory("\\\\server\\share"))
        assertNull(guard.resolveDirectory("plugins\\LuckPerms"))
    }

    @Test
    fun `拒绝段尾点空格与 Windows 保留名`() {
        assertNull(guard.resolveDirectory("plugins/name."))
        assertNull(guard.resolveDirectory("plugins/name "))
        assertNull(guard.resolveFile("plugins/con/config.yml"))
        assertNull(guard.resolveFile("plugins/aux.txt"))
    }

    @Test
    fun `不存在的安全子路径允许预解析`() {
        val resolved = guard.resolveFile("plugins/NewPlugin/config.yml")
        assertNotNull(resolved)
        assertFalse(resolved.toFile().exists())
        assertTrue(resolved.startsWith(root.toPath().toRealPath()))
    }

    @Test
    fun `符号链接逃逸到服务器根外被拒`() {
        val secret = File(outside, "secret.yml").apply { writeText("TOP SECRET", StandardCharsets.UTF_8) }
        val link = File(root, "linked-out").toPath()
        if (!trySymlink(link, outside.toPath())) return

        assertNull(guard.resolveDirectory("linked-out"))
        assertNull(guard.resolveFile("linked-out/secret.yml"))
        assertNull(guard.resolveFile("linked-out/new.yml"))
        assertTrue(secret.exists(), "测试准备的根外文件不应被触碰")
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

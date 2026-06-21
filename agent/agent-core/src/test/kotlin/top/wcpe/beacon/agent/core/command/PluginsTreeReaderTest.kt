package top.wcpe.beacon.agent.core.command

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 读真实 plugins 树 FS 边界 [PluginsTreeReader] 单测（FR-39，见 ADR-0027）：
 * - 递归读取相对路径→字节，统一正斜杠；
 * - 只收普通文件（跳目录）；
 * - 符号链接逃逸（指向 root 外）被剔除——FS 级 startsWith 真实路径判定。
 */
class PluginsTreeReaderTest {

    private val root: File = Files.createTempDirectory("beacon-plugins-root").toFile()
    private val outside: File = Files.createTempDirectory("beacon-outside").toFile()

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
        outside.deleteRecursively()
    }

    private fun write(rel: String, content: String) {
        val f = File(root, rel)
        f.parentFile.mkdirs()
        f.writeText(content, StandardCharsets.UTF_8)
    }

    private fun text(bytes: ByteArray?): String? = bytes?.toString(StandardCharsets.UTF_8)

    @Test
    fun `递归读取相对路径与内容`() {
        write("config.yml", "k: v")
        write("lang/zh_CN.yml", "hello: 你好")
        write("a/b/deep.json", "{}")

        val tree = PluginsTreeReader.read(root)
        assertEquals(setOf("config.yml", "lang/zh_CN.yml", "a/b/deep.json"), tree.keys)
        assertEquals("hello: 你好", text(tree["lang/zh_CN.yml"]))
        // 相对路径统一正斜杠（跨平台一致）。
        assertTrue(tree.keys.none { it.contains('\\') }, "相对路径不应含反斜杠")
    }

    @Test
    fun `空目录读出空映射`() {
        assertTrue(PluginsTreeReader.read(root).isEmpty())
    }

    @Test
    fun `不存在或非目录读出空映射`() {
        assertTrue(PluginsTreeReader.read(File(root, "no-such-dir")).isEmpty())
        val file = File(root, "afile.txt").apply { writeText("x") }
        assertTrue(PluginsTreeReader.read(file).isEmpty(), "传入文件（非目录）应得空映射")
    }

    @Test
    fun `只收普通文件 跳过空子目录`() {
        write("plugins-data/keep.yml", "k: v")
        File(root, "emptydir").mkdirs() // 空目录不产生条目
        val tree = PluginsTreeReader.read(root)
        assertEquals(setOf("plugins-data/keep.yml"), tree.keys)
    }

    @Test
    fun `读盘前按名跳过 jar 不读其字节`() {
        write("config.yml", "k: v")
        write("SomePlugin.jar", "MZ-binary-bulk")
        write("libs/dep.JAR", "x")
        val tree = PluginsTreeReader.read(root)
        // jar 在读盘阶段即按名跳过（不读字节、不进映射）。
        assertEquals(setOf("config.yml"), tree.keys, "jar 应在读盘前跳过")
    }

    @Test
    fun `超大文件读取被截断供纯函数判超限`() {
        // 写一个超 1MB 的文件；读取应截断到 MAX_FILE_BYTES+1（不全载），交 PluginsTreeFilter 判超限。
        val big = "a".repeat((PluginIngestLimits.MAX_FILE_BYTES + 4096).toInt())
        write("huge.yml", big)
        val tree = PluginsTreeReader.read(root)
        val read = tree["huge.yml"]!!
        assertEquals((PluginIngestLimits.MAX_FILE_BYTES + 1).toInt(), read.size, "超大文件应截断到上限+1 字节")
    }

    @Test
    fun `符号链接逃逸到 root 外被剔除`() {
        // 在 root 外放一个目标文件，root 内建指向它的符号链接 → 解析真实路径逃出 root，应剔除。
        val secret = File(outside, "secret.txt").apply { writeText("TOP SECRET") }
        write("normal.yml", "k: v")
        val link = File(root, "leak.txt").toPath()
        // 环境不支持创建符号链接（Windows 无权限 / FS 不支持）→ 早返回视作跳过，不让环境差异致测试红。
        if (!trySymlink(link, secret.toPath())) return

        val tree = PluginsTreeReader.read(root)
        assertTrue(tree.containsKey("normal.yml"), "root 内普通文件应保留")
        assertFalse(tree.containsKey("leak.txt"), "逃逸 root 的符号链接应被剔除")
        assertTrue(tree.values.none { text(it) == "TOP SECRET" }, "绝不读出 root 外内容")
    }

    @Test
    fun `指向 root 内的符号链接允许读取`() {
        // 符号链接目标仍在 root 内 → 真实路径落在 root，允许。
        write("real/data.yml", "k: v")
        val link = File(root, "alias.yml").toPath()
        if (!trySymlink(link, File(root, "real/data.yml").toPath())) return // 不支持符号链接 → 跳过

        val tree = PluginsTreeReader.read(root)
        // 真实路径相对化后归一到 real/data.yml（解析链接后），内容可读。
        assertTrue(tree.values.any { text(it) == "k: v" }, "指向 root 内的链接应可读")
    }

    /** 尝试创建符号链接；环境不支持（无权限 / FS 不支持）返回 false 供调用方跳过。 */
    private fun trySymlink(link: java.nio.file.Path, target: java.nio.file.Path): Boolean {
        return try {
            Files.createSymbolicLink(link, target)
            true
        } catch (e: Exception) {
            false
        }
    }
}

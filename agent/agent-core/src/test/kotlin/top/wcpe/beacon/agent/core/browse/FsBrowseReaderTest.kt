package top.wcpe.beacon.agent.core.browse

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 只读交互式文件浏览 FS 边界 [FsBrowseReader] 单测（FR-109，见 ADR-0049）。
 *
 * 覆盖验收标准：path traversal 各类越权被拒、大目录分页、读子树有界、单文件超限不读全文、jar/二进制排除、只读性。
 */
class FsBrowseReaderTest {

    private val root: File = Files.createTempDirectory("beacon-browse-root").toFile()
    private val outside: File = Files.createTempDirectory("beacon-browse-outside").toFile()

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

    private fun mkdir(rel: String) {
        File(root, rel).mkdirs()
    }

    // ---- 原语①：懒列目录（分页、稳定排序、只列直接子项） ----

    @Test
    fun `列根目录只列直接子项 不递归`() {
        write("config.yml", "k: v")
        write("PluginA/data.yml", "x: 1")
        mkdir("EmptyDir")

        val listing = FsBrowseReader.listDir(root, "", 0, 100)
        assertNotNull(listing)
        // 只列直接子项：config.yml / PluginA / EmptyDir（不含 PluginA/data.yml）。
        assertEquals(setOf("config.yml", "PluginA", "EmptyDir"), listing.entries.map { it.name }.toSet())
        // 目录优先排序：PluginA / EmptyDir 在 config.yml 之前。
        assertTrue(listing.entries.first().dir, "目录应排在前")
        val pluginA = listing.entries.first { it.name == "PluginA" }
        assertTrue(pluginA.dir)
        assertEquals("PluginA", pluginA.relPath)
        val cfg = listing.entries.first { it.name == "config.yml" }
        assertFalse(cfg.dir)
        assertTrue(cfg.text, "yml 应判为文本")
    }

    @Test
    fun `列子目录用相对路径`() {
        write("PluginA/lang/zh_CN.yml", "hi: 你好")
        val listing = FsBrowseReader.listDir(root, "PluginA/lang", 0, 100)
        assertNotNull(listing)
        assertEquals("PluginA/lang", listing.path)
        assertEquals(listOf("zh_CN.yml"), listing.entries.map { it.name })
        assertEquals("PluginA/lang/zh_CN.yml", listing.entries.first().relPath)
    }

    @Test
    fun `大目录分页 带 total 与 hasMore`() {
        for (i in 1..10) write("d/f%02d.yml".format(i), "k: $i")
        // 第一页 limit=3。
        val page1 = FsBrowseReader.listDir(root, "d", 0, 3)
        assertNotNull(page1)
        assertEquals(3, page1.entries.size)
        assertEquals(10, page1.total)
        assertTrue(page1.hasMore)
        assertEquals(listOf("f01.yml", "f02.yml", "f03.yml"), page1.entries.map { it.name })
        // 末页。
        val page4 = FsBrowseReader.listDir(root, "d", 9, 3)
        assertNotNull(page4)
        assertEquals(listOf("f10.yml"), page4.entries.map { it.name })
        assertFalse(page4.hasMore, "末页 hasMore 应为 false")
        // offset 超出总数 → 空页。
        val beyond = FsBrowseReader.listDir(root, "d", 99, 3)
        assertNotNull(beyond)
        assertTrue(beyond.entries.isEmpty())
        assertFalse(beyond.hasMore)
    }

    @Test
    fun `limit 收口到上限 防一次拉全`() {
        for (i in 1..5) write("d/f$i.yml", "k: $i")
        val listing = FsBrowseReader.listDir(root, "d", 0, FsBrowseLimits.MAX_LIST_LIMIT + 9999)
        assertNotNull(listing)
        assertEquals(FsBrowseLimits.MAX_LIST_LIMIT, listing.limit, "limit 应收口到 MAX_LIST_LIMIT")
    }

    @Test
    fun `列不存在或非目录返回 null`() {
        write("a.yml", "k: v")
        assertNull(FsBrowseReader.listDir(root, "no-such-dir", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "a.yml", 0, 100), "对文件列目录应 null")
        assertNull(FsBrowseReader.listDir(File(root, "no-root"), "", 0, 100), "root 非目录应 null")
    }

    // ---- path traversal 各类越权被拒（安全红线） ----

    @Test
    fun `穿越路径列目录一律拒绝`() {
        write("a.yml", "k: v")
        File(outside, "secret.txt").writeText("TOP SECRET")
        assertNull(FsBrowseReader.listDir(root, "..", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "../escape", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "a/../../escape", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "../../etc", 0, 100))
    }

    @Test
    fun `绝对 盘符 UNC 反斜杠列目录拒绝`() {
        assertNull(FsBrowseReader.listDir(root, "/etc", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "C:/Windows", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "a\\b", 0, 100))
        assertNull(FsBrowseReader.listDir(root, "\\\\host\\share", 0, 100))
    }

    @Test
    fun `穿越路径读文件一律拒绝`() {
        write("a.yml", "k: v")
        File(outside, "secret.txt").writeText("TOP SECRET")
        assertNull(FsBrowseReader.readFile(root, "../secret.txt"))
        assertNull(FsBrowseReader.readFile(root, "../../etc/passwd"))
        assertNull(FsBrowseReader.readFile(root, "a/../../secret.txt"))
        assertNull(FsBrowseReader.readFile(root, "/etc/passwd"))
    }

    @Test
    fun `符号链接逃逸到 root 外被拒 绝不读越权内容`() {
        val secret = File(outside, "secret.txt").apply { writeText("TOP SECRET") }
        write("normal.yml", "k: v")
        val link = File(root, "leak.txt").toPath()
        if (!trySymlink(link, secret.toPath())) return // 环境不支持符号链接 → 跳过

        // 读逃逸链接 → null，绝不读出 root 外内容。
        assertNull(FsBrowseReader.readFile(root, "leak.txt"), "逃逸 root 的符号链接读文件应被拒")
        // 列根时逃逸链接被剔除、不出现在条目里。
        val listing = FsBrowseReader.listDir(root, "", 0, 100)
        assertNotNull(listing)
        assertTrue(listing.entries.none { it.name == "leak.txt" }, "逃逸链接应从列表剔除")
        assertTrue(listing.entries.any { it.name == "normal.yml" })
    }

    @Test
    fun `指向 root 内的符号链接允许读`() {
        write("real/data.yml", "k: v")
        val link = File(root, "alias.yml").toPath()
        if (!trySymlink(link, File(root, "real/data.yml").toPath())) return // 不支持 → 跳过

        val content = FsBrowseReader.readFile(root, "alias.yml")
        assertNotNull(content, "指向 root 内的链接应可读")
        assertEquals("k: v", content.content)
    }

    // ---- 原语③：读单文件（上限、jar/二进制排除、只读） ----

    @Test
    fun `读文本文件得内容`() {
        write("PluginA/config.yml", "key: 值")
        val content = FsBrowseReader.readFile(root, "PluginA/config.yml")
        assertNotNull(content)
        assertEquals("PluginA/config.yml", content.path)
        assertEquals("key: 值", content.content)
        assertFalse(content.truncated)
    }

    @Test
    fun `读单文件超上限截断不读全文`() {
        val big = "a".repeat((FsBrowseLimits.MAX_FILE_BYTES + 4096).toInt())
        write("huge.yml", big)
        val content = FsBrowseReader.readFile(root, "huge.yml")
        assertNotNull(content)
        assertTrue(content.truncated, "超上限应标 truncated")
        assertEquals(FsBrowseLimits.MAX_FILE_BYTES.toInt(), content.content.length, "内容应截断到上限、非全文")
    }

    @Test
    fun `读 jar 被排除返回 null`() {
        write("SomePlugin.jar", "MZ-binary-bulk")
        write("libs/dep.JAR", "x")
        assertNull(FsBrowseReader.readFile(root, "SomePlugin.jar"))
        assertNull(FsBrowseReader.readFile(root, "libs/dep.JAR"))
    }

    @Test
    fun `读二进制文件 含 NUL 返回 null`() {
        val f = File(root, "data.bin")
        f.parentFile.mkdirs()
        f.writeBytes(byteArrayOf(0x4D, 0x5A, 0x00, 0x01, 0x02))
        assertNull(FsBrowseReader.readFile(root, "data.bin"), "含 NUL 的二进制应被拒")
    }

    @Test
    fun `读目录或不存在文件返回 null`() {
        mkdir("adir")
        assertNull(FsBrowseReader.readFile(root, "adir"), "对目录读文件应 null")
        assertNull(FsBrowseReader.readFile(root, "no-such.yml"))
        assertNull(FsBrowseReader.readFile(root, ""), "空路径读文件应 null")
    }

    @Test
    fun `浏览全程只读 不改盘`() {
        write("a.yml", "k: v")
        write("PluginA/b.yml", "x: 1")
        val before = snapshot(root)

        FsBrowseReader.listDir(root, "", 0, 100)
        FsBrowseReader.readTree(root, "", 5)
        FsBrowseReader.readFile(root, "a.yml")
        FsBrowseReader.readFile(root, "PluginA/b.yml")
        FsBrowseReader.readFile(root, "../escape") // 越权请求也不应有副作用

        val after = snapshot(root)
        assertEquals(before, after, "浏览不应新增/修改/删除任何文件")
    }

    // ---- 原语②：读文件树（按需展开、逐层有界） ----

    @Test
    fun `读子树按层展开`() {
        write("config.yml", "k: v")
        write("PluginA/lang/zh.yml", "hi: 你好")
        val tree = FsBrowseReader.readTree(root, "", 5)
        assertNotNull(tree)
        assertTrue(tree.dir)
        val names = tree.children.map { it.name }.toSet()
        assertEquals(setOf("config.yml", "PluginA"), names)
        val pluginA = tree.children.first { it.name == "PluginA" }
        assertTrue(pluginA.dir)
        val lang = pluginA.children.first { it.name == "lang" }
        assertEquals(listOf("zh.yml"), lang.children.map { it.name })
    }

    @Test
    fun `读子树深度有界 超深度目录标 truncated`() {
        write("l1/l2/l3/deep.yml", "k: v")
        // maxDepth=1：只展开根的直接子项（l1），l1 不再向下展开、标 truncated。
        val tree = FsBrowseReader.readTree(root, "", 1)
        assertNotNull(tree)
        val l1 = tree.children.first { it.name == "l1" }
        assertTrue(l1.truncated, "超展开深度的目录应标 truncated")
        assertTrue(l1.children.isEmpty(), "超深度目录不展开 children")
    }

    @Test
    fun `读子树 maxDepth 收口到上限`() {
        write("a/b.yml", "k: v")
        // 请求超大深度 → 收口到 MAX_TREE_DEPTH，不应抛错、能正常返回。
        val tree = FsBrowseReader.readTree(root, "", FsBrowseLimits.MAX_TREE_DEPTH + 9999)
        assertNotNull(tree)
        assertTrue(tree.children.any { it.name == "a" })
    }

    @Test
    fun `读子树越权返回 null`() {
        write("a.yml", "k: v")
        assertNull(FsBrowseReader.readTree(root, "..", 5))
        assertNull(FsBrowseReader.readTree(root, "/etc", 5))
        assertNull(FsBrowseReader.readTree(root, "a.yml", 5), "对文件读子树应 null")
    }

    /** 取目录下所有文件相对路径→内容/大小的快照，用于断言只读不改盘。 */
    private fun snapshot(dir: File): Map<String, String> {
        val map = HashMap<String, String>()
        dir.walkTopDown().filter { it.isFile }.forEach {
            map[dir.toPath().relativize(it.toPath()).toString()] = it.readText()
        }
        return map
    }

    /** 尝试创建符号链接；环境不支持返回 false 供跳过。 */
    private fun trySymlink(link: java.nio.file.Path, target: java.nio.file.Path): Boolean {
        return try {
            Files.createSymbolicLink(link, target)
            true
        } catch (e: Exception) {
            false
        }
    }
}

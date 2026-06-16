package top.wcpe.beacon.agent.core.filetree

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** FileMirrorWriter 原子落盘（临时文件→fsync→rename）+ 删除 + 路径安全校验单测。 */
class FileMirrorWriterTest {

    private val root: File = Files.createTempDirectory("beacon-mirror").toFile()
    private val writer = FileMirrorWriter(root)

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
    }

    @Test
    fun `写入后内容与目标一致`() {
        writer.write("a.yml", "hello: world\n")
        val target = File(root, "a.yml")
        assertTrue(target.exists())
        assertEquals("hello: world\n", target.readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `自动创建嵌套父目录`() {
        writer.write("ui-components/sub/main.allin", "x")
        val target = File(root, "ui-components/sub/main.allin")
        assertTrue(target.exists())
        assertEquals("x", target.readText(StandardCharsets.UTF_8))
        assertTrue(File(root, "ui-components/sub").isDirectory)
    }

    @Test
    fun `原子重命名不残留临时文件`() {
        writer.write("a.yml", "v1")
        // 临时文件应已被原子重命名消费，目录下不残留 .beacon-tmp。
        val residue = File(root, "a.yml.beacon-tmp")
        assertFalse(residue.exists(), "临时文件应在 ATOMIC_MOVE 后消失")
    }

    @Test
    fun `覆盖已存在文件`() {
        writer.write("a.yml", "old")
        writer.write("a.yml", "new")
        assertEquals("new", File(root, "a.yml").readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `删除已存在文件且幂等`() {
        writer.write("a.yml", "x")
        writer.delete("a.yml")
        assertFalse(File(root, "a.yml").exists())
        // 再删不存在的同 path 不抛（幂等）。
        writer.delete("a.yml")
    }

    @Test
    fun `非法路径写入抛异常 不逃逸目标根`() {
        assertFailsWith<IllegalArgumentException> { writer.write("../escape.txt", "x") }
        assertFailsWith<IllegalArgumentException> { writer.write("/abs.txt", "x") }
        assertFailsWith<IllegalArgumentException> { writer.write("a\\b.txt", "x") }
        // 逃逸目标根的文件不应被创建。
        assertFalse(File(root.parentFile, "escape.txt").exists())
    }

    @Test
    fun `非法路径删除抛异常`() {
        assertFailsWith<IllegalArgumentException> { writer.delete("../x") }
    }
}

package top.wcpe.beacon.agent.core.filetree

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
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
        // 临时文件应已被原子重命名消费，目录下不残留任何 a.yml.beacon-tmp* 临时文件。
        val residue = root.listFiles()
            ?.filter { it.name.startsWith("a.yml") && it.name != "a.yml" }
            ?: emptyList()
        assertTrue(residue.isEmpty(), "临时文件应在重命名后消失，实际残留：${residue.map { it.name }}")
    }

    @Test
    fun `并发写同一文件不抛异常且无 tmp 残留`() {
        // 复现 Windows 真机偶发：多线程抢同一临时文件，一方 move 走后另一方 move 找不到源 → NoSuchFileException。
        // 旧实现（共享固定 .beacon-tmp 名）此处必抛；唯一 tmp + 重命名回退/重试后应全程无异常、无残留。
        val threads = 6
        val iterations = 60
        val errors = CopyOnWriteArrayList<Throwable>()
        val start = CountDownLatch(1)
        val workers = (0 until threads).map { t ->
            Thread {
                start.await()
                repeat(iterations) { i ->
                    try {
                        writer.write("shared.yml", "t$t-i$i")
                    } catch (e: Throwable) {
                        errors += e
                    }
                }
            }
        }
        workers.forEach { it.start() }
        start.countDown()
        workers.forEach { it.join() }

        assertTrue(
            errors.isEmpty(),
            "并发原子写不应抛异常，实际：${errors.map { it.javaClass.simpleName + ":" + it.message }}",
        )
        // 最终文件存在且为某次完整写入值（非半截损坏）。
        val target = File(root, "shared.yml")
        assertTrue(target.exists())
        assertTrue(target.readText(StandardCharsets.UTF_8).matches(Regex("t\\d+-i\\d+")))
        // 不残留任何临时文件。
        val residue = root.listFiles()
            ?.filter { it.name.startsWith("shared.yml") && it.name != "shared.yml" }
            ?: emptyList()
        assertTrue(residue.isEmpty(), "不应残留临时文件，实际：${residue.map { it.name }}")
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

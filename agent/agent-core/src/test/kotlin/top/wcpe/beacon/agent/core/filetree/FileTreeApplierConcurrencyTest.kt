package top.wcpe.beacon.agent.core.filetree

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.file.Files
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * FileTreeApplier 并发落盘安全单测（复现 Windows 真机偶发 NoSuchFileException）。
 *
 * 背景：apply 可由文件树长轮询循环 / SSE file-changed / 运维 resync(forceSyncFileTreeNow) 并发触发；
 * 多次落盘抢同一临时文件（清单 .tmp / 镜像 .beacon-tmp），一方 move 走后另一方 move 找不到源
 * → NoSuchFileException 抛到 Bukkit 调度器（能自愈但刷异常栈、首次落盘可能失败）。
 * 锁定：并发 apply 全程不抛任何异常、清单最终可读一致、无临时文件残留。
 */
class FileTreeApplierConcurrencyTest {

    private val root: File = Files.createTempDirectory("beacon-applier-conc").toFile()
    private val manifestFile = File(root, "_applied.json")

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
    }

    @Test
    fun `并发 apply 不抛异常且清单最终一致无残留`() {
        val codec = TinyCodec()
        val applier = FileTreeApplier(
            mirrorWriter = FileMirrorWriter(root),
            appliedStore = AppliedFileManifestStore(manifestFile, codec),
            adapter = SilentAdapter(root),
            fetchContent = { p -> FileContent(p, "md5-$p", "content-$p") },
        )

        val threads = 4
        val iterations = 120
        val errors = CopyOnWriteArrayList<Throwable>()
        val start = CountDownLatch(1)
        val workers = (0 until threads).map { t ->
            Thread {
                start.await()
                repeat(iterations) { i ->
                    // 每次 fileTreeMd5 不同 → 必走真落盘（不被幂等守卫短路）→ 抢同一清单 tmp。
                    val manifest = FileManifest(
                        namespace = "prod", serverId = "s", group = "g", zone = "z",
                        fileTreeMd5 = "t-$t-$i",
                        entries = listOf(FileManifestEntry("f$t.yml", "$i")),
                    )
                    try {
                        applier.apply(manifest)
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
            "并发 apply 不应抛异常，实际：${errors.map { it.javaClass.simpleName + ":" + it.message }}",
        )
        // 清单可读、为某次写入的 md5（非半截损坏）。
        val applied = AppliedFileManifestStore(manifestFile, codec).read()
        assertTrue(
            applied != null && applied.fileTreeMd5.startsWith("t-"),
            "清单应可读且为某次写入值，实际：${applied?.fileTreeMd5}",
        )
        // 无任何临时文件残留。
        val residue = root.listFiles()
            ?.filter { it.name.contains(".beacon-tmp") || it.name.endsWith(".tmp") }
            ?: emptyList()
        assertTrue(residue.isEmpty(), "不应残留临时文件，实际：${residue.map { it.name }}")
    }

    /** 静默 adapter：runAsync 同步执行，日志 no-op。 */
    private class SilentAdapter(private val folder: File) : PlatformAdapter {
        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = folder
        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun info(msg: String) {}
        override fun warn(msg: String) {}
        override fun error(msg: String, t: Throwable?) {}
    }

    /** 最小 JsonCodec：仅需写后读回 fileTreeMd5（entries 容错为空，本测试不依赖）。 */
    private class TinyCodec : JsonCodec {
        override fun encode(value: Any?): String {
            val tree = value as Map<*, *>
            val md5 = tree["fileTreeMd5"] as String
            return """{"fileTreeMd5":"$md5"}"""
        }

        override fun decode(json: String): Any? {
            val md5 = Regex("\"fileTreeMd5\":\"([^\"]*)\"").find(json)?.groupValues?.get(1) ?: ""
            return mapOf("fileTreeMd5" to md5, "entries" to emptyList<Map<String, String>>())
        }
    }
}

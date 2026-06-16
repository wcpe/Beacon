package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.RecordingPlatformAdapter
import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.FileContent
import top.wcpe.beacon.agent.core.filetree.FileManifest
import top.wcpe.beacon.agent.core.filetree.FileManifestEntry
import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * FileTreeApplier 端到端落盘 + fail-static 单测（真实 KotlinxJsonCodec + 真实文件系统）。
 *
 * 重点：fail-static——取内容失败时不删既有文件、不写 applied 清单，保留本地镜像不动。
 */
class FileTreeApplierTest {

    private val codec = KotlinxJsonCodec()
    private val baseDir: File = Files.createTempDirectory("beacon-ft").toFile()
    private val mirrorRoot = File(baseDir, "plugins").apply { mkdirs() }
    private val dataFolder = File(baseDir, "data").apply { mkdirs() }
    private val adapter = RecordingPlatformAdapter(dataFolder)

    @AfterTest
    fun cleanup() {
        baseDir.deleteRecursively()
    }

    /** 构造 applier；fetch 由测试传入的内容表查表（缺失即返回 null 模拟取不到）。 */
    private fun applier(contents: Map<String, String>): FileTreeApplier {
        return FileTreeApplier(
            mirrorWriter = FileMirrorWriter(mirrorRoot),
            appliedStore = AppliedFileManifestStore(File(dataFolder, "applied.json"), codec),
            adapter = adapter,
            fetchContent = { path ->
                contents[path]?.let { FileContent(path, "md5-of-$path", it) }
            },
        )
    }

    private fun manifest(md5: String, vararg entries: Pair<String, String>): FileManifest = FileManifest(
        namespace = "prod",
        serverId = "lobby-1",
        group = "area1",
        zone = "zoneA",
        fileTreeMd5 = md5,
        entries = entries.map { FileManifestEntry(it.first, it.second) },
    )

    private fun mirror(path: String) = File(mirrorRoot, path)

    @Test
    fun `首次同步全部落盘并写清单`() {
        val ok = applier(mapOf("a.yml" to "AAA", "dir/b.js" to "BBB"))
            .apply(manifest("t1", "a.yml" to "1", "dir/b.js" to "2"))
        assertTrue(ok)
        assertEquals("AAA", mirror("a.yml").readText(StandardCharsets.UTF_8))
        assertEquals("BBB", mirror("dir/b.js").readText(StandardCharsets.UTF_8))
        // 清单已落盘记录新 md5。
        assertEquals("t1", AppliedFileManifestStore(File(dataFolder, "applied.json"), codec).read()!!.fileTreeMd5)
    }

    @Test
    fun `相同 fileTreeMd5 幂等跳过`() {
        applier(mapOf("a.yml" to "AAA")).apply(manifest("t1", "a.yml" to "1"))
        // 同 md5 再来一发：fetch 表清空也不应触发取内容（直接跳过）。
        val ok = applier(emptyMap()).apply(manifest("t1", "a.yml" to "1"))
        assertTrue(ok)
        // 文件保持不变。
        assertEquals("AAA", mirror("a.yml").readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `高层删 path 时删除本地镜像`() {
        applier(mapOf("a.yml" to "AAA", "b.yml" to "BBB")).apply(manifest("t1", "a.yml" to "1", "b.yml" to "2"))
        assertTrue(mirror("b.yml").exists())
        // 新清单移除 b.yml。
        val ok = applier(emptyMap()).apply(manifest("t2", "a.yml" to "1"))
        assertTrue(ok)
        assertTrue(mirror("a.yml").exists())
        assertFalse(mirror("b.yml").exists(), "目标已无的 path 应删除本地镜像")
    }

    @Test
    fun `fail-static 取内容失败时不删既有 不写清单`() {
        // 先建立已落盘态 t1（a.yml 在盘上）。
        applier(mapOf("a.yml" to "AAA")).apply(manifest("t1", "a.yml" to "1"))
        assertEquals("AAA", mirror("a.yml").readText(StandardCharsets.UTF_8))

        // 新目标 t2：新增 new.yml，但 fetch 表为空（取不到内容）→ 必须放弃整轮。
        val ok = applier(emptyMap()).apply(manifest("t2", "a.yml" to "1", "new.yml" to "9"))
        assertFalse(ok, "取内容失败应放弃本轮")
        // 既有文件不动。
        assertTrue(mirror("a.yml").exists(), "fail-static：不得删除既有文件")
        assertEquals("AAA", mirror("a.yml").readText(StandardCharsets.UTF_8))
        // 新文件未落盘。
        assertFalse(mirror("new.yml").exists())
        // 清单仍停留在 t1（未写入 t2）。
        assertEquals("t1", AppliedFileManifestStore(File(dataFolder, "applied.json"), codec).read()!!.fileTreeMd5)
    }

    @Test
    fun `首启无清单且取内容失败时不臆测删任何文件`() {
        // 无 applied 清单（首启）；目标要新增文件但取不到 → 放弃，不写清单。
        val ok = applier(emptyMap()).apply(manifest("t1", "x.yml" to "1"))
        assertFalse(ok)
        assertFalse(mirror("x.yml").exists())
        assertNull(AppliedFileManifestStore(File(dataFolder, "applied.json"), codec).read())
    }

    @Test
    fun `更新文件覆盖旧内容`() {
        applier(mapOf("a.yml" to "OLD")).apply(manifest("t1", "a.yml" to "1"))
        val ok = applier(mapOf("a.yml" to "NEW")).apply(manifest("t2", "a.yml" to "2"))
        assertTrue(ok)
        assertEquals("NEW", mirror("a.yml").readText(StandardCharsets.UTF_8))
    }
}

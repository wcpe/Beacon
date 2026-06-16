package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.FileManifestEntry
import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** AppliedFileManifestStore 原子读写往返一致性（用真实 KotlinxJsonCodec）。 */
class AppliedFileManifestStoreTest {

    private val codec = KotlinxJsonCodec()
    private val dir: File = Files.createTempDirectory("beacon-applied").toFile()

    @AfterTest
    fun cleanup() {
        dir.deleteRecursively()
    }

    private fun store() = AppliedFileManifestStore(File(dir, "applied.json"), codec)

    @Test
    fun `写后读回与原值一致`() {
        val store = store()
        store.write(
            "tree-md5-abc",
            listOf(
                FileManifestEntry("ui-components/main.allin", "9f"),
                FileManifestEntry("scripts/hello.js", "77"),
            ),
        )
        val read = store.read()!!
        assertEquals("tree-md5-abc", read.fileTreeMd5)
        assertEquals(2, read.entries.size)
        assertEquals(mapOf("ui-components/main.allin" to "9f", "scripts/hello.js" to "77"), read.toMap())
    }

    @Test
    fun `文件不存在时读返回 null`() {
        assertNull(store().read())
    }

    @Test
    fun `写入采用原子替换不残留 tmp`() {
        store().write("m", emptyList())
        val tmp = File(dir, "applied.json.tmp")
        assertFalse(tmp.exists(), "tmp 文件应在原子重命名后消失")
        assertTrue(File(dir, "applied.json").exists())
    }
}

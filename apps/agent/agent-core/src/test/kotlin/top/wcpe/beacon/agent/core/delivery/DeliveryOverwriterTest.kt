package top.wcpe.beacon.agent.core.delivery

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 交付覆盖器 [DeliveryOverwriter] 单测（FR-165，spec §4.2.3 / §4.5.3）：
 * - plan 本地重判：同 hash 跳过、缺失新增、异 hash 覆盖、delete 项存在则删 / 不存在则跳过；
 * - 路径越权 / 落在 agent 自身目录 → 拒绝（抛异常，整单失败）；
 * - apply：从临时目录原子入位、delete 项删除、skip 项不动。
 */
class DeliveryOverwriterTest {
    private val serverRoot: File = DeliveryTestSupport.tempDir("delivery-ow-root")
    private val dataDir: File = File(serverRoot, "plugins/BeaconAgent").apply { mkdirs() }
    private val overwriter = DeliveryOverwriter(DeliveryTargetResolver(serverRoot, dataDir))

    @Test
    fun `plan 本地重判分类正确`() {
        val same = "SAME".toByteArray()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/same.txt", same)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/diff.txt", "OLD".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", "X".toByteArray())

        val plan =
            overwriter.plan(
                listOf(
                    file("plugins/same.txt", "update", DeliveryTestSupport.sha256(same)), // 同 hash
                    file("plugins/diff.txt", "update", DeliveryTestSupport.sha256("NEW".toByteArray())), // 异 hash
                    file("plugins/new.txt", "add", DeliveryTestSupport.sha256("NEW2".toByteArray())), // 缺失
                    file("plugins/del.txt", "delete", ""), // delete 且存在
                    file("plugins/gone.txt", "delete", ""), // delete 但不存在
                ),
            )

        assertEquals(DeliveryFileOp.Kind.SKIP, kindOf(plan, "plugins/same.txt"))
        assertEquals(DeliveryFileOp.Kind.UPDATE, kindOf(plan, "plugins/diff.txt"))
        assertEquals(DeliveryFileOp.Kind.ADD, kindOf(plan, "plugins/new.txt"))
        assertEquals(DeliveryFileOp.Kind.DELETE, kindOf(plan, "plugins/del.txt"))
        assertEquals(DeliveryFileOp.Kind.SKIP, kindOf(plan, "plugins/gone.txt"))
    }

    @Test
    fun `plan 拒绝路径越权`() {
        assertFailsWith<DeliveryPathException> {
            overwriter.plan(listOf(file("../evil.txt", "update", "deadbeef")))
        }
    }

    @Test
    fun `plan 拒绝落在 agent 自身目录的目标`() {
        assertFailsWith<DeliveryPathException> {
            overwriter.plan(listOf(file("plugins/BeaconAgent/secret.txt", "update", "deadbeef")))
        }
    }

    @Test
    fun `apply 原子入位与删除跳过不动`() {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/diff.txt", "OLD".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", "X".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/same.txt", "SAME".toByteArray())
        val tempDir = DeliveryTestSupport.tempDir("delivery-ow-tmp")
        DeliveryTestSupport.writeFile(tempDir, "plugins/new.txt", "NEW2".toByteArray())
        DeliveryTestSupport.writeFile(tempDir, "plugins/diff.txt", "NEW".toByteArray())
        val ops =
            listOf(
                op("plugins/new.txt", DeliveryFileOp.Kind.ADD),
                op("plugins/diff.txt", DeliveryFileOp.Kind.UPDATE),
                op("plugins/del.txt", DeliveryFileOp.Kind.DELETE),
                op("plugins/same.txt", DeliveryFileOp.Kind.SKIP),
            )

        val changed = overwriter.apply(ops, tempDir)

        assertEquals(3, changed)
        assertEquals("NEW2", File(serverRoot, "plugins/new.txt").readText())
        assertEquals("NEW", File(serverRoot, "plugins/diff.txt").readText())
        assertFalse(File(serverRoot, "plugins/del.txt").exists())
        assertTrue(File(serverRoot, "plugins/same.txt").exists())
        assertEquals("SAME", File(serverRoot, "plugins/same.txt").readText())
    }

    private fun kindOf(
        plan: List<DeliveryFileOp>,
        path: String,
    ): DeliveryFileOp.Kind = plan.first { it.path == path }.kind

    private fun file(
        path: String,
        action: String,
        sha: String,
    ) = DeliveryManifestFile(path, action, sha, 0L)

    private fun op(
        path: String,
        kind: DeliveryFileOp.Kind,
    ) = DeliveryFileOp(path, kind, "", 0L)
}

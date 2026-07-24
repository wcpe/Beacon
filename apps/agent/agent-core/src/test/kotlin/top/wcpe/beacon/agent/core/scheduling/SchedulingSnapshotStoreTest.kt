package top.wcpe.beacon.agent.core.scheduling

import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** [SchedulingSnapshotStore] 单测（FR-148）：落盘 + 读盘往返、缺文件 / 损坏返 null。 */
class SchedulingSnapshotStoreTest {
    private fun tempFile(name: String): File {
        val dir = Files.createTempDirectory("sched-snap").toFile()
        return File(dir, name)
    }

    @Test
    fun `写入后读回内容一致`() {
        val file = tempFile("candidates-snapshot.json")
        val store = SchedulingSnapshotStore(file, RoundTripCodec())
        val snapshot =
            CandidateSnapshot(
                generatedAtMs = 1_700L,
                savedAtMs = 1_800L,
                zones =
                    linkedMapOf(
                        "z-a" to listOf(candidateEntry("lobby-1", 90, "healthy", true, 3, 100)),
                        "z-b" to listOf(candidateEntry("lobby-9", 55, "degraded", true, 1, 50)),
                    ),
            )
        store.write(snapshot)

        val loaded = store.read()
        assertEquals(1_700L, loaded?.generatedAtMs)
        assertEquals(1_800L, loaded?.savedAtMs)
        assertEquals(setOf("z-a", "z-b"), loaded?.zones?.keys)
        val a = loaded?.zones?.get("z-a")?.single()
        assertEquals("lobby-1", a?.serverId)
        assertEquals(90, a?.score)
        assertEquals("healthy", a?.level)
        assertEquals(100, a?.maxOnline)
    }

    @Test
    fun `文件不存在返回 null`() {
        val store = SchedulingSnapshotStore(tempFile("missing.json"), RoundTripCodec())
        assertNull(store.read())
    }

    @Test
    fun `损坏文件返回 null`() {
        val file = tempFile("corrupt.json")
        file.writeText("{ 非法", StandardCharsets.UTF_8)
        // RoundTripCodec.decode 找不到 token 返回空 map → 解析出零 zones，仍是合法（空）快照而非崩溃；
        // 用抛错 codec 验证解析异常被吞为 null。
        val throwingCodec =
            object : top.wcpe.beacon.agent.core.transport.JsonCodec {
                override fun encode(value: Any?): String = "x"

                override fun decode(json: String): Any? = throw RuntimeException("解析失败")
            }
        assertNull(SchedulingSnapshotStore(file, throwingCodec).read())
    }
}

package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** [SchedulingSnapshotStore] 单测（FR-148）：落盘 + 读盘往返、缺文件 / 损坏返 null。 */
class SchedulingSnapshotStoreTest {
    /** 记下最后一次落盘的泛型树并按同一棵树读回：让用例能直接断「这个键到底写没写」。 */
    private class TreeCapturingCodec : JsonCodec {
        @Volatile
        var lastEncoded: Any? = null

        override fun encode(value: Any?): String {
            lastEncoded = value
            return "captured"
        }

        override fun decode(json: String): Any? = lastEncoded
    }

    /** 从落盘树里取某个 zone 的候选树（走与生产同一套 [JsonTree] 读取口，不额外加类型假设）。 */
    private fun candidateTrees(
        tree: Any?,
        zone: String,
    ): List<Map<String, Any?>> {
        val root = JsonTree.asObject(tree)
        val hit =
            JsonTree
                .asList(root["zones"])
                .map(JsonTree::asObject)
                .first { JsonTree.strOr(it, "zone", "") == zone }
        return JsonTree.asList(hit["candidates"]).map(JsonTree::asObject)
    }

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
                        "z-a" to listOf(candidateEntry("lobby-1", 90, "healthy", true, 3, 100, labelsPresent = true)),
                        "z-b" to listOf(candidateEntry("lobby-9", 55, "degraded", true, 1, 50, labelsPresent = false)),
                    ),
                lobby = LobbyCandidates(12L, true, listOf(candidateEntry("lobby-1", 90, "healthy", true, 3, 100, labelsPresent = true))),
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
        assertEquals(12L, loaded?.lobby?.clusterId)
        assertEquals("lobby-1", loaded?.lobby?.candidates?.single()?.serverId)
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

    @Test
    fun `看得到的标签随快照一起落盘并被原样恢复`() {
        val file = tempFile("labels.json")
        val store = SchedulingSnapshotStore(file, RoundTripCodec())
        store.write(
            CandidateSnapshot(
                generatedAtMs = 1_700L,
                savedAtMs = 1_800L,
                zones =
                    linkedMapOf(
                        "z-a" to
                            listOf(
                                candidateEntry(
                                    "lobby-1",
                                    90,
                                    labels = mapOf("example.zone.a" to "true"),
                                    labelsPresent = true,
                                ),
                                // 空标签集但键在：这是"这台没声明过"这一稳定事实，不能退化成"看不到"。
                                candidateEntry("lobby-2", 70, labelsPresent = true),
                            ),
                    ),
                // 大厅段与 zones 走同一对序列化函数，一并锁上（漏一处就会在重启后出现两套口径）。
                lobby =
                    LobbyCandidates(
                        12L,
                        true,
                        listOf(
                            candidateEntry(
                                "lobby-9",
                                60,
                                labels = mapOf("k" to "v"),
                                labelsPresent = true,
                            ),
                        ),
                    ),
            ),
        )

        val restored = store.read()
        val byId = restored?.zones?.get("z-a").orEmpty().associateBy { it.serverId }
        assertTrue(byId.getValue("lobby-1").labelsPresent, "键在 → 重启恢复后仍看得到声明")
        assertEquals(mapOf("example.zone.a" to "true"), byId.getValue("lobby-1").labels, "标签内容要原样回来")
        assertTrue(byId.getValue("lobby-2").labelsPresent, "空标签集也是「看得到」")
        assertTrue(byId.getValue("lobby-2").labels.isEmpty())
        val lobbyCandidate = restored?.lobby?.candidates?.single()
        assertTrue(lobbyCandidate?.labelsPresent == true, "大厅段候选同样带出可见性")
        assertEquals(mapOf("k" to "v"), lobbyCandidate?.labels)
    }

    @Test
    fun `看不到标签的候选落盘时不写 labels 键`() {
        val file = tempFile("legacy.json")
        val codec = TreeCapturingCodec()
        SchedulingSnapshotStore(file, codec).write(
            CandidateSnapshot(
                generatedAtMs = 1_700L,
                savedAtMs = 1_800L,
                zones =
                    linkedMapOf(
                        // 旧格式来源（旧控制面响应 / 自旧格式恢复）：看不到就是看不到，落盘不得自造空 map 冒充"没声明"。
                        "z-a" to listOf(candidateEntry("lobby-1", 90, labelsPresent = false)),
                    ),
                lobby = LobbyCandidates(12L, true, listOf(candidateEntry("lobby-9", 60, labelsPresent = false))),
            ),
        )

        val candidateTree = candidateTrees(codec.lastEncoded, "z-a").single()
        assertFalse(candidateTree.containsKey("labels"), "看不到声明的候选不得落 labels 键")

        val restored = SchedulingSnapshotStore(file, codec).read()
        assertFalse(restored?.zones?.get("z-a")?.single()?.labelsPresent ?: true, "恢复后仍是「看不到」")
        assertFalse(restored?.lobby?.candidates?.single()?.labelsPresent ?: true, "大厅段同样保真")
    }
}

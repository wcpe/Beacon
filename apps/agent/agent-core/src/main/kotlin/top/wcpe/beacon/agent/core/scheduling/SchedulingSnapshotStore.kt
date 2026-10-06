package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.filetree.AtomicFileWriter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets

/**
 * 候选快照 fail-static 读写（FR-148）：把最近一次候选缓存原子落盘到 `candidates-snapshot.json`，
 * agent 重启后凭它继续降级决策（重启后仍可用，§4.6 降级路径 step 1）。
 *
 * 与配置快照 [SnapshotStore][top.wcpe.beacon.agent.core.snapshot.SnapshotStore] 平行：同用 [AtomicFileWriter]
 * 原子写（唯一 tmp → force → 重命名覆盖 + 父目录 fsync，Windows 安全），同用 [JsonCodec] 编解码。
 *
 * @param file  快照落点（dataFolder/candidates-snapshot.json）
 * @param codec JSON 编解码
 */
class SchedulingSnapshotStore(
    private val file: File,
    private val codec: JsonCodec,
) {
    /** 原子写候选快照。失败抛 IO 异常由上层（fail-static）记录并保留内存快照。 */
    fun write(snapshot: CandidateSnapshot) {
        val tree = LinkedHashMap<String, Any?>()
        tree["generatedAtMs"] = snapshot.generatedAtMs
        tree["savedAt"] = snapshot.savedAtMs
        tree["zones"] =
            snapshot.zones.map { (zone, candidates) ->
                linkedMapOf<String, Any?>(
                    "zone" to zone,
                    "candidates" to candidates.map { candidateTree(it) },
                )
            }
        snapshot.lobby?.let { lobby ->
            tree["lobby"] =
                linkedMapOf<String, Any?>(
                    "clusterId" to lobby.clusterId,
                    "ready" to lobby.ready,
                    "candidates" to lobby.candidates.map { candidateTree(it) },
                )
        }
        AtomicFileWriter.write(file, codec.encode(tree).toByteArray(StandardCharsets.UTF_8))
    }

    /** 读候选快照；文件不存在或解析失败返回 null（fail-static 容忍）。 */
    fun read(): CandidateSnapshot? {
        if (!file.exists()) {
            return null
        }
        return try {
            val obj = JsonTree.asObject(codec.decode(file.readText(StandardCharsets.UTF_8)))
            val zones = LinkedHashMap<String, List<CandidateEntry>>()
            for (rawZone in JsonTree.asList(obj["zones"])) {
                val zoneObj = JsonTree.asObject(rawZone)
                val zone = JsonTree.strOr(zoneObj, "zone", "")
                if (zone.isEmpty()) {
                    continue
                }
                zones[zone] = JsonTree.asList(zoneObj["candidates"]).map { parseCandidate(it) }
            }
            val lobby =
                (obj["lobby"] as? Map<*, *>)?.let { rawLobby ->
                    val lobbyObj = JsonTree.asObject(rawLobby)
                    LobbyCandidates(
                        clusterId = JsonTree.longOr(lobbyObj, "clusterId", 0L),
                        ready = JsonTree.boolOr(lobbyObj, "ready", false),
                        candidates = JsonTree.asList(lobbyObj["candidates"]).map { parseCandidate(it) },
                    )
                }
            CandidateSnapshot(
                generatedAtMs = JsonTree.longOr(obj, "generatedAtMs", 0L),
                savedAtMs = JsonTree.longOr(obj, "savedAt", 0L),
                zones = zones,
                lobby = lobby,
            )
        } catch (e: Exception) {
            null
        }
    }

    /**
     * 候选条目 → 落盘树（zones 与 lobby 两侧共用同一对函数，落盘 / 还原口径只此一处）。
     *
     * <p>`labels` 键**只在** [CandidateEntry.labelsPresent] 为 true 时写入（空 map 也写 `{}`）：
     * 该键的存在与否就是"本帧来源携带没携带自声明标签字段"的信号，
     * 为 false 时不落这个键，恢复后才仍读成"看不到"——落盘不能把「看不到」升级成「没有声明」。
     * 少了这一步，重启恢复后的快照会一律被判成"判据不可见"，带作用域的降级决策凭空失真。</p>
     */
    private fun candidateTree(entry: CandidateEntry): Map<String, Any?> {
        val tree =
            linkedMapOf<String, Any?>(
                "serverId" to entry.serverId,
                "score" to entry.score,
                "level" to entry.level,
                "schedulable" to entry.schedulable,
                "onlineCount" to entry.onlineCount,
                "maxOnline" to entry.maxOnline,
                "reasons" to entry.reasons,
            )
        if (entry.labelsPresent) {
            tree["labels"] = entry.labels
        }
        return tree
    }

    private fun parseCandidate(raw: Any?): CandidateEntry {
        val obj = JsonTree.asObject(raw)
        return CandidateEntry(
            serverId = JsonTree.strOr(obj, "serverId", ""),
            score = JsonTree.intOr(obj, "score", 0),
            level = JsonTree.strOr(obj, "level", ""),
            schedulable = JsonTree.boolOr(obj, "schedulable", true),
            onlineCount = JsonTree.intOr(obj, "onlineCount", 0),
            maxOnline = JsonTree.intOr(obj, "maxOnline", 0),
            reasons = JsonTree.asList(obj["reasons"]).map(JsonTree::asString),
            // 键在不在决定"看不看得到声明"：在 → 支持该字段（空 map 即"这台没声明过"）；不在 → 看不到。
            labels = JsonTree.strMap(obj, "labels"),
            labelsPresent = obj.containsKey("labels"),
        )
    }
}

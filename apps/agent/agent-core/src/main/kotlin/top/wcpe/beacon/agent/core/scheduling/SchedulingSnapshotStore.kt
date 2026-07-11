package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.JsonTree
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
            CandidateSnapshot(
                generatedAtMs = JsonTree.longOr(obj, "generatedAtMs", 0L),
                savedAtMs = JsonTree.longOr(obj, "savedAt", 0L),
                zones = zones,
            )
        } catch (e: Exception) {
            null
        }
    }

    private fun candidateTree(entry: CandidateEntry): Map<String, Any?> =
        linkedMapOf(
            "serverId" to entry.serverId,
            "score" to entry.score,
            "level" to entry.level,
            "schedulable" to entry.schedulable,
            "onlineCount" to entry.onlineCount,
            "maxOnline" to entry.maxOnline,
        )

    private fun parseCandidate(raw: Any?): CandidateEntry {
        val obj = JsonTree.asObject(raw)
        return CandidateEntry(
            serverId = JsonTree.strOr(obj, "serverId", ""),
            score = JsonTree.intOr(obj, "score", 0),
            level = JsonTree.strOr(obj, "level", ""),
            schedulable = JsonTree.boolOr(obj, "schedulable", true),
            onlineCount = JsonTree.intOr(obj, "onlineCount", 0),
            maxOnline = JsonTree.intOr(obj, "maxOnline", 0),
        )
    }
}

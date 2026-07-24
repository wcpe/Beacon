package top.wcpe.beacon.agent.core.filetree

import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.command.AssetEntry
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets

/**
 * 本地文件资产清单缓存（asset-manifest.json）的原子读写，存 agent 数据目录（FR-163，见 ADR asset-manifest-sync-protocol）。
 *
 * 内容 {confirmedDigest, savedAt, entries:[{path,sha256,size,mtimeMs,isText}]}：记录上次成功上报后的清单与控制面确认摘要。
 * 重启后缓存可用则继续增量（(size,mtime) 命中复用哈希、baseDigest 续上）；缺失 / 损坏则回退全量（fail-static）。
 *
 * 原子写委托 [AtomicFileWriter]（唯一 tmp → `FileChannel.force` → 重命名覆盖 + 父目录 fsync），与已落盘文件清单同源。
 *
 * @param file  清单落点（dataFolder/<fileName>）
 * @param codec JSON 编解码
 * @param now   当前时间提供者（毫秒），便于测试
 */
class AssetManifestStore(
    private val file: File,
    private val codec: JsonCodec,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    /** 原子写清单缓存。失败抛 IO 异常由上层记录。 */
    fun write(
        confirmedDigest: String,
        entries: List<AssetEntry>,
    ) {
        val tree = LinkedHashMap<String, Any?>()
        tree["confirmedDigest"] = confirmedDigest
        tree["savedAt"] = now()
        tree["entries"] =
            entries.map { e ->
                linkedMapOf<String, Any?>(
                    "path" to e.path,
                    "sha256" to e.sha256,
                    "size" to e.size,
                    "mtimeMs" to e.mtimeMs,
                    "isText" to e.isText,
                )
            }
        AtomicFileWriter.write(file, codec.encode(tree).toByteArray(StandardCharsets.UTF_8))
    }

    /**
     * 读清单缓存；文件不存在或解析失败返回 null（fail-static：无缓存则回退全量）。
     *
     * 捕获宽泛异常而非仅 IOException：损坏的 JSON 会由 codec 抛序列化异常，必须一并兜底归 null，
     * 否则损坏缓存会每周期抛错卡死扫描、无法自愈（治本靠回退全量覆盖损坏文件）。
     */
    fun read(): StoredAssetManifest? {
        if (!file.exists()) return null
        return try {
            val obj = JsonTree.asObject(codec.decode(file.readText(StandardCharsets.UTF_8)))
            val entries =
                JsonTree.asList(obj["entries"]).map { raw ->
                    val itemObj = JsonTree.asObject(raw)
                    AssetEntry(
                        path = JsonTree.strOr(itemObj, "path", ""),
                        sha256 = JsonTree.strOr(itemObj, "sha256", ""),
                        size = JsonTree.longOr(itemObj, "size", 0L),
                        mtimeMs = JsonTree.longOr(itemObj, "mtimeMs", 0L),
                        isText = JsonTree.boolOr(itemObj, "isText", true),
                    )
                }
            StoredAssetManifest(
                confirmedDigest = JsonTree.strOr(obj, "confirmedDigest", ""),
                entries = entries,
            )
        } catch (e: Exception) {
            null
        }
    }
}

/**
 * 本地文件资产清单缓存的内存视图。
 *
 * @param confirmedDigest 控制面确认的清单摘要（增量 baseDigest）
 * @param entries         上次成功上报的清单条目
 */
data class StoredAssetManifest(
    val confirmedDigest: String,
    val entries: List<AssetEntry>,
) {
    /** 转 path→entry 映射（增量 (size,mtime) 命中复用哈希、算 upserts/deleted 用）。 */
    fun entriesMap(): Map<String, AssetEntry> = entries.associateBy { it.path }
}

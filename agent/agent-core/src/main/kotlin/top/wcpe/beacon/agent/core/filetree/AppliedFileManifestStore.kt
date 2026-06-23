package top.wcpe.beacon.agent.core.filetree

import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets

/**
 * 本地「已落盘文件清单」（applied-file-manifest）的原子读写，存 agent 数据目录。
 *
 * 内容 {fileTreeMd5, savedAt, entries:[{path,md5}]}。它是「本地镜像已是哪一版」的真源：
 * agent 据它与控制面 manifest 比对增量同步、长轮询续杯比对 fileTreeMd5。
 *
 * 原子写委托 [AtomicFileWriter]：唯一临时文件 → `FileChannel.force` → 重命名覆盖 + 父目录 fsync（见 ADR-0010 决策4）。
 * 「先文件后清单」持久化序：先把变更文件落盘并 fsync，再写本清单——崩溃恢复后清单只反映已落盘的部分。
 *
 * @param file  清单落点（dataFolder/<fileName>）
 * @param codec JSON 编解码
 * @param now   当前时间提供者（毫秒），便于测试
 */
class AppliedFileManifestStore(
    private val file: File,
    private val codec: JsonCodec,
    private val now: () -> Long = { System.currentTimeMillis() },
) {

    /** 原子写已落盘清单。失败抛 IO 异常由上层记录。 */
    fun write(fileTreeMd5: String, entries: List<FileManifestEntry>) {
        val tree = LinkedHashMap<String, Any?>()
        tree["fileTreeMd5"] = fileTreeMd5
        tree["savedAt"] = now()
        tree["entries"] = entries.map { e ->
            linkedMapOf<String, Any?>("path" to e.path, "md5" to e.md5)
        }
        val json = codec.encode(tree)
        // 原子写委托 AtomicFileWriter（唯一 tmp + 重命名回退/重试 + 父目录 fsync），消除 Windows 并发竞争。
        AtomicFileWriter.write(file, json.toByteArray(StandardCharsets.UTF_8))
    }

    /**
     * 读已落盘清单；文件不存在或解析失败返回 null（fail-static：无清单则不动既有文件）。
     */
    fun read(): AppliedFileManifest? {
        if (!file.exists()) return null
        return try {
            val obj = JsonTree.asObject(codec.decode(file.readText(StandardCharsets.UTF_8)))
            val entries = JsonTree.asList(obj["entries"]).map { raw ->
                val itemObj = JsonTree.asObject(raw)
                FileManifestEntry(
                    path = JsonTree.strOr(itemObj, "path", ""),
                    md5 = JsonTree.strOr(itemObj, "md5", ""),
                )
            }
            AppliedFileManifest(
                fileTreeMd5 = JsonTree.strOr(obj, "fileTreeMd5", ""),
                entries = entries,
            )
        } catch (e: IOException) {
            null
        }
    }
}

/**
 * 本地已落盘文件清单（applied-file-manifest 的内存视图）。
 *
 * @param fileTreeMd5 已落盘那一版的整树指纹（长轮询续杯比对用）
 * @param entries     已落盘的 path→md5 列表
 */
data class AppliedFileManifest(
    val fileTreeMd5: String,
    val entries: List<FileManifestEntry>,
) {
    /** 转成 path→md5 映射（差分用）。 */
    fun toMap(): Map<String, String> = entries.associate { it.path to it.md5 }
}

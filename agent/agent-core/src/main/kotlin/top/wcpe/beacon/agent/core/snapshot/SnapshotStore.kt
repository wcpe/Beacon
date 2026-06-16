package top.wcpe.beacon.agent.core.snapshot

import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.StandardCopyOption

/**
 * 本地快照 fail-static 读写：{namespace,serverId,group,zone,md5,savedAt,items:[...]}（无 version）。
 *
 * 原子写：先写 *.tmp 再 rename 覆盖，避免半截文件。用 JsonCodec 编解码。
 *
 * @param file  快照落点（dataFolder/<fileName>）
 * @param codec JSON 编解码
 * @param now   当前时间提供者（毫秒），便于测试
 */
class SnapshotStore(
    private val file: File,
    private val codec: JsonCodec,
    private val now: () -> Long = { System.currentTimeMillis() },
) {

    /** 原子写快照。失败抛 IO 异常由上层记录。 */
    fun write(result: EffectiveResult) {
        val tree = LinkedHashMap<String, Any?>()
        tree["namespace"] = result.namespace
        tree["serverId"] = result.serverId
        tree["group"] = result.group
        tree["zone"] = result.zone
        tree["md5"] = result.md5
        tree["savedAt"] = now()
        tree["items"] = result.items.map { item ->
            linkedMapOf<String, Any?>(
                "dataId" to item.dataId,
                "format" to item.format,
                "md5" to item.md5,
                "content" to item.content,
            )
        }
        val json = codec.encode(tree)

        val parent = file.parentFile
        if (parent != null && !parent.exists()) {
            parent.mkdirs()
        }
        val tmp = File(file.parentFile, file.name + ".tmp")
        tmp.writeText(json, StandardCharsets.UTF_8)
        // 原子重命名覆盖目标。
        Files.move(
            tmp.toPath(),
            file.toPath(),
            StandardCopyOption.REPLACE_EXISTING,
            StandardCopyOption.ATOMIC_MOVE,
        )
    }

    /** 读快照；文件不存在或解析失败返回 null（fail-static 容忍）。 */
    fun read(): EffectiveResult? {
        if (!file.exists()) return null
        return try {
            val obj = JsonTree.asObject(codec.decode(file.readText(StandardCharsets.UTF_8)))
            val items = JsonTree.asList(obj["items"]).map { raw ->
                val itemObj = JsonTree.asObject(raw)
                ConfigItem(
                    dataId = JsonTree.strOr(itemObj, "dataId", ""),
                    format = JsonTree.strOr(itemObj, "format", ""),
                    md5 = JsonTree.strOr(itemObj, "md5", ""),
                    content = JsonTree.strOr(itemObj, "content", ""),
                )
            }
            EffectiveResult(
                namespace = JsonTree.strOr(obj, "namespace", ""),
                serverId = JsonTree.strOr(obj, "serverId", ""),
                group = JsonTree.str(obj, "group"),
                zone = JsonTree.str(obj, "zone"),
                md5 = JsonTree.strOr(obj, "md5", ""),
                items = items,
            )
        } catch (e: Exception) {
            null
        }
    }
}

package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.filetree.AtomicFileWriter
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.time.Clock

/**
 * 交付覆盖前本地备份与保留清理（FR-165，spec §4.7.1）。
 *
 * 备份布局（必须落在 agent 自身 `dataFolder()` 之下，否则会被文件资产扫描漏掉反被纳入清单自我指涉，见 ADR-0070）：
 * ```
 * <dataFolder()>/delivery-backups/<orderId>/
 *   manifest.json          # [{path, action, sha256(旧), size}]；add 项只记 path+action（回滚删该文件）
 *   files/<原相对路径>      # 被覆盖 / 删除文件的原内容，按原相对路径存放
 * ```
 *
 * 备份成功后才允许覆盖；备份失败（IO 异常）抛出，由调用方判该目标 failed、**不动原文件**。
 * 保留策略：每服最多 [MAX_BACKUPS] 个变更单备份且最长 [RETENTION_DAYS] 天，超限按最旧清理（本地周期执行）。
 *
 * @param backupRoot 备份区根（`dataFolder()/delivery-backups`）
 * @param resolver   落盘目标解析与安全校验（读旧文件内容）
 * @param codec      JSON 编解码（写 manifest.json）
 * @param adapter    平台日志
 * @param clock      时钟（保留清理按 lastModified 判龄；测试可注入）
 */
class DeliveryBackupManager(
    private val backupRoot: File,
    private val resolver: DeliveryTargetResolver,
    private val codec: JsonCodec,
    private val adapter: PlatformAdapter,
    private val clock: Clock = Clock.systemUTC(),
) {
    /**
     * 覆盖 / 删除前备份本单工作集（非 SKIP 项）。返回是否已生成备份（backupPresent，回滚预检依据）。
     *
     * 每次调用先清空本单备份目录再重建（同单重推得到一致的干净备份集）；对 update / delete 项复制旧内容 +
     * 记旧哈希，对 add 项只记标记（回滚删该文件）。任一步 IO 失败抛 [IOException]——此时尚未触碰任何原文件。
     */
    fun backup(
        orderId: Long,
        ops: List<DeliveryFileOp>,
    ): Boolean {
        if (ops.isEmpty()) return false // 无工作集，无需备份
        val orderDir = File(backupRoot, orderId.toString())
        clearDir(orderDir)
        val filesDir = File(orderDir, FILES_SUBDIR)
        val entries = ArrayList<Map<String, Any?>>(ops.size)
        for (op in ops) {
            entries.add(backupOne(op, filesDir))
        }
        AtomicFileWriter.write(File(orderDir, MANIFEST_NAME), codec.encode(entries).toByteArray(StandardCharsets.UTF_8))
        adapter.info("交付备份完成：orderId=$orderId，条目=${entries.size}，位置=${orderDir.absolutePath}")
        return true
    }

    /** 备份单文件：add 只记标记；update / delete 复制旧内容并记旧哈希 / 大小。返回该项 manifest 条目。 */
    private fun backupOne(
        op: DeliveryFileOp,
        filesDir: File,
    ): Map<String, Any?> {
        if (op.kind == DeliveryFileOp.Kind.ADD) {
            // add 项目标覆盖前不存在，无旧内容可备；仅记标记，回滚时删除该新增文件。
            return backupEntry(op.path, DeliveryManifestFile.ACTION_ADD, "", 0L)
        }
        val target =
            resolver.resolve(op.path)
                ?: throw IOException("备份目标路径非法：${op.path}")
        val backupFile = File(filesDir, op.path)
        backupFile.parentFile?.mkdirs()
        // 流式复制旧内容到备份区（不整读入内存）。
        Files.copy(target.toPath(), backupFile.toPath(), StandardCopyOption.REPLACE_EXISTING)
        val oldSha = DeliverySha256.ofFile(backupFile) ?: ""
        val action = if (op.kind == DeliveryFileOp.Kind.DELETE) DeliveryManifestFile.ACTION_DELETE else DeliveryManifestFile.ACTION_UPDATE
        return backupEntry(op.path, action, oldSha, backupFile.length())
    }

    /** 组装一条备份 manifest 条目（键集与 M5 回滚读取口径一致）。 */
    private fun backupEntry(
        path: String,
        action: String,
        sha256: String,
        size: Long,
    ): Map<String, Any?> = mapOf("path" to path, "action" to action, "sha256" to sha256, "size" to size)

    /**
     * 按备份 manifest 还原本单磁盘变更（整单回滚，spec §4.7.2）：读 manifest.json 逐条反转——
     * update / delete 项从 `files/<path>` 备份还原到目标（覆盖前原内容 / 被删文件），add 项删除该新增文件。
     * 返回还原的文件数。manifest / 备份内容缺失抛 [IOException]（调用方据此回执 failed「备份不存在」）。
     */
    fun restore(orderId: Long): Int {
        val orderDir = File(backupRoot, orderId.toString())
        val manifestFile = File(orderDir, MANIFEST_NAME)
        if (!manifestFile.exists()) {
            throw IOException("回滚备份不存在：orderId=$orderId，位置=${orderDir.absolutePath}")
        }
        val filesDir = File(orderDir, FILES_SUBDIR)
        val entries = decodeManifest(manifestFile)
        for (entry in entries) {
            restoreOne(entry, filesDir)
        }
        adapter.info("交付回滚还原完成：orderId=$orderId，还原=${entries.size}，来源=${orderDir.absolutePath}")
        return entries.size
    }

    /** 解析备份 manifest（[{path, action, sha256, size}]）；结构非法抛 [IOException]。 */
    @Suppress("UNCHECKED_CAST")
    private fun decodeManifest(manifestFile: File): List<Map<String, Any?>> {
        val decoded = codec.decode(manifestFile.readText(StandardCharsets.UTF_8))
        if (decoded !is List<*>) {
            throw IOException("回滚备份 manifest 格式非法：${manifestFile.absolutePath}")
        }
        return decoded.map { it as? Map<String, Any?> ?: throw IOException("回滚备份 manifest 条目非法") }
    }

    /** 还原单条：add 项删目标文件（还原为不存在）；update / delete 项从备份复制旧内容回目标（经 resolver 路径校验）。 */
    @Suppress("ThrowsCount") // 还原每步校验（path/action/目标路径/备份内容）失败均须 IOException 上抛，供调用方回执 failed
    private fun restoreOne(
        entry: Map<String, Any?>,
        filesDir: File,
    ) {
        val path = entry["path"] as? String ?: throw IOException("回滚备份条目缺 path")
        val action = entry["action"] as? String ?: throw IOException("回滚备份条目缺 action")
        val target = resolver.resolve(path) ?: throw IOException("回滚目标路径非法：$path")
        if (action == DeliveryManifestFile.ACTION_ADD) {
            if (target.exists() && !target.delete()) {
                throw IOException("回滚删除新增文件失败：$path")
            }
            return
        }
        val backupFile = File(filesDir, path)
        if (!backupFile.exists()) {
            throw IOException("回滚备份内容缺失：$path")
        }
        target.parentFile?.mkdirs()
        Files.copy(backupFile.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
    }

    /**
     * 保留清理（spec §4.7.1）：删除超 [RETENTION_DAYS] 天的备份单，再把超 [MAX_BACKUPS] 个的最旧删除。
     *
     * 本地周期执行——由交付执行器在每次成功备份后机会式调用（新增备份时顺带修剪），无需另起调度循环。
     */
    fun enforceRetention() {
        val cutoff = clock.millis() - RETENTION_DAYS * MILLIS_PER_DAY
        val expired = backupDirs().filter { it.lastModified() < cutoff }
        for (dir in expired) deleteQuietly(dir)
        val remaining = backupDirs().sortedByDescending { it.lastModified() }
        for (dir in remaining.drop(MAX_BACKUPS)) deleteQuietly(dir)
    }

    /** 列出备份区下的各单备份目录（不存在 / 非目录返回空）。 */
    private fun backupDirs(): List<File> = backupRoot.listFiles()?.filter { it.isDirectory }.orEmpty()

    /** 清空并重建目录（同单重推得干净备份集）。 */
    private fun clearDir(dir: File) {
        if (dir.exists()) dir.deleteRecursively()
        dir.mkdirs()
    }

    /** 尽力删除一棵备份目录树；失败仅记 warn（保留清理非关键路径，不因残留崩流程）。 */
    private fun deleteQuietly(dir: File) {
        if (!dir.deleteRecursively()) {
            adapter.warn("交付备份保留清理未能完全删除：${dir.absolutePath}")
        } else {
            adapter.info("交付备份保留清理删除过期 / 超额备份：${dir.name}")
        }
    }

    private companion object {
        /** 备份 manifest 文件名。 */
        private const val MANIFEST_NAME = "manifest.json"

        /** 备份内容子目录名（按原相对路径存放旧文件）。 */
        private const val FILES_SUBDIR = "files"

        /** 每服最多保留的变更单备份数（spec §8 #5）。 */
        private const val MAX_BACKUPS = 5

        /** 备份最长保留天数（spec §8 #5）。 */
        private const val RETENTION_DAYS = 30L

        /** 一天的毫秒数。 */
        private const val MILLIS_PER_DAY = 24L * 60L * 60L * 1000L
    }
}

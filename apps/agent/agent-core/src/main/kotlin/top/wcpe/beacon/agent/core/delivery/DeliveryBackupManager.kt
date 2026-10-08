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
 * 同单重推**增量合并、绝不清空既有备份**，还原前按 manifest 的 sha256 / size 校验备份内容（FR-267）。
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
     * **增量合并既有备份，绝不清空本单备份目录（FR-267）**：对 update / delete 项复制旧内容 + 记旧哈希，
     * 对 add 项只记标记（回滚删该文件）；**已有同路径条目原样保留、绝不重备**——老条目记的是本单改动前的
     * 原始内容，同单重推时目标可能已被本单覆盖，重备会把覆盖后的内容冒充回滚点（回滚反把文件写坏）。
     * 既不清空也保证了「重推途中失败」不会毁掉既有回滚点（旧实现先清空目录，第二次推送失败即失唯一回滚点）。
     *
     * 既有 manifest 存在但无法解析 → 抛 [IOException]（fail-closed：宁可让本次推送失败并给出可读原因，
     * 也不在无法确认既有回滚点的情况下继续动盘）。任一步 IO 失败抛 [IOException]——此时尚未触碰任何原文件。
     */
    fun backup(
        orderId: Long,
        ops: List<DeliveryFileOp>,
    ): Boolean {
        if (ops.isEmpty()) return false // 无工作集，无需备份
        val orderDir = File(backupRoot, orderId.toString())
        val filesDir = File(orderDir, FILES_SUBDIR)
        // 既有 manifest 存在但无法解析 → fail-closed 抛错（无法确认既有回滚点时不继续动盘）。
        val manifestFile = File(orderDir, MANIFEST_NAME)
        val entries = if (manifestFile.exists()) decodeManifest(manifestFile).toMutableList() else mutableListOf()
        val recorded = entries.mapTo(HashSet()) { it["path"] as? String ?: "" }
        var added = 0
        for (op in ops) {
            if (!recorded.add(op.path)) continue // 本单已备过该路径：旧内容已在盘，绝不重备
            entries.add(backupOne(op, filesDir))
            added++
        }
        if (added > 0) {
            AtomicFileWriter.write(File(orderDir, MANIFEST_NAME), codec.encode(entries).toByteArray(StandardCharsets.UTF_8))
        }
        adapter.info("交付备份完成：orderId=$orderId，条目=${entries.size}，新增=$added，位置=${orderDir.absolutePath}")
        return true
    }

    /** 备份单文件：add 只记标记；update / delete 复制旧内容并记旧哈希 / 大小。返回该项 manifest 条目。 */
    private fun backupOne(
        op: DeliveryFileOp,
        filesDir: File,
    ): Map<String, Any?> {
        if (op.kind == DeliveryFileOp.Kind.ADD) {
            // add 项目标覆盖前不存在，无旧内容可备；仅记标记，回滚时删除该新增文件。
            return manifestEntry(op.path, DeliveryManifestFile.ACTION_ADD, "", 0L)
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
        return manifestEntry(op.path, action, oldSha, backupFile.length())
    }

    /** 组装一条备份 manifest 条目（键集与 M5 回滚读取口径一致）。 */
    private fun manifestEntry(
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

    /**
     * 还原单条：add 项删目标文件（还原为不存在）；update / delete 项先校验再复制回目标。
     *
     * **校验按条目类型分流（FR-267 真机验收 O4 修正）**：
     * - **目录条目**（备份区里该项是目录）→ 只校验存在性与类型一致（必须仍是目录），**不比对 sha256 / size**。
     *   原因：备份目录时 `Files.copy` 只建空目录（不递归内容），`DeliverySha256.ofFile` 读目录必然失败，
     *   manifest 里该条目的 `sha256` 天生为空、`size` 也不可信（这是**既有形态**，非损坏）。
     *   若对目录条目也做严格比对，则任何含目录的交付单回滚会被整单拒绝——安全但功能退化。
     * - **文件条目** → 保持严格校验：`sha256` / `size` 缺失或与磁盘不符即抛 [IOException]。
     *
     * 校验先于复制：文件备份损坏 / 被截断时抛 [IOException]，**绝不把损坏内容写回目标**——
     * 逐条回滚的语义是「要么忠实还原，要么明确失败」，不做半真半假的写入。
     */
    private fun restoreOne(
        entry: Map<String, Any?>,
        filesDir: File,
    ) {
        val path = manifestField(entry, "path")
        val action = manifestField(entry, "action")
        val target = resolver.resolve(path) ?: throw IOException("回滚目标路径非法：$path")
        if (action == DeliveryManifestFile.ACTION_ADD) {
            // add 项还原 = 撤销新增：删除本单新增的文件 / 目录（本单没动过的路径不在此列）。
            removeThenEnsure(target, path, ensureDir = false, failurePrefix = "回滚删除新增文件失败")
            return
        }
        val backupFile = File(filesDir, path)
        if (!backupFile.exists()) {
            throw IOException("回滚备份内容缺失：$path")
        }
        // 目录条目（备份区里该项是目录 → 备份时原目标就是目录）：只校验目标当前仍是目录，不比对 sha256 / size——
        // 目录无法哈希，其 manifest 的 sha256 天生为空（既有形态，非损坏）；若在此做严格比对，
        // 任何含目录的交付单整单回滚都会被拒（安全但功能退化，真机验收 O4）。
        if (backupFile.isDirectory) {
            removeThenEnsure(target, path, ensureDir = true, failurePrefix = "回滚还原目录失败")
            return
        }
        verifyBackupContent(backupFile, entry, path)
        target.parentFile?.mkdirs()
        Files.copy(backupFile.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
    }

    /** 读 manifest 条目的必填字符串字段；缺失 / 类型不符即抛 [IOException]（结构非法，回滚失败并给可读原因）。 */
    private fun manifestField(
        entry: Map<String, Any?>,
        key: String,
    ): String = entry[key] as? String ?: throw IOException("回滚备份条目缺 $key")

    /** 校验备份内容与 manifest 记录的 sha256 / size 一致（FR-267）；缺失 / 不符即抛 [IOException]（脱敏原因含路径与两侧长度）。 */
    @Suppress("ThrowsCount") // 三项校验（sha 缺失 / size 缺失 / 大小不符 / 哈希不符）各自上抛可读原因，收敛为单一异常类型
    private fun verifyBackupContent(
        backupFile: File,
        entry: Map<String, Any?>,
        path: String,
    ) {
        val expectedSha = entry["sha256"] as? String
        if (expectedSha.isNullOrEmpty()) {
            throw IOException("回滚备份条目缺 sha256，拒绝还原：$path")
        }
        val expectedSize =
            (entry["size"] as? Number)?.toLong()
                ?: throw IOException("回滚备份条目缺 size，拒绝还原：$path")
        val actualSize = backupFile.length()
        if (actualSize != expectedSize) {
            throw IOException("回滚备份内容大小不符（manifest=$expectedSize，实际=$actualSize）：$path")
        }
        if (DeliverySha256.ofFile(backupFile) != expectedSha) {
            throw IOException("回滚备份内容哈希不符（备份已损坏，拒绝写回目标）：$path")
        }
    }

    /**
     * 保留清理（spec §4.7.1）：删除超 [RETENTION_DAYS] 天的备份单，再把超 [MAX_BACKUPS] 个的最旧删除。
     *
     * 本地周期执行——由交付执行器在**每次推送**时机会式调用（不论本次是否生成新备份，FR-267：闲置服也要修剪），
     * 无需另起调度循环。
     */
    fun enforceRetention() {
        val cutoff = clock.millis() - RETENTION_DAYS * MILLIS_PER_DAY
        // 备份区下的各单备份目录（不存在 / 非目录即空表），按最近修改时间倒序（最新在前）。
        val dirs = backupRoot.listFiles()?.filter { it.isDirectory }.orEmpty().sortedByDescending { it.lastModified() }
        val (expired, fresh) = dirs.partition { it.lastModified() < cutoff }
        for (dir in expired + fresh.drop(MAX_BACKUPS)) deleteQuietly(dir)
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

/**
 * 按目标类型处置目标位置：**先清掉现存项，再按需重建目录**。
 *
 * - `ensureDir = true`（还原目录条目）：目录无法参与哈希校验，故只做存在性与类型处置。
 *   交付可能已把该目录替换成同名**文件**（合法覆盖形态），须先清掉占位文件再建目录，
 *   否则 `mkdirs()` 恒失败、含目录的交付单整单回滚会被挡（真机验收 O4）；
 * - `ensureDir = false`（还原 add 项）：仅撤销新增，把目标删回不存在。
 */
private fun removeThenEnsure(
    target: File,
    path: String,
    ensureDir: Boolean,
    failurePrefix: String,
) {
    if (ensureDir && target.isDirectory) {
        return
    }
    if (target.exists() && !target.deleteRecursively()) {
        throw IOException("$failurePrefix：$path")
    }
    if (ensureDir && !target.mkdirs()) {
        throw IOException("$failurePrefix：$path")
    }
}

package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.nio.charset.StandardCharsets
import java.security.MessageDigest

/**
 * 三方插件文件覆盖兼容的应用编排（ADR-0011 决策 2/4/5/6/7）。
 *
 * 单个文件的处理序：
 * 1. **Path 级安全校验**（[OverridePathSecurity]）：非法（穿越 / 绝对 / 盘符 / jar / server 关键文件等）→ 跳过 + 告警，不逃逸目标根、不阻断其余；
 * 2. **反馈环防护**（[ManagedFileTracker]）：磁盘现状被外部改过（与 agent 上次写入的 md5 不同）→ 告警而非盲盖，跳过该文件；
 * 3. **覆盖前备份**（[BackupManager]）：记原本是否存在；
 * 4. **原子覆盖**（复用 [FileMirrorWriter] 临时文件→fsync→ATOMIC_MOVE）；
 * 5. **受管标记**：记下本次写入的 md5 作下一轮反馈环基准。
 *
 * 全部成功覆盖后，若有重载命令则经 [ReloadCommandExecutor]（本地白名单 + 异步派发，不在主线程等结果）派发。
 *
 * **回滚只还原文件 + 不重放命令**（[rollback] 只调 BackupManager.restore，绝不触碰 reloadExecutor，见决策 5）。
 *
 * 落盘 / 校验全在异步线程由调用方驱动（lifecycle），本类不阻塞主线程。
 */
class OverrideApplier(
    targetRoot: File,
    private val backupManager: BackupManager,
    private val tracker: ManagedFileTracker,
    private val pathSecurity: OverridePathSecurity,
    private val reloadExecutor: ReloadCommandExecutor,
    private val adapter: PlatformAdapter,
) {

    private val root: File = targetRoot
    private val mirrorWriter = FileMirrorWriter(targetRoot)

    /**
     * 应用一组覆盖文件并（命中白名单时）派发重载命令。
     *
     * @return true 表示所有应覆盖的文件均已落盘（含因外部改动 / 非法路径跳过的视为"未全量成功"返回 false）。
     */
    fun apply(setId: String, files: List<OverrideFile>, reloadCommand: String?): Boolean {
        // 不持有备份记录的入口：覆盖 + 派发，返回是否全量成功（需回滚的调用方改用 applyAndReturnBackups）。
        val (_, allWritten) = doApply(setId, files)
        if (allWritten && !reloadCommand.isNullOrBlank()) {
            // 仅在全部覆盖成功后才派发重载命令（部分失败不派发，避免对半成品 reload）。
            reloadExecutor.execute(reloadCommand)
        }
        return allWritten
    }

    /**
     * 同 [apply] 但返回备份记录（供调用方在外部失败时回滚）。reloadCommand 非空且全量成功才派发。
     */
    fun applyAndReturnBackups(setId: String, files: List<OverrideFile>, reloadCommand: String?): List<BackupRecord> {
        val (records, allWritten) = doApply(setId, files)
        if (allWritten && !reloadCommand.isNullOrBlank()) {
            reloadExecutor.execute(reloadCommand)
        }
        return records
    }

    /**
     * 回滚一组备份记录：原本存在→还原旧内容，原本不存在→删除。**绝不重放重载命令**（决策 5）。
     */
    fun rollback(records: List<BackupRecord>) {
        for (record in records) {
            val target = File(root, record.relPath)
            try {
                backupManager.restore(record, target)
                tracker.forget(record.relPath)
            } catch (e: Exception) {
                adapter.error("回滚还原文件失败（path=${record.relPath}），继续其余", e)
            }
        }
        adapter.info("覆盖已回滚：还原 ${records.size} 个文件（未重放任何重载命令）")
    }

    /** 执行覆盖主体，返回（备份记录, 是否全量成功）。 */
    private fun doApply(setId: String, files: List<OverrideFile>): Pair<List<BackupRecord>, Boolean> {
        val records = mutableListOf<BackupRecord>()
        var allWritten = true
        for (file in files) {
            val rel = file.path
            if (!pathSecurity.isSafe(rel)) {
                adapter.warn("跳过非法覆盖路径（穿越 / 绝对 / 盘符 / jar / server 关键文件），不落盘：$rel")
                allWritten = false
                continue
            }
            val target = File(root, rel)
            // 反馈环防护：检测外部改动则告警不盲盖。
            if (target.exists()) {
                val diskMd5 = md5Hex(target.readText(StandardCharsets.UTF_8))
                if (tracker.isExternallyModified(rel, diskMd5)) {
                    adapter.warn("检测到受管文件被外部改动（疑似插件自身重写），告警而非盲盖，跳过：$rel")
                    allWritten = false
                    continue
                }
            }
            // 备份 → 原子覆盖 → 受管标记。
            try {
                records.add(backupManager.backup(setId, rel, target))
                mirrorWriter.write(rel, file.content)
                tracker.markWritten(rel, file.md5)
            } catch (e: Exception) {
                adapter.error("覆盖文件失败（path=$rel），跳过", e)
                allWritten = false
            }
        }
        return records to allWritten
    }

    /** 按字节算 md5（与控制面一致基准），供反馈环比对磁盘现状。 */
    private fun md5Hex(content: String): String {
        val digest = MessageDigest.getInstance("MD5").digest(content.toByteArray(StandardCharsets.UTF_8))
        return digest.joinToString("") { "%02x".format(it) }
    }
}

/**
 * 一个待覆盖的文件（控制面按覆盖链解析后的整文件态）。
 *
 * @param path    相对目标根的 path
 * @param content 整文件内容
 * @param md5     内容 md5（按字节，与控制面同基准）
 */
data class OverrideFile(
    val path: String,
    val content: String,
    val md5: String,
)

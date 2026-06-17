package top.wcpe.beacon.agent.core.override

import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.StandardCopyOption

/**
 * 覆盖前备份与回滚还原（ADR-0011 决策 5）。
 *
 * 三方插件文件覆盖兼容会改 / 删插件目录里的真实文件。覆盖前先把被改 / 删的现有文件备份，
 * 并记录「原本是否存在」——这样回滚时能精确还原：原本存在 → 还原旧内容；原本不存在 → 删除（不留垃圾）。
 *
 * **回滚只还原文件，绝不重放重载命令**（命令本身可能就是失败根因，见 ADR-0011 决策 5）；重放禁令由
 * 调用方（[ReloadCommandExecutor] 不被回滚触碰）保证，本类只管文件层面的备份 / 还原。
 *
 * @param backupRoot 备份区根目录（落 agent 数据目录下，与镜像目标分离）
 */
class BackupManager(
    private val backupRoot: File,
) {

    /**
     * 在覆盖 [target] 前备份其当前状态。
     *
     * 文件存在 → 拷贝到备份区并记 existedBefore=true；不存在 → 仅记 existedBefore=false（无内容可备）。
     *
     * @param setId    覆盖集标识（备份按集隔离，避免不同集互相覆盖备份）
     * @param relPath  相对目标根的 path（决定备份区内的落点）
     * @return 备份记录，供回滚使用
     */
    fun backup(setId: String, relPath: String, target: File): BackupRecord {
        if (!target.exists()) {
            return BackupRecord(setId, relPath, existedBefore = false, backupFile = null)
        }
        val backupFile = File(File(backupRoot, sanitize(setId)), relPath)
        backupFile.parentFile?.mkdirs()
        Files.copy(target.toPath(), backupFile.toPath(), StandardCopyOption.REPLACE_EXISTING)
        return BackupRecord(setId, relPath, existedBefore = true, backupFile = backupFile)
    }

    /**
     * 按备份记录回滚 [target]：原本存在 → 还原备份内容；原本不存在 → 删除（幂等）。
     *
     * 失败抛 [IOException] 由上层记录并告警；回滚不触发任何重载命令。
     */
    fun restore(record: BackupRecord, target: File) {
        if (record.existedBefore) {
            val backupFile = record.backupFile
                ?: throw IOException("备份记录标记原本存在却无备份文件：${record.relPath}")
            target.parentFile?.mkdirs()
            Files.copy(backupFile.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
        } else {
            Files.deleteIfExists(target.toPath())
        }
    }

    /** 把 setId 归一化为可作目录名的安全串（去掉路径分隔与冒号等）。 */
    private fun sanitize(setId: String): String =
        setId.replace(Regex("[^A-Za-z0-9_.-]"), "_").ifEmpty { "_" }
}

/**
 * 单个文件的备份记录。
 *
 * @param setId        所属覆盖集
 * @param relPath      相对目标根的 path
 * @param existedBefore 覆盖前该真实文件是否已存在（回滚据此决定还原还是删除）
 * @param backupFile   备份落点（existedBefore=false 时为 null）
 */
data class BackupRecord(
    val setId: String,
    val relPath: String,
    val existedBefore: Boolean,
    val backupFile: File?,
) {
    /** 读备份内容（仅 existedBefore=true 时有意义；测试与校验用）。 */
    fun readBackupContent(): String? =
        backupFile?.takeIf { it.exists() }?.readText(StandardCharsets.UTF_8)
}

package top.wcpe.beacon.agent.core.filesync

import top.wcpe.beacon.agent.core.filetree.AtomicFileWriter
import java.io.File
import java.io.IOException
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.FileVisitResult
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.Path
import java.nio.file.SimpleFileVisitor
import java.nio.file.StandardCopyOption
import java.nio.file.attribute.BasicFileAttributes
import java.time.Clock
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

/**
 * 文件同步覆盖前目录备份与回滚骨架。
 *
 * 备份采用目录 rename：把目标相对目录移动到备份区的时间戳目录。回滚只还原文件目录，
 * 不执行任何重载命令，也不接入完整编排状态机。
 */
class FileSyncBackupManager(
    private val serverRoot: File,
    private val backupRoot: File,
    private val clock: Clock = Clock.systemUTC(),
) {
    private val guard = FileSyncPathGuard(serverRoot)

    /** 覆盖前备份目标目录；目标不存在时返回可幂等回滚的空备份点。 */
    fun backupDirectory(relativeDir: String): FileSyncBackupPoint {
        val target =
            guard.resolveDirectory(relativeDir)?.toFile()
                ?: throw IllegalArgumentException("非法同步目录：$relativeDir")
        if (!target.exists()) return FileSyncBackupPoint(relativeDir, existedBefore = false, backupPath = null)
        require(target.isDirectory) { "同步目标不是目录：$relativeDir" }
        val backupDir = nextBackupDir(relativeDir)
        backupDir.parentFile?.mkdirs()
        moveDirectory(target, backupDir)
        AtomicFileWriter.fsyncDir(target.parentFile)
        AtomicFileWriter.fsyncDir(backupDir.parentFile)
        return FileSyncBackupPoint(relativeDir, existedBefore = true, backupPath = backupDir)
    }

    /** 回滚到备份点；备份点不存在返回 false，原目录本来不存在时为空操作。 */
    fun rollback(point: FileSyncBackupPoint): Boolean {
        if (!point.existedBefore) return true
        val backupDir = point.backupPath ?: return false
        if (!backupDir.isDirectory) return false
        val target =
            guard.resolveDirectory(point.relativeDir)?.toFile()
                ?: throw IllegalArgumentException("非法同步目录：${point.relativeDir}")
        deleteCurrentTarget(target)
        target.parentFile?.mkdirs()
        moveDirectory(backupDir, target)
        AtomicFileWriter.fsyncDir(target.parentFile)
        return true
    }

    private fun nextBackupDir(relativeDir: String): File {
        val baseName = sanitize(relativeDir) + "-" + TIMESTAMP.format(clock.instant())
        var candidate = File(backupRoot, baseName)
        var index = 1
        while (candidate.exists()) {
            candidate = File(backupRoot, "$baseName-$index")
            index++
        }
        return candidate
    }

    private fun deleteCurrentTarget(target: File) {
        val path = target.toPath()
        if (!Files.exists(path, LinkOption.NOFOLLOW_LINKS)) return
        deleteTreeWithoutFollowingLinks(path)
    }

    private fun deleteTreeWithoutFollowingLinks(path: Path) {
        Files.walkFileTree(
            path,
            object : SimpleFileVisitor<Path>() {
                override fun visitFile(
                    file: Path,
                    attrs: BasicFileAttributes,
                ): FileVisitResult {
                    Files.delete(file)
                    return FileVisitResult.CONTINUE
                }

                override fun postVisitDirectory(
                    dir: Path,
                    exc: IOException?,
                ): FileVisitResult {
                    if (exc != null) throw exc
                    Files.delete(dir)
                    return FileVisitResult.CONTINUE
                }
            },
        )
    }

    private fun moveDirectory(
        source: File,
        target: File,
    ) {
        try {
            Files.move(source.toPath(), target.toPath(), StandardCopyOption.ATOMIC_MOVE)
        } catch (e: AtomicMoveNotSupportedException) {
            Files.move(source.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
        }
    }

    private fun sanitize(relativeDir: String): String =
        relativeDir.replace(Regex("[^A-Za-z0-9_.-]"), "_").ifEmpty { "_" }

    companion object {
        private val TIMESTAMP: DateTimeFormatter =
            DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss").withZone(ZoneOffset.UTC)
    }
}

/** 文件同步目录备份点。 */
data class FileSyncBackupPoint(
    val relativeDir: String,
    val existedBefore: Boolean,
    val backupPath: File?,
)

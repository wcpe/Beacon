package top.wcpe.beacon.agent.core.filetree

import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.nio.file.StandardOpenOption

/**
 * 文件镜像落盘器：把有效文件树镜像到目标根目录（相对 path）。
 *
 * 原子写：临时文件 → `FileChannel.force`（含父目录 fsync）→ `ATOMIC_MOVE` 覆盖，
 * 保证崩溃恢复时「先文件后清单」的持久化序可靠（补 SnapshotStore 未做 fsync 的缺口，见 ADR-0010 决策4）。
 *
 * 落盘路径只允许落在 [root] 内，相对 path 经 [RelativePathGuard] 校验，拒绝 `..` / 绝对路径 / 反斜杠穿越。
 * 纯 java.nio 实现（非 Bukkit API），可在 core 使用。
 *
 * @param root 镜像目标根（壳层传入插件 plugins 基目录）
 */
class FileMirrorWriter(
    private val root: File,
) {

    /**
     * 原子写一个文件到 root/<relativePath>。
     *
     * 路径非法抛 [IllegalArgumentException]；IO 失败抛 [IOException]（由上层记录、保 fail-static 不删既有）。
     */
    fun write(relativePath: String, content: String) {
        require(RelativePathGuard.isSafe(relativePath)) { "非法落盘路径（绝对/穿越/反斜杠）：$relativePath" }
        val target = resolve(relativePath)
        val parent = target.parentFile
        if (parent != null && !parent.exists() && !parent.mkdirs() && !parent.exists()) {
            throw IOException("创建父目录失败：${parent.absolutePath}")
        }

        // 1) 写临时文件并 force（含数据落盘）。
        val tmp = File(parent, target.name + TMP_SUFFIX)
        val bytes = content.toByteArray(StandardCharsets.UTF_8)
        Files.newByteChannel(
            tmp.toPath(),
            StandardOpenOption.CREATE,
            StandardOpenOption.WRITE,
            StandardOpenOption.TRUNCATE_EXISTING,
        ).use { channel ->
            val buffer = java.nio.ByteBuffer.wrap(bytes)
            while (buffer.hasRemaining()) {
                channel.write(buffer)
            }
            if (channel is java.nio.channels.FileChannel) {
                channel.force(true) // 元数据 + 数据一并刷盘
            }
        }

        // 2) 原子重命名覆盖目标。
        Files.move(
            tmp.toPath(),
            target.toPath(),
            StandardCopyOption.REPLACE_EXISTING,
            StandardCopyOption.ATOMIC_MOVE,
        )

        // 3) 父目录 fsync：让重命名这条目录项变更也落盘，保证崩溃后能看到新文件。
        fsyncDir(parent?.toPath())
    }

    /**
     * 删除 root/<relativePath> 的本地镜像（高层删 path 或整体下线时）。
     *
     * 文件不存在视为已删（幂等）；删后 fsync 父目录。路径非法抛异常。
     */
    fun delete(relativePath: String) {
        require(RelativePathGuard.isSafe(relativePath)) { "非法落盘路径（绝对/穿越/反斜杠）：$relativePath" }
        val target = resolve(relativePath)
        val parent = target.parentFile
        val deleted = Files.deleteIfExists(target.toPath())
        if (deleted) {
            fsyncDir(parent?.toPath())
        }
    }

    /** 解析相对路径到 root 下的绝对 File（路径已校验安全）。 */
    private fun resolve(relativePath: String): File = File(root, relativePath)

    /** 尽力 fsync 目录项；目录不支持 fsync 的平台（如部分 Windows）忽略异常。 */
    private fun fsyncDir(dir: Path?) {
        if (dir == null) return
        try {
            Files.newByteChannel(dir, StandardOpenOption.READ).use { channel ->
                if (channel is java.nio.channels.FileChannel) {
                    channel.force(true)
                }
            }
        } catch (e: IOException) {
            // 目录 fsync 在部分平台（Windows / 某些文件系统）不被支持，忽略——文件本身已 force。
        }
    }

    companion object {
        private const val TMP_SUFFIX = ".beacon-tmp"
    }
}

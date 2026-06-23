package top.wcpe.beacon.agent.core.filetree

import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.nio.file.Files

/**
 * 文件镜像落盘器：把有效文件树镜像到目标根目录（相对 path）。
 *
 * 原子写委托 [AtomicFileWriter]：唯一临时文件 → `FileChannel.force`（含父目录 fsync）→ 重命名覆盖，
 * 保证崩溃恢复时「先文件后清单」的持久化序可靠（见 ADR-0010 决策4），并消除 Windows 并发落盘竞争。
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
        AtomicFileWriter.write(resolve(relativePath), content.toByteArray(StandardCharsets.UTF_8))
    }

    /**
     * 删除 root/<relativePath> 的本地镜像（高层删 path 或整体下线时）。
     *
     * 文件不存在视为已删（幂等）；删后 fsync 父目录。路径非法抛异常。
     */
    fun delete(relativePath: String) {
        require(RelativePathGuard.isSafe(relativePath)) { "非法落盘路径（绝对/穿越/反斜杠）：$relativePath" }
        val target = resolve(relativePath)
        val deleted = Files.deleteIfExists(target.toPath())
        if (deleted) {
            AtomicFileWriter.fsyncDir(target.parentFile)
        }
    }

    /** 解析相对路径到 root 下的绝对 File（路径已校验安全）。 */
    private fun resolve(relativePath: String): File = File(root, relativePath)
}

package top.wcpe.beacon.agent.core.filetree

import java.io.File
import java.io.IOException
import java.nio.ByteBuffer
import java.nio.channels.FileChannel
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.FileSystemException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.nio.file.StandardOpenOption
import java.util.UUID
import java.util.concurrent.ThreadLocalRandom

/**
 * 跨平台原子落盘原语：写唯一临时文件 → `force`（数据落盘）→ 重命名覆盖 → 尽力 fsync 父目录。
 *
 * 各通道落盘器（文件树镜像 / 已落盘清单 / 配置快照）共用此原语，避免重复实现并消除 Windows 真机偶发缺陷：
 * - **唯一临时文件名**（线程 id + 纳秒）：杜绝并发两次落盘抢同一 tmp——一方 `move` 走后另一方 `move`
 *   找不到源而抛 `NoSuchFileException`（本次 E2E 在 Windows 上暴露的根因）。
 * - **原子移动回退**：优先 `ATOMIC_MOVE`+`REPLACE_EXISTING`；平台 / 文件系统不支持原子移动
 *   （`AtomicMoveNotSupportedException`）时回退为非原子 `REPLACE_EXISTING` 覆盖（仍先写 tmp，崩溃窗口极小）。
 * - **瞬时占用 / 并发覆盖抖动重试**：杀软 / 索引器短暂持有目标，或多线程并发覆盖同一目标时 Windows
 *   `MoveFileEx(REPLACE_EXISTING)` 触发共享冲突（`AccessDeniedException`，属 `FileSystemException`），
 *   以**抖动退避**有限重试——抖动打散并发线程的重试对齐，避免反复撞同一目标锁窗口；仍失败才抛给上层
 *   （由上层 fail-static 处理、不抛未捕获异常到调度器）。
 * - **失败清理 tmp**：异常时尽力删除临时文件，绝不残留半截 tmp。
 *
 * 纯 java.nio 实现、无可变全局状态（[object] 仅承载无副作用静态逻辑），可在 core 复用。
 */
object AtomicFileWriter {

    /** 重命名最大尝试次数（含首次），覆盖 Windows 杀软 / 索引器占用与多线程并发覆盖同一目标的争用窗口。 */
    private const val MAX_MOVE_ATTEMPTS = 10

    /** 重试退避基数毫秒：实际退避 = 基数×尝试次数 + [0,基数×尝试次数] 随机抖动（打散并发重试对齐）。 */
    private const val RETRY_BACKOFF_MS = 15L

    /** 临时文件名中缀（便于运维识别与清理）。 */
    private const val TMP_INFIX = ".beacon-tmp."

    /**
     * 原子写 bytes 到 [target]。父目录不存在则创建。
     *
     * 重试耗尽仍失败抛 [IOException]，由上层（fail-static）记录并保留既有文件、下轮重试。
     */
    fun write(target: File, bytes: ByteArray) {
        val parent = target.parentFile
        if (parent != null && !parent.exists() && !parent.mkdirs() && !parent.exists()) {
            throw IOException("创建父目录失败：${parent.absolutePath}")
        }
        // 唯一 tmp：随机 UUID 后缀，彻底消除并发落盘抢同一临时文件的竞争（无全局可变状态）。
        val tmp = File(parent, target.name + TMP_INFIX + UUID.randomUUID())
        try {
            writeAndForce(tmp, bytes)
            moveWithRetry(tmp.toPath(), target.toPath())
        } finally {
            // 成功后 tmp 已被 move 消费；失败则清理残留，绝不留半截 tmp。
            if (tmp.exists()) {
                runCatching { Files.deleteIfExists(tmp.toPath()) }
            }
        }
        fsyncDir(parent)
    }

    /**
     * 尽力 fsync 目录项，使重命名 / 删除这条目录项变更落盘（崩溃后能看到新文件）。
     *
     * 目录不支持 fsync 的平台（Windows / 某些文件系统）忽略——文件本身已 force。
     */
    fun fsyncDir(dir: File?) {
        val path = dir?.toPath() ?: return
        try {
            Files.newByteChannel(path, StandardOpenOption.READ).use { channel ->
                if (channel is FileChannel) {
                    channel.force(true)
                }
            }
        } catch (e: IOException) {
            // 目录 fsync 在部分平台不被支持，忽略。
        }
    }

    /** 写临时文件并 force（数据 + 元数据一并刷盘）。 */
    private fun writeAndForce(tmp: File, bytes: ByteArray) {
        Files.newByteChannel(
            tmp.toPath(),
            StandardOpenOption.CREATE,
            StandardOpenOption.WRITE,
            StandardOpenOption.TRUNCATE_EXISTING,
        ).use { channel ->
            val buffer = ByteBuffer.wrap(bytes)
            while (buffer.hasRemaining()) {
                channel.write(buffer)
            }
            if (channel is FileChannel) {
                channel.force(true)
            }
        }
    }

    /** 重命名覆盖目标：原子移动优先、瞬时占用有限重试；重试耗尽抛出。 */
    private fun moveWithRetry(source: Path, target: Path) {
        var attempt = 0
        while (true) {
            attempt++
            try {
                moveOnce(source, target)
                return
            } catch (e: FileSystemException) {
                // 瞬时占用（杀软 / 索引器持有目标）或并发覆盖共享冲突（AccessDeniedException）：有限重试，仍失败则抛给上层。
                if (attempt >= MAX_MOVE_ATTEMPTS) throw e
                // 抖动退避：线性退避叠加随机抖动，打散并发线程的重试对齐，避免反复在同一瞬间撞目标锁窗口。
                val backoff = RETRY_BACKOFF_MS * attempt
                sleepQuietly(backoff + ThreadLocalRandom.current().nextLong(backoff + 1))
            }
        }
    }

    /** 单次移动：先试 `ATOMIC_MOVE`，平台不支持原子移动时回退非原子 `REPLACE_EXISTING`。 */
    private fun moveOnce(source: Path, target: Path) {
        try {
            Files.move(source, target, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
        } catch (e: AtomicMoveNotSupportedException) {
            // 平台 / 文件系统不支持原子移动：退化为非原子覆盖。
            Files.move(source, target, StandardCopyOption.REPLACE_EXISTING)
        }
    }

    /** 静默睡眠：被中断时复位中断标志并立即返回。 */
    private fun sleepQuietly(ms: Long) {
        try {
            Thread.sleep(ms)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }
}

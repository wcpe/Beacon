package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.filetree.AtomicFileWriter
import java.io.File
import java.io.IOException

/**
 * 交付覆盖器（FR-165，spec §4.2.3 / §4.5.3）：执行期本地重判 + 从临时目录原子入位。
 *
 * 两步职责：
 * 1. [plan]：逐文件本地 sha256 比对得工作集——相同则跳过（计 skipped）；缺失→ADD；不同→UPDATE；
 *    delete 项目标存在→DELETE、不存在→SKIP。路径经 [DeliveryTargetResolver] 校验（含自我保护），非法即抛。
 * 2. [apply]：把已下载校验齐全的临时文件流式移入目标（复用 [AtomicFileWriter.moveInto] 重命名内核，
 *    不整读入内存），delete 项目标存在才删。
 *
 * **务必在备份成功后才调 [apply]**（备份失败绝不覆盖，由调用方保证时序）。
 *
 * @param resolver 落盘目标解析与安全校验
 */
class DeliveryOverwriter(
    private val resolver: DeliveryTargetResolver,
) {
    /**
     * 逐文件本地重判得操作计划（spec §4.2.3）。任一路径非法抛 [DeliveryPathException]（整单失败，不静默丢文件）。
     */
    fun plan(files: List<DeliveryManifestFile>): List<DeliveryFileOp> = files.map { classify(it) }

    /** 读取当前目标文件 sha256；文件不存在返回 null，存在但读取失败则抛异常。 */
    fun currentSha256(path: String): String? {
        val target = resolver.resolve(path) ?: throw DeliveryPathException(path)
        if (!target.exists()) return null
        return DeliverySha256.ofFile(target) ?: throw IOException("交付目标文件摘要读取失败：$path")
    }

    /** 单文件重判：delete 项按目标存在性、非 delete 项按本地哈希异同分类。 */
    private fun classify(file: DeliveryManifestFile): DeliveryFileOp {
        val target = resolver.resolve(file.path) ?: throw DeliveryPathException(file.path)
        return when {
            // delete 项：目标本地已不存在则跳过（执行期最终一致，不误删）。
            isDeleteItem(file) -> op(file, if (target.exists()) DeliveryFileOp.Kind.DELETE else DeliveryFileOp.Kind.SKIP)
            // 本地缺失→新增。
            !target.exists() -> op(file, DeliveryFileOp.Kind.ADD)
            // 本地同 hash 跳过，只覆盖变更文件（spec §4.2.3，验收 #6）；否则覆盖。
            DeliverySha256.ofFile(target) == file.sha256 -> op(file, DeliveryFileOp.Kind.SKIP)
            else -> op(file, DeliveryFileOp.Kind.UPDATE)
        }
    }

    /** 是否 delete 项（显式 action=delete 或无内容哈希）。 */
    private fun isDeleteItem(file: DeliveryManifestFile): Boolean =
        file.action == DeliveryManifestFile.ACTION_DELETE || file.sha256.isEmpty()

    /**
     * 应用操作计划：ADD / UPDATE 从临时目录原子移入目标，DELETE 删除目标。返回实际变更文件数（不含 SKIP）。
     *
     * 任一步 IO 失败抛 [IOException]（此时备份已在盘，调用方判目标 failed，可整单回滚）。
     */
    fun apply(
        ops: List<DeliveryFileOp>,
        tempDir: File,
    ): Int {
        var changed = 0
        for (op in ops) {
            when (op.kind) {
                DeliveryFileOp.Kind.ADD, DeliveryFileOp.Kind.UPDATE -> {
                    moveIntoPlace(op, tempDir)
                    changed++
                }

                DeliveryFileOp.Kind.DELETE -> {
                    deleteTarget(op)
                    changed++
                }

                DeliveryFileOp.Kind.SKIP -> Unit // 本地已一致，不动
            }
        }
        return changed
    }

    /** 从临时目录把已校验文件原子移入目标位置。 */
    private fun moveIntoPlace(
        op: DeliveryFileOp,
        tempDir: File,
    ) {
        val target = resolver.resolve(op.path) ?: throw DeliveryPathException(op.path)
        val tempFile = File(tempDir, op.path)
        if (!tempFile.exists()) throw IOException("交付临时文件缺失，无法入位：${op.path}")
        AtomicFileWriter.moveInto(tempFile, target)
    }

    /** 删除 delete 项目标（存在才删），并 fsync 父目录持久化该删除。 */
    private fun deleteTarget(op: DeliveryFileOp) {
        val target = resolver.resolve(op.path) ?: throw DeliveryPathException(op.path)
        if (!target.exists()) return
        if (!target.delete() && target.exists()) throw IOException("交付删除目标失败：${op.path}")
        AtomicFileWriter.fsyncDir(target.parentFile)
    }

    private fun op(
        file: DeliveryManifestFile,
        kind: DeliveryFileOp.Kind,
    ): DeliveryFileOp = DeliveryFileOp(file.path, kind, file.sha256, file.sizeBytes)
}

/** 交付目标路径非法（path traversal / 逃逸 / 落在 agent 自身目录）；属 IO 边界异常，由覆盖 / 备份路径抛出。 */
class DeliveryPathException(
    path: String,
) : IOException("交付目标路径非法：$path")

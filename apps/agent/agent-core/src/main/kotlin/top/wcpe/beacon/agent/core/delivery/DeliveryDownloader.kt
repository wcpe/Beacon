package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.BlobStreamTransport
import java.io.File
import java.io.FileOutputStream
import java.io.IOException

/**
 * 交付目标流式下载器（FR-165，spec §4.5.3）。
 *
 * 按工作集逐文件 `GET blobs/{sha256}` 下载到 agent 侧临时目录，支持 `Range` 断点续传；下载完成本地校验 sha256，
 * 不符删除重下；单文件重试上限 [MAX_ATTEMPTS]，耗尽即整体失败（原因含文件路径与失败环节）。
 *
 * **先全部下载到临时目录、校验齐全后才由 [DeliveryOverwriter] 备份 + 覆盖**——把「传输失败」与「覆盖失败」隔离，
 * 覆盖阶段不再依赖网络。全程流式写盘、绝不整读入内存；调用方保证在 async 线程使用。
 *
 * @param transport   blob 流式传输端口（注入假实现可测）
 * @param blobUrl     由 sha256 构造完整 blob URL
 * @param authHeaders 取当前鉴权头（X-Beacon-Token / Identity / Boot）
 * @param adapter     平台日志
 */
class DeliveryDownloader(
    private val transport: BlobStreamTransport,
    private val blobUrl: (String) -> String,
    private val authHeaders: () -> Map<String, String>,
    private val adapter: PlatformAdapter,
) {
    /**
     * 逐文件下载工作集到 [tempDir]（保留原相对路径），任一文件重试耗尽即失败返回。
     *
     * @param work    需下载的操作（ADD / UPDATE；DELETE / SKIP 不在列）
     * @param tempDir 本单临时目录（`dataFolder()/delivery-tmp/<orderId>`）
     */
    fun downloadAll(
        work: List<DeliveryFileOp>,
        tempDir: File,
    ): DeliveryDownloadResult {
        for (op in work) {
            if (!downloadOne(op, tempDir)) {
                return DeliveryDownloadResult(false, "流式下载 / 校验失败：路径=${op.path}")
            }
        }
        return DeliveryDownloadResult(true, "")
    }

    /** 下载单文件：续传命中即跳过，否则重试下载 + 校验（Range 续传 / 忽略 Range 兜底 / 校验不符重下）。 */
    private fun downloadOne(
        op: DeliveryFileOp,
        tempDir: File,
    ): Boolean {
        val tempFile = File(tempDir, op.path)
        tempFile.parentFile?.mkdirs()
        // 续传命中：上轮（或上次命令）已下完并校验通过则直接复用，免重下（卫语句）。
        if (verified(tempFile, op)) return true
        var attempt = 0
        var done = false
        while (!done && attempt < MAX_ATTEMPTS) {
            attempt++
            done = attemptDownload(op, tempFile)
        }
        if (!done) adapter.warn("交付下载重试耗尽：路径=${op.path}，sha=${op.sha256}，尝试=$MAX_ATTEMPTS")
        return done
    }

    /** 一次下载尝试：返回 true 表示已下完并校验通过；false 表示需继续重试（部分文件按需保留供续传）。 */
    private fun attemptDownload(
        op: DeliveryFileOp,
        tempFile: File,
    ): Boolean {
        if (tempFile.length() > op.sizeBytes) tempFile.delete() // 超长损坏，弃重下
        val rangeStart = if (tempFile.exists()) tempFile.length() else 0L
        return try {
            val outcome =
                FileOutputStream(tempFile, rangeStart > 0).use { sink ->
                    transport.download(blobUrl(op.sha256), authHeaders(), rangeStart, sink)
                }
            when {
                // 服务端忽略 Range 返整体：整体已被追加到部分文件之后 → 弃并从头重下。
                rangeStart > 0 && outcome.ignoredRange() -> {
                    tempFile.delete()
                    false
                }
                // 收到可用响应体（整体 200 / 区间 206）：完整则校验，未完整保留续传，校验不符删重下。
                outcome.hasBody() -> verifyOrDiscard(op, tempFile)
                // 4xx / 5xx：删部分、下一轮从头重下。
                else -> {
                    tempFile.delete()
                    false
                }
            }
        } catch (e: IOException) {
            // 网络中断：保留已下部分供下一轮 Range 续传（不删）。
            adapter.warn("交付下载中断，将续传：路径=${op.path}，已下=${tempFile.length()}/${op.sizeBytes}，原因=${e.javaClass.simpleName}")
            false
        }
    }

    /** 下载后判定：完整且哈希匹配→成功；哈希不符→删重下；未达预期大小→保留续传。 */
    private fun verifyOrDiscard(
        op: DeliveryFileOp,
        tempFile: File,
    ): Boolean {
        if (tempFile.length() != op.sizeBytes) return false // 区间未下满，保留供续传
        if (DeliverySha256.ofFile(tempFile) == op.sha256) return true
        tempFile.delete() // 校验不符，删除重下
        return false
    }

    /** 临时文件是否已下完且哈希匹配（续传 / 重跑命中判定）。 */
    private fun verified(
        tempFile: File,
        op: DeliveryFileOp,
    ): Boolean = tempFile.length() == op.sizeBytes && DeliverySha256.ofFile(tempFile) == op.sha256

    private companion object {
        /** 单文件下载重试上限（spec §4.5.3）。 */
        private const val MAX_ATTEMPTS = 3
    }
}

/**
 * 下载阶段结果。
 *
 * @param ok    全部工作集是否下载 + 校验通过
 * @param error 失败原因（含路径与环节，脱敏无凭据；成功为空串）
 */
data class DeliveryDownloadResult(
    val ok: Boolean,
    val error: String,
)

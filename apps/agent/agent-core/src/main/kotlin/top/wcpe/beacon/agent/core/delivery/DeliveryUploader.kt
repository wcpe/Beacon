package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.BlobStreamTransport
import java.io.File
import java.io.FileInputStream
import java.io.IOException

/**
 * 交付模板源流式上传器（FR-165，spec §4.5.2）。
 *
 * 逐文件先 `HEAD blobs/{sha256}` 去重（已就绪则跳过——sha256 寻址跨单 / 跨路径复用），未就绪再流式 `PUT`
 * （`Content-Length` 必填）；上传失败整文件重试上限 [MAX_ATTEMPTS]，耗尽即整体失败（原因含路径）。
 *
 * 全程流式读源文件、绝不整读入内存；调用方保证在 async 线程使用。
 *
 * @param transport   blob 流式传输端口
 * @param resolver    源文件路径解析与安全校验（服务器根内 + 自我保护）
 * @param blobUrl     由 sha256 构造完整 blob URL
 * @param authHeaders 取当前鉴权头
 * @param adapter     平台日志
 */
class DeliveryUploader(
    private val transport: BlobStreamTransport,
    private val resolver: DeliveryTargetResolver,
    private val blobUrl: (String) -> String,
    private val authHeaders: () -> Map<String, String>,
    private val adapter: PlatformAdapter,
) {
    /** 逐项上传待传清单；任一项失败即整体失败返回（含已上传 / 去重计数供回执与日志）。 */
    fun upload(items: List<DeliveryUploadItem>): DeliveryUploadResult {
        var uploaded = 0
        var deduped = 0
        for (item in items) {
            when (uploadOne(item)) {
                Outcome.DEDUPED -> deduped++
                Outcome.UPLOADED -> uploaded++
                Outcome.FAILED -> return DeliveryUploadResult(false, uploaded, deduped, "流式上传失败：路径=${item.path}")
            }
        }
        return DeliveryUploadResult(true, uploaded, deduped, "")
    }

    /** 上传单项：源文件缺失→失败；HEAD 就绪→去重跳过；否则 PUT 重试。 */
    private fun uploadOne(item: DeliveryUploadItem): Outcome {
        val file = resolver.resolve(item.path)
        if (file == null || !file.isFile) {
            adapter.warn("交付上传源文件不存在或路径非法：路径=${item.path}")
            return Outcome.FAILED // 卫语句：源缺失直接失败
        }
        return when {
            isReady(item) -> {
                adapter.info("交付上传去重跳过（blob 已就绪）：路径=${item.path}，sha=${item.sha256}")
                Outcome.DEDUPED
            }

            putWithRetry(item, file) -> Outcome.UPLOADED
            else -> Outcome.FAILED
        }
    }

    /** HEAD 去重判断：就绪返回 true（可跳过）；连接失败按未就绪继续上传（服务端按 sha 天然去重、无害）。 */
    private fun isReady(item: DeliveryUploadItem): Boolean =
        try {
            transport.head(blobUrl(item.sha256), authHeaders()).ready
        } catch (e: IOException) {
            adapter.warn("交付上传 HEAD 失败，按未就绪继续上传：路径=${item.path}，原因=${e.javaClass.simpleName}")
            false
        }

    /** 整文件重试 PUT（不做分块断点，spec §4.5.2 第 3 步）：成功码即成功；耗尽返回 false。 */
    private fun putWithRetry(
        item: DeliveryUploadItem,
        file: File,
    ): Boolean {
        var attempt = 0
        while (attempt < MAX_ATTEMPTS) {
            attempt++
            try {
                val outcome = transport.upload(blobUrl(item.sha256), authHeaders(), file.length()) { FileInputStream(file) }
                if (outcome.isSuccess()) return true
                adapter.warn("交付上传返回非成功码：路径=${item.path}，code=${outcome.statusCode}，尝试=$attempt")
            } catch (e: IOException) {
                adapter.warn("交付上传中断：路径=${item.path}，尝试=$attempt，原因=${e.javaClass.simpleName}")
            }
        }
        return false
    }

    /** 单项上传结果分类。 */
    private enum class Outcome { DEDUPED, UPLOADED, FAILED }

    private companion object {
        /** 单文件上传重试上限（spec §4.5.2）。 */
        private const val MAX_ATTEMPTS = 3
    }
}

/**
 * 上传阶段结果。
 *
 * @param ok       全部待传项是否成功（含去重跳过）
 * @param uploaded 实际流式上传的文件数
 * @param deduped  HEAD 去重跳过的文件数
 * @param error    失败原因（含路径，脱敏；成功为空串）
 */
data class DeliveryUploadResult(
    val ok: Boolean,
    val uploaded: Int,
    val deduped: Int,
    val error: String,
)

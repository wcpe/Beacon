package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.transport.BlobDownloadOutcome
import top.wcpe.beacon.agent.core.transport.BlobHeadResult
import top.wcpe.beacon.agent.core.transport.BlobStreamTransport
import top.wcpe.beacon.agent.core.transport.BlobTransferOutcome
import java.io.InputStream
import java.io.OutputStream

/**
 * 测试用假 blob 流式传输：三种操作各由可注入的行为 lambda 驱动，并记录调用供断言。
 *
 * 默认行为：HEAD 未就绪（404）、PUT 成功（204）、GET 整体 200 但不写内容——各测试按需覆写
 * [onHead] / [onUpload] / [onDownload]（download 的 lambda 可向 sink 写模拟内容 / 抛异常模拟中断）。
 */
class FakeBlobStreamTransport : BlobStreamTransport {
    /** HEAD 行为：入参为 url（测试用 blobUrl={it} 时即 sha）。 */
    var onHead: (String) -> BlobHeadResult = { BlobHeadResult(HTTP_NOT_FOUND, false, -1L) }

    /** PUT 行为：入参 url / contentLength / body 工厂。 */
    var onUpload: (String, Long, () -> InputStream) -> BlobTransferOutcome = { _, _, _ -> BlobTransferOutcome(HTTP_NO_CONTENT) }

    /** GET 行为：入参 url / rangeStart / sink；可向 sink 写内容或抛 IOException 模拟中断。 */
    var onDownload: (String, Long, OutputStream) -> BlobDownloadOutcome = { _, _, _ -> BlobDownloadOutcome(HTTP_OK, 0L) }

    /** 各操作调用记录（供断言去重跳过 / Range 续传 / 重试次数）。 */
    val headUrls: MutableList<String> = mutableListOf()
    val uploadUrls: MutableList<String> = mutableListOf()
    val downloadCalls: MutableList<Pair<String, Long>> = mutableListOf()

    override fun head(
        url: String,
        headers: Map<String, String>,
    ): BlobHeadResult {
        headUrls.add(url)
        return onHead(url)
    }

    override fun upload(
        url: String,
        headers: Map<String, String>,
        contentLength: Long,
        body: () -> InputStream,
    ): BlobTransferOutcome {
        uploadUrls.add(url)
        return onUpload(url, contentLength, body)
    }

    override fun download(
        url: String,
        headers: Map<String, String>,
        rangeStart: Long,
        sink: OutputStream,
    ): BlobDownloadOutcome {
        downloadCalls.add(url to rangeStart)
        return onDownload(url, rangeStart, sink)
    }

    private companion object {
        private const val HTTP_OK = 200
        private const val HTTP_NO_CONTENT = 204
        private const val HTTP_NOT_FOUND = 404
    }
}

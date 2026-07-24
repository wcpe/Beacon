package top.wcpe.beacon.agent.core.transport

import java.io.InputStream
import java.io.OutputStream

/**
 * 交付数据面 sha256 内容寻址 blob 流式传输抽象（ADR-0069）。
 *
 * 与既有 [HttpTransport]（全缓冲文本）、[StreamTransport]（单向 SSE 接收）并列为第三个传输端口：
 * 承载交付编排的大文件流式上传 / 下载。core 只依赖本接口；具体 OkHttp 实现（source / sink + Range +
 * Content-Length）落 agent-adapters 的 `OkHttpBlobStreamTransport`，为唯一触碰 OkHttp 的类（守 ADR-0005 与
 * 架构不变量 #5）。
 *
 * 铁律：全程流式不整读入内存；调用方负责在 async 线程使用（上传 / 下载 / 落盘均阻塞 IO，绝不上 MC 主线程）。
 * 连接级失败（无 HTTP 响应）以异常抛出交上层重试判定；收到 HTTP 响应则返回带状态码的 outcome。
 */
interface BlobStreamTransport {
    /**
     * HEAD 就绪查询（去重与断点判断）：ready 时服务端回 200 + Content-Length，未就绪 / 不存在回 404。
     *
     * @param url     完整 blob URL（`.../stream/delivery/blobs/{sha256}`）
     * @param headers 鉴权头（X-Beacon-Token / X-Beacon-Identity / X-Beacon-Boot）
     * @return 就绪度与已知大小；连接级失败抛异常
     */
    fun head(
        url: String,
        headers: Map<String, String>,
    ): BlobHeadResult

    /**
     * 流式 PUT 上传：`Content-Length` 必填（[contentLength]），服务端边收边算 sha256 与 URL 声明比对，
     * 不符回 422、一致原子入位回 204。
     *
     * @param body 每次调用创建一个新的输入流（供实现按需重放请求体）；实现负责关闭它
     * @return 带 HTTP 状态码的 outcome；连接级失败抛异常
     */
    fun upload(
        url: String,
        headers: Map<String, String>,
        contentLength: Long,
        body: () -> InputStream,
    ): BlobTransferOutcome

    /**
     * 流式 GET 下载并写入 [sink]：[rangeStart] > 0 时带 `Range: bytes=rangeStart-` 走断点续传。
     *
     * 实现只往 [sink] 写、**不关闭 [sink]**（由调用方管理其生命周期，便于续传时以 append 模式复用）。
     *
     * @return 带 HTTP 状态码与本次写入字节数的 outcome；连接级失败抛异常
     */
    fun download(
        url: String,
        headers: Map<String, String>,
        rangeStart: Long,
        sink: OutputStream,
    ): BlobDownloadOutcome
}

/**
 * HEAD 结果。
 *
 * @param statusCode HTTP 状态码（200=就绪 / 404=未就绪或不存在）
 * @param ready      blob 是否已就绪（可跳过上传 / 可下载）
 * @param sizeBytes  就绪时的字节数（未就绪为 -1）
 */
data class BlobHeadResult(
    val statusCode: Int,
    val ready: Boolean,
    val sizeBytes: Long,
)

/**
 * 上传结果。
 *
 * @param statusCode HTTP 状态码（204=成功 / 422=sha 不符 / 其它=失败）
 */
data class BlobTransferOutcome(
    val statusCode: Int,
) {
    /** 上传是否成功入位（2xx 均视作成功；服务端成功码为 204）。 */
    fun isSuccess(): Boolean = statusCode in SUCCESS_RANGE

    private companion object {
        private val SUCCESS_RANGE = 200..299
    }
}

/**
 * 下载结果。
 *
 * @param statusCode   HTTP 状态码（200=整体 / 206=区间 / 其它=失败）
 * @param bytesWritten 本次写入 sink 的字节数
 */
data class BlobDownloadOutcome(
    val statusCode: Int,
    val bytesWritten: Long,
) {
    /** 是否收到可用响应体（整体 200 或区间 206）。 */
    fun hasBody(): Boolean = statusCode == STATUS_OK || statusCode == STATUS_PARTIAL

    /** 服务端是否忽略了 Range 而返回整体（rangeStart>0 却回 200，需弃部分文件从头重下）。 */
    fun ignoredRange(): Boolean = statusCode == STATUS_OK

    private companion object {
        private const val STATUS_OK = 200
        private const val STATUS_PARTIAL = 206
    }
}

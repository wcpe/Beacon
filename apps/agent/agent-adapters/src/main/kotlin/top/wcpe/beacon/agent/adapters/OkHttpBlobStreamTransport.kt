package top.wcpe.beacon.agent.adapters

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okio.BufferedSink
import top.wcpe.beacon.agent.core.transport.BlobDownloadOutcome
import top.wcpe.beacon.agent.core.transport.BlobHeadResult
import top.wcpe.beacon.agent.core.transport.BlobStreamTransport
import top.wcpe.beacon.agent.core.transport.BlobTransferOutcome
import java.io.InputStream
import java.io.OutputStream
import java.util.concurrent.TimeUnit

/**
 * 基于 OkHttp 的交付 blob 流式传输实现（ADR-0069，ADR-0005 唯一碰具体库的类之一）。
 *
 * 上传用流式 [RequestBody]（writeTo 从调用方 [InputStream] 分块写网络 + 显式 Content-Length）；
 * 下载把响应体流直接拷进调用方 [OutputStream]（支持 Range），**不关闭 sink**（生命周期归调用方）。
 * 全程不把文件内容转字符串、不整读入内存。
 *
 * @param connectTimeoutMs 连接超时（毫秒）
 * @param readTimeoutMs    读 / 写超时（毫秒）：大文件流式传输取较大值，按分块 IO 间隔计
 */
class OkHttpBlobStreamTransport(
    connectTimeoutMs: Long = 5000,
    readTimeoutMs: Long = 60_000,
) : BlobStreamTransport {
    private val client: OkHttpClient =
        OkHttpClient.Builder()
            .connectTimeout(connectTimeoutMs, TimeUnit.MILLISECONDS)
            .readTimeout(readTimeoutMs, TimeUnit.MILLISECONDS)
            // 上传写超时同读超时：大文件分块写网络，避免默认 10s 在慢链路误断。
            .writeTimeout(readTimeoutMs, TimeUnit.MILLISECONDS)
            .build()

    override fun head(
        url: String,
        headers: Map<String, String>,
    ): BlobHeadResult {
        val request = requestBuilder(url, headers).head().build()
        client.newCall(request).execute().use { resp ->
            val ready = resp.code == STATUS_OK
            val size = if (ready) resp.header("Content-Length")?.toLongOrNull() ?: -1L else -1L
            return BlobHeadResult(resp.code, ready, size)
        }
    }

    override fun upload(
        url: String,
        headers: Map<String, String>,
        contentLength: Long,
        body: () -> InputStream,
    ): BlobTransferOutcome {
        val request = requestBuilder(url, headers).put(streamingBody(contentLength, body)).build()
        client.newCall(request).execute().use { resp ->
            return BlobTransferOutcome(resp.code)
        }
    }

    override fun download(
        url: String,
        headers: Map<String, String>,
        rangeStart: Long,
        sink: OutputStream,
    ): BlobDownloadOutcome {
        val builder = requestBuilder(url, headers).get()
        // 仅 rangeStart>0 才带 Range，让首次下载得整体 200、续传得区间 206。
        if (rangeStart > 0) builder.header("Range", "bytes=$rangeStart-")
        client.newCall(builder.build()).execute().use { resp ->
            var written = 0L
            // 仅在拿到可用响应体（整体 200 / 区间 206）时写盘；非 2xx 的错误体绝不拷进 sink。
            if (resp.code == STATUS_OK || resp.code == STATUS_PARTIAL) {
                resp.body?.byteStream()?.use { input -> written = input.copyTo(sink) }
            }
            return BlobDownloadOutcome(resp.code, written)
        }
    }

    private fun requestBuilder(
        url: String,
        headers: Map<String, String>,
    ): Request.Builder {
        val builder = Request.Builder().url(url)
        for ((k, v) in headers) {
            builder.header(k, v)
        }
        return builder
    }

    private fun streamingBody(
        contentLength: Long,
        body: () -> InputStream,
    ): RequestBody {
        val mediaType = "application/octet-stream".toMediaType()
        return object : RequestBody() {
            override fun contentType() = mediaType

            override fun contentLength(): Long = contentLength

            override fun writeTo(sink: BufferedSink) {
                body().use { input -> input.copyTo(sink.outputStream()) }
            }
        }
    }

    private companion object {
        /** HTTP 整体响应码。 */
        private const val STATUS_OK = 200

        /** HTTP 区间响应码（Range 断点续传）。 */
        private const val STATUS_PARTIAL = 206
    }
}

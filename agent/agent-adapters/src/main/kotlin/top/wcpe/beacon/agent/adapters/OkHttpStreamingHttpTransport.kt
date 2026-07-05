package top.wcpe.beacon.agent.adapters

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okio.BufferedSink
import top.wcpe.beacon.agent.core.transport.StreamingDownloadRequest
import top.wcpe.beacon.agent.core.transport.StreamingHttpResult
import top.wcpe.beacon.agent.core.transport.StreamingHttpTransport
import top.wcpe.beacon.agent.core.transport.StreamingUploadRequest
import java.io.InputStream
import java.util.concurrent.TimeUnit

/**
 * 基于 OkHttp 的流式 HTTP 传输实现。
 *
 * 上传从调用方提供的 [InputStream] 分块写入网络；下载把响应体流直接交给调用方处理。
 * 不把文件内容转成 JSON 字符串，也不把响应体整体读成字符串。
 */
class OkHttpStreamingHttpTransport(
    connectTimeoutMs: Long = 5000,
) : StreamingHttpTransport {
    private val client: OkHttpClient =
        OkHttpClient.Builder()
            .connectTimeout(connectTimeoutMs, TimeUnit.MILLISECONDS)
            .readTimeout(60, TimeUnit.SECONDS)
            .build()

    override fun upload(request: StreamingUploadRequest): StreamingHttpResult {
        val body = streamingBody(request)
        val httpRequest =
            requestBuilder(request.url, request.headers)
                .method(request.method.uppercase(), body)
                .build()
        return executeWithTimeout(httpRequest, request.readTimeoutMs) { response ->
            StreamingHttpResult(response.code)
        }
    }

    override fun download(
        request: StreamingDownloadRequest,
        sink: (InputStream) -> Unit,
    ): StreamingHttpResult {
        val httpRequest =
            requestBuilder(request.url, request.headers)
                .method(request.method.uppercase(), null)
                .build()
        return executeWithTimeout(httpRequest, request.readTimeoutMs) { response ->
            if (response.isSuccessful) {
                response.body?.byteStream()?.use(sink)
            }
            StreamingHttpResult(response.code)
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

    private fun streamingBody(request: StreamingUploadRequest): RequestBody {
        val mediaType = "application/octet-stream".toMediaType()
        return object : RequestBody() {
            override fun contentType() = mediaType

            override fun contentLength(): Long = request.contentLength ?: -1L

            override fun writeTo(sink: BufferedSink) {
                request.body().use { input -> input.copyTo(sink.outputStream()) }
            }
        }
    }

    private fun <T> executeWithTimeout(
        request: Request,
        readTimeoutMs: Long,
        block: (okhttp3.Response) -> T,
    ): T {
        val perCall =
            client.newBuilder()
                .readTimeout(readTimeoutMs, TimeUnit.MILLISECONDS)
                .build()
        return perCall.newCall(request).execute().use(block)
    }
}

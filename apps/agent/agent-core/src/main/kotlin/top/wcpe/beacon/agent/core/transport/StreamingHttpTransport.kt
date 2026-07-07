package top.wcpe.beacon.agent.core.transport

import java.io.InputStream

/**
 * 流式 HTTP 传输抽象（ADR-0058 数据面端口）。
 *
 * core 只依赖本接口；具体 OkHttp 实现在 agent-adapters。调用方负责在异步线程使用，
 * 避免在 MC 主线程执行上传 / 下载 / 落盘等阻塞 IO。
 */
interface StreamingHttpTransport {
    /** 流式上传请求体，返回 HTTP 状态码。 */
    fun upload(request: StreamingUploadRequest): StreamingHttpResult

    /** 流式下载响应体并交给调用方处理，返回 HTTP 状态码。 */
    fun download(
        request: StreamingDownloadRequest,
        sink: (InputStream) -> Unit,
    ): StreamingHttpResult
}

/**
 * 流式上传请求。
 *
 * @param method HTTP 方法，默认用于 blob 上传的 PUT
 * @param url 完整 URL
 * @param headers 请求头
 * @param contentLength 已知长度；未知时传 null
 * @param readTimeoutMs 读超时（毫秒）
 * @param body 每次请求创建一个新的输入流，传输实现负责关闭
 */
data class StreamingUploadRequest(
    val method: String = "PUT",
    val url: String,
    val headers: Map<String, String>,
    val contentLength: Long? = null,
    val readTimeoutMs: Long,
    val body: () -> InputStream,
)

/**
 * 流式下载请求。
 *
 * @param method HTTP 方法，默认 GET
 * @param url 完整 URL
 * @param headers 请求头
 * @param readTimeoutMs 读超时（毫秒）
 */
data class StreamingDownloadRequest(
    val method: String = "GET",
    val url: String,
    val headers: Map<String, String>,
    val readTimeoutMs: Long,
)

/** 流式 HTTP 调用结果。 */
data class StreamingHttpResult(
    val statusCode: Int,
)

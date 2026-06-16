package top.wcpe.beacon.agent.core.transport

/**
 * HTTP 传输抽象（ADR-0005）。core 只依赖本接口，具体实现（OkHttp）在 agent-adapters。
 *
 * 实现需按请求设置读超时（长轮询需长读超时）。
 */
interface HttpTransport {

    /** 同步执行一次请求并返回响应；连接级失败应抛异常由上层处理。 */
    fun execute(request: HttpRequest): HttpResponse
}

/**
 * HTTP 请求。
 *
 * @param method       HTTP 方法（GET / POST）
 * @param url          完整 URL
 * @param headers      请求头（含 X-Beacon-Token）
 * @param body         请求体文本（GET 时为 null）
 * @param readTimeoutMs 读超时（毫秒）；长轮询取较大值
 */
data class HttpRequest(
    val method: String,
    val url: String,
    val headers: Map<String, String>,
    val body: String?,
    val readTimeoutMs: Long,
)

/**
 * HTTP 响应。
 *
 * @param statusCode HTTP 状态码
 * @param body       响应体文本（可能为空字符串）
 */
data class HttpResponse(
    val statusCode: Int,
    val body: String,
)

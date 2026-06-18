package top.wcpe.beacon.agent.core.transport

/**
 * 流式传输抽象（扩展 ADR-0005，为 server→agent 单条 SSE 推送提供 core 端口）。
 *
 * core 只依赖本接口，具体的 SSE 客户端实现（纯 HTTP 读流）在 agent-adapters，
 * 守架构不变量 #5：HTTP / 流客户端只在适配器、core 不硬绑 OkHttp。
 *
 * 实现要点：
 * - 连接持有期间逐条解析 SSE 帧并回调 [StreamListener.onEvent]；
 * - 调用方在异步线程使用（绝不上 MC 主线程），[open] 同步阻塞直到流结束/断开；
 * - 连接结束（正常关闭 / 断线 / 读超时）返回，调用方据此退避重连。
 */
interface StreamTransport {

    /**
     * 打开一条 SSE 流并阻塞读取，逐帧回调 listener，直到流结束或 [request] 被中断。
     *
     * @param request 流请求（URL 含上报的各通道 md5；headers 含 X-Beacon-Token）
     * @param listener 事件与连接状态回调
     */
    fun open(request: StreamRequest, listener: StreamListener)
}

/**
 * SSE 流请求。
 *
 * @param url        完整流 URL（查询串携带 namespace/serverId 与各通道 md5）
 * @param headers    请求头（含 X-Beacon-Token）
 * @param readTimeoutMs 读超时（毫秒）：须显著大于服务端保活间隔，避免空闲被误判断流
 */
data class StreamRequest(
    val url: String,
    val headers: Map<String, String>,
    val readTimeoutMs: Long,
)

/**
 * SSE 流监听器：连接建立、收到事件、连接结束三类回调。
 */
interface StreamListener {

    /** 流成功建立（HTTP 200 且 Content-Type 为 text/event-stream）时回调一次。 */
    fun onOpen()

    /** 收到一条完整 SSE 事件（注释行/保活心跳不触发本回调）。 */
    fun onEvent(event: StreamEvent)

    /**
     * 流结束：正常关闭或异常断开都回调一次（之后不再有事件）。
     *
     * @param error 断开原因；正常关闭为 null
     */
    fun onClosed(error: Throwable?)
}

/**
 * 一条 SSE 事件：type 决定 agent 走哪条取数据-应用逻辑，data 为对应通道的新 md5（通知式，不含内容）。
 */
data class StreamEvent(
    val type: String,
    val data: String,
)

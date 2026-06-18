package top.wcpe.beacon.agent.adapters

import okhttp3.OkHttpClient
import okhttp3.Request
import top.wcpe.beacon.agent.core.stream.SseFrameParser
import top.wcpe.beacon.agent.core.transport.StreamListener
import top.wcpe.beacon.agent.core.transport.StreamRequest
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.util.concurrent.TimeUnit

/**
 * 基于 OkHttp 的 SSE 流式传输实现（FR-24，ADR-0005 唯一碰具体库的类之一）。
 *
 * 纯 HTTP 读流：GET 一个 text/event-stream 响应，逐行读取喂给 core 的 [SseFrameParser] 解析成事件。
 * **不引入 okhttp-sse 等额外依赖**，无 netty、无重型件，符合架构不变量。
 *
 * [open] 同步阻塞直到流结束/断开，调用方在异步线程使用（绝不上 MC 主线程）。
 *
 * @param connectTimeoutMs 连接超时（毫秒）
 */
class OkHttpStreamTransport(
    connectTimeoutMs: Long = 5000,
) : StreamTransport {

    // 共享客户端：连接池复用；读超时按请求覆盖（SSE 需长读超时）。
    private val client: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(connectTimeoutMs, TimeUnit.MILLISECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS) // 基础不限读超时；具体每请求再覆盖
        .build()

    override fun open(request: StreamRequest, listener: StreamListener) {
        val perCall = client.newBuilder()
            .readTimeout(request.readTimeoutMs, TimeUnit.MILLISECONDS)
            .build()

        val builder = Request.Builder().url(request.url)
        for ((k, v) in request.headers) {
            builder.header(k, v)
        }
        // 明确声明只接受 SSE，便于反代/服务端按流处理。
        builder.header("Accept", "text/event-stream")

        var closeError: Throwable? = null
        try {
            perCall.newCall(builder.get().build()).execute().use { resp ->
                if (!resp.isSuccessful) {
                    listener.onClosed(IllegalStateException("SSE 流非 200：${resp.code}"))
                    return
                }
                val contentType = resp.header("Content-Type") ?: ""
                if (!contentType.startsWith("text/event-stream")) {
                    listener.onClosed(IllegalStateException("SSE 流 Content-Type 非 event-stream：$contentType"))
                    return
                }
                listener.onOpen()

                val source = resp.body?.source() ?: run {
                    listener.onClosed(IllegalStateException("SSE 流响应体为空"))
                    return
                }
                val parser = SseFrameParser()
                // 逐行读取直到流结束（服务端关闭 / 读超时 / 客户端取消 → readUtf8Line 返回 null 或抛异常）。
                while (true) {
                    val line = source.readUtf8Line() ?: break
                    val event = parser.feed(line)
                    if (event != null) {
                        listener.onEvent(event)
                    }
                }
            }
        } catch (e: Exception) {
            // 连接级异常（断线 / 读超时 / 取消）：作为断开原因上抛，由生命周期退避重连。
            closeError = e
        }
        listener.onClosed(closeError)
    }
}

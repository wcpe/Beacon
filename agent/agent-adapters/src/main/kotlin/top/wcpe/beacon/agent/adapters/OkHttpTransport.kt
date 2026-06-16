package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

/**
 * 基于 OkHttp 的 HttpTransport 实现（ADR-0005 唯一碰具体库的类之一）。
 *
 * 按请求设置读超时（长轮询需长读超时）；连接复用由共享 OkHttpClient 负责。
 *
 * @param connectTimeoutMs 连接超时（毫秒）
 */
class OkHttpTransport(
    connectTimeoutMs: Long = 5000,
) : HttpTransport {

    // 共享客户端：连接池复用；读超时按请求覆盖（newBuilder）。
    private val client: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(connectTimeoutMs, TimeUnit.MILLISECONDS)
        // 基础读超时设较大值，具体每请求再覆盖。
        .readTimeout(60, TimeUnit.SECONDS)
        .build()

    override fun execute(request: HttpRequest): HttpResponse {
        // 按请求读超时定制一个派生客户端（复用底层连接池/线程池）。
        val perCall = client.newBuilder()
            .readTimeout(request.readTimeoutMs, TimeUnit.MILLISECONDS)
            .build()

        val builder = Request.Builder().url(request.url)
        for ((k, v) in request.headers) {
            builder.header(k, v)
        }
        when (request.method.uppercase()) {
            "GET" -> builder.get()
            "POST" -> {
                val mediaType = "application/json; charset=utf-8".toMediaType()
                val body = (request.body ?: "").toRequestBody(mediaType)
                builder.post(body)
            }

            else -> throw IllegalArgumentException("不支持的 HTTP 方法：${request.method}")
        }

        perCall.newCall(builder.build()).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            return HttpResponse(statusCode = resp.code, body = text)
        }
    }
}

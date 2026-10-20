package top.wcpe.beacon.agent.adapters

import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpServer
import top.wcpe.beacon.agent.core.transport.StreamingDownloadRequest
import top.wcpe.beacon.agent.core.transport.StreamingHttpResult
import top.wcpe.beacon.agent.core.transport.StreamingUploadRequest
import java.io.ByteArrayInputStream
import java.io.File
import java.net.InetSocketAddress
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/** OkHttp 流式 HTTP 传输测试：上传 / 下载均经 InputStream，不走 JSON 字符串请求体。 */
class OkHttpStreamingHttpTransportTest {
    private val server: HttpServer = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
    private val executor = Executors.newSingleThreadExecutor()
    private val transport = OkHttpStreamingHttpTransport(connectTimeoutMs = 1000)

    // 读超时留足余量：本组用例打的是本机 com.sun.net.httpserver，正常情况下是毫秒级，
    // 但 Windows runner 上冷启动与 GC 停顿会让 2s 预算偶然耗尽（2026-10-20 发版 CI 即因
    // SocketTimeoutException 偶发失败、重跑即过）。超时只用于让卡死快速失败而非校验性能，
    // 放宽到 10s 不削弱任何断言的鉴别力。
    private val testReadTimeoutMs = 10_000L

    init {
        server.executor = executor
        server.start()
    }

    @AfterTest
    fun cleanup() {
        server.stop(0)
        executor.shutdownNow()
    }

    @Test
    fun `upload 从输入流上传字节`() {
        val received = AtomicReference<ByteArray>()
        server.createContext("/upload") { exchange ->
            received.set(exchange.requestBody.use { it.readBytes() })
            respond(exchange, 200, "ok")
        }

        val result =
            transport.upload(
                StreamingUploadRequest(
                    method = "PUT",
                    url = url("/upload"),
                    headers = mapOf("X-Beacon-Token" to "tk"),
                    contentLength = 12,
                    readTimeoutMs = testReadTimeoutMs,
                    body = { ByteArrayInputStream("hello-stream".toByteArray(StandardCharsets.UTF_8)) },
                ),
            )

        assertEquals(StreamingHttpResult(200), result)
        assertEquals("hello-stream", received.get().toString(StandardCharsets.UTF_8))
    }

    @Test
    fun `download 把响应流交给调用方落盘`() {
        server.createContext("/download") { exchange ->
            respond(exchange, 200, "binary\u0000data")
        }
        val target: File = Files.createTempFile("beacon-stream-download", ".bin").toFile()
        try {
            val result =
                transport.download(
                    StreamingDownloadRequest(
                        url = url("/download"),
                        headers = mapOf("X-Beacon-Token" to "tk"),
                        readTimeoutMs = testReadTimeoutMs,
                    ),
                ) { input ->
                    target.outputStream().use { output -> input.copyTo(output) }
                }

            assertEquals(StreamingHttpResult(200), result)
            assertTrue(target.readBytes().contentEquals("binary\u0000data".toByteArray(StandardCharsets.UTF_8)))
        } finally {
            target.delete()
        }
    }

    private fun url(path: String): String = "http://127.0.0.1:${server.address.port}$path"

    private fun respond(
        exchange: HttpExchange,
        status: Int,
        body: String,
    ) {
        val bytes = body.toByteArray(StandardCharsets.UTF_8)
        exchange.sendResponseHeaders(status, bytes.size.toLong())
        exchange.responseBody.use { it.write(bytes) }
    }
}

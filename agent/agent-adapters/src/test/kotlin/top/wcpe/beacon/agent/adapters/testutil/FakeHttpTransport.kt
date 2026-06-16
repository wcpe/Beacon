package top.wcpe.beacon.agent.adapters.testutil

import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport

/**
 * 测试用假 HttpTransport：按预设响应队列依次返回，并记录收到的请求供断言。
 */
class FakeHttpTransport(
    private val responses: MutableList<HttpResponse> = mutableListOf(),
) : HttpTransport {

    /** 记录每次执行的请求，供断言请求头 / URL / body。 */
    val captured: MutableList<HttpRequest> = mutableListOf()

    /** 预设下一个响应。 */
    fun enqueue(response: HttpResponse): FakeHttpTransport {
        responses.add(response)
        return this
    }

    override fun execute(request: HttpRequest): HttpResponse {
        captured.add(request)
        if (responses.isEmpty()) {
            throw IllegalStateException("无预设响应")
        }
        return responses.removeAt(0)
    }
}

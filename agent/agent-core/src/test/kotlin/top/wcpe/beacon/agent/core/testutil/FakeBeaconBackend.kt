package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.CountDownLatch
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

/**
 * 测试用假控制面后端：transport 按 URL 路由返回 canned 响应并统计各端点调用次数；
 * codec 走「body 即 key」的极简旁路（encode 仅记录入参不参与断言，decode 按 body key 取预置树）。
 *
 * 关键能力：register 可挂在 latch 上「保持在飞」，配合并发 reconnect 断言单飞不变量。
 */
class FakeBeaconBackend : HttpTransport {

    /** 各端点累计调用次数（用于断言单飞）。 */
    val registerCalls = AtomicInteger(0)
    val heartbeatCalls = AtomicInteger(0)
    val pollCalls = AtomicInteger(0)
    val reportCalls = AtomicInteger(0)

    /** 反向抓取拉命令端点累计调用次数（FR-39，断言 command-pending / READY 触发）。 */
    val commandsCalls = AtomicInteger(0)

    /** 拉命令响应码（默认 204 无待办；置 200 走拉命令成功路径）。 */
    @Volatile
    var commandsStatus: Int = 204

    /** 任意时刻正在执行 register 的并发数与历史峰值（单飞要求峰值=1）。 */
    val inFlightRegister = AtomicInteger(0)
    val maxConcurrentRegister = AtomicInteger(0)

    /** register 进入时打开此 latch（让测试感知「已开始注册」），再阻塞于 releaseRegister。 */
    @Volatile
    var registerEntered: CountDownLatch? = null

    /** 非 null 时 register 会阻塞直到该 latch 被 countDown（模拟慢注册以放大并发窗口）。 */
    @Volatile
    var releaseRegister: CountDownLatch? = null

    /** 心跳响应码（默认 200；置 404 触发重注册路径）。 */
    @Volatile
    var heartbeatStatus: Int = 200

    /** 长轮询响应码（默认 304；置 200 走 apply 路径）。 */
    @Volatile
    var pollStatus: Int = 304

    /** 长轮询最近一次收到的 md5 参数（null 表示 md5 为空，即强制重拉）。 */
    val lastPollMd5 = AtomicReference<String?>(null)

    /** 累计收到「空 md5」长轮询的次数（forcePollNow 旁路 304 的可断言信号）。 */
    val emptyMd5PollCalls = AtomicInteger(0)

    override fun execute(request: HttpRequest): HttpResponse {
        val url = request.url
        return when {
            url.endsWith("/register") -> handleRegister()
            url.contains("/heartbeat") -> {
                heartbeatCalls.incrementAndGet()
                if (heartbeatStatus == 200) HttpResponse(200, BODY_HEARTBEAT) else HttpResponse(heartbeatStatus, "")
            }

            url.contains("/config/effective") -> {
                pollCalls.incrementAndGet()
                val md5 = extractMd5(url)
                lastPollMd5.set(md5)
                if (md5 == null) emptyMd5PollCalls.incrementAndGet()
                if (pollStatus == 200) HttpResponse(200, BODY_EFFECTIVE) else HttpResponse(pollStatus, "")
            }

            url.endsWith("/report") -> {
                reportCalls.incrementAndGet()
                HttpResponse(200, "")
            }

            url.contains("/agent/commands") -> {
                commandsCalls.incrementAndGet()
                if (commandsStatus == 200) HttpResponse(200, BODY_COMMAND) else HttpResponse(commandsStatus, "")
            }

            else -> HttpResponse(404, "")
        }
    }

    private fun handleRegister(): HttpResponse {
        registerCalls.incrementAndGet()
        val now = inFlightRegister.incrementAndGet()
        maxConcurrentRegister.updateAndGet { if (now > it) now else it }
        try {
            registerEntered?.countDown()
            releaseRegister?.await()
            return HttpResponse(200, BODY_REGISTER)
        } finally {
            inFlightRegister.decrementAndGet()
        }
    }

    /** 从 effective URL 的查询串里取 md5 值；空串返回 null（即「强制重拉」语义）。 */
    private fun extractMd5(url: String): String? {
        val idx = url.indexOf("md5=")
        if (idx < 0) return null
        val rest = url.substring(idx + 4)
        val end = rest.indexOf('&').let { if (it < 0) rest.length else it }
        val value = rest.substring(0, end)
        return if (value.isEmpty()) null else value
    }

    companion object {
        const val BODY_REGISTER = "register-ok"
        const val BODY_HEARTBEAT = "heartbeat-ok"
        const val BODY_EFFECTIVE = "effective-v1"
        const val BODY_COMMAND = "command-ingest"
    }
}

/**
 * 极简 codec：encode 返回固定占位（请求体内容测试不关心），
 * decode 按 body key 返回预置泛型树，喂给 BeaconApiClient 的解析器。
 */
class CannedJsonCodec : JsonCodec {

    override fun encode(value: Any?): String = "encoded"

    override fun decode(json: String): Any? = when (json) {
        FakeBeaconBackend.BODY_REGISTER -> mapOf(
            "instanceKey" to "prod/lobby-1",
            "resolvedGroup" to "area1",
            "resolvedZone" to "zoneA",
            "heartbeatIntervalSec" to 10,
            "ttlSec" to 30,
            "assigned" to true,
        )

        FakeBeaconBackend.BODY_HEARTBEAT -> mapOf("ttlSec" to 30, "configDirty" to false)

        FakeBeaconBackend.BODY_COMMAND -> mapOf(
            "id" to 1,
            "type" to "ingest-plugins",
            "payload" to mapOf("scope" to "group", "group" to "area1", "target" to ""),
        )

        FakeBeaconBackend.BODY_EFFECTIVE -> mapOf(
            "namespace" to "prod",
            "serverId" to "lobby-1",
            "group" to "area1",
            "zone" to "zoneA",
            "md5" to "md5-v1",
            "items" to listOf(
                mapOf("dataId" to "demo.yml", "format" to "yaml", "md5" to "i1", "content" to "k: v"),
            ),
        )

        else -> emptyMap<String, Any?>()
    }
}

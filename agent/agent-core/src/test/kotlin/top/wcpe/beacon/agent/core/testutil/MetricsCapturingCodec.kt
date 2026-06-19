package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicReference

/**
 * 测试用 codec：decode 复用 [CannedJsonCodec] 的预置树（让注册/长轮询解析照常工作），
 * encode 额外捕获「含 report 报文键」的最近一次 Map（含 memUsed/memMax/cpuLoad），供指标上报断言。
 *
 * 仅捕获 report 报文（按是否含 appliedMd5 键判定），避免 register/heartbeat 体覆盖断言目标。
 */
class MetricsCapturingCodec : JsonCodec {

    private val canned = CannedJsonCodec()

    /** 最近一次 report 报文体（Map）；null 表示尚未发生 report。 */
    val lastReport = AtomicReference<Map<String, Any?>?>(null)

    @Suppress("UNCHECKED_CAST")
    override fun encode(value: Any?): String {
        if (value is Map<*, *> && value.containsKey("appliedMd5")) {
            lastReport.set(value as Map<String, Any?>)
        }
        return canned.encode(value)
    }

    override fun decode(json: String): Any? = canned.decode(json)
}

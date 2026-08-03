package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.HealthMetrics
import top.wcpe.beacon.agent.core.client.report
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics

// ---- 周期性指标上报循环（FR-32 / FR-34） ----

/**
 * 启动周期性指标上报循环：把负载指标（人数 / TPS / 内存 / CPU / BC 专属）的上报与「配置是否变更」解耦。
 *
 * 根因修复：此前 reportApplied 仅在长轮询 / SSE 返回 200（配置 md5 变更）时触发，稳态配置不变恒 304 →
 * 采集供给从不被调用 → 控制面注册表指标恒为零值 → 看板与趋势全 0。现按心跳周期持续上报，让注册表常新。
 * 首跳立即发（注册 / 重连后尽快点亮看板真值），其后每 heartbeatIntervalMs 续杯；全程异步、不阻塞主线程、
 * 采集 / 上报失败均已在内部回退（fail-static 不变）。
 */
internal fun AgentLifecycle.startMetricsReportLoop() {
    val gen = metricsReportGen.get() + 1
    metricsReportGen.set(gen)
    scheduleMetricsReport(gen, 0)
}

internal fun AgentLifecycle.scheduleMetricsReport(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    if (delayMs <= 0) {
        adapter.runAsync { metricsReportTick(gen) }
    } else {
        adapter.runAsyncDelayed(delayMs) { metricsReportTick(gen) }
    }
}

internal fun AgentLifecycle.metricsReportTick(gen: Int) {
    // 代标识不符（已重启循环或已 shutdown）→ 当前跳作废。
    if (!running.get() || gen != metricsReportGen.get()) return
    // 以本地当前已应用的有效配置 md5 上报（稳态即上次应用值，准确而非陈旧；尚无配置时为空串，控制面按空处理）。
    reportApplied(store.currentMd5() ?: "")
    scheduleMetricsReport(gen, heartbeatIntervalMs)
}

internal fun AgentLifecycle.reportApplied(appliedMd5: String) {
    // 上报失败仅告警，不影响主流程。指标取当前一帧（人数 / TPS / 内存 / CPU），由壳层注入的供给采集；
    // 未注入时为零指标（向后兼容）。本调用在 async 上报线程内，指标采集为廉价 MXBean / Runtime 读取，不阻塞主线程。
    val metrics = currentMetrics()
    val ok =
        apiClient.report(
            identity,
            appliedMd5,
            health =
                HealthMetrics(
                    playerCount = metrics.playerCount,
                    tps = metrics.tps,
                    memUsed = metrics.memUsed,
                    memMax = metrics.memMax,
                    cpuLoad = metrics.cpuLoad,
                ),
            backends = currentBackends(),
            proxy = currentProxyMetrics(),
        )
    if (!ok) {
        adapter.warn("上报 applied 状态失败（不影响有效配置生效）")
    }
}

/** 取当前一帧运行指标；供给抛异常时回退零指标，绝不让上报因采集失败而中断。 */
internal fun AgentLifecycle.currentMetrics(): RuntimeMetrics {
    return try {
        metricsProvider()
    } catch (e: Exception) {
        adapter.warn("采集运行指标失败，本次按零指标上报：${e.message}")
        RuntimeMetrics.ZERO
    }
}

/** 取当前后端归属集合（FR-36）；供给抛异常时回退空集，绝不让注册/上报因采集失败而中断。 */
internal fun AgentLifecycle.currentBackends(): List<String> {
    return try {
        backendsProvider()
    } catch (e: Exception) {
        adapter.warn("采集后端归属集合失败，本次按空集上报：${e.message}")
        emptyList()
    }
}

/** 取当前 BC 专属指标（FR-34）；供给抛异常时回退 null（本次不上报 proxy 段），绝不让上报因采集失败而中断。 */
internal fun AgentLifecycle.currentProxyMetrics(): ProxyMetrics? {
    return try {
        proxyMetricsProvider()
    } catch (e: Exception) {
        adapter.warn("采集 BC 专属指标失败，本次不上报 proxy 段：${e.message}")
        null
    }
}

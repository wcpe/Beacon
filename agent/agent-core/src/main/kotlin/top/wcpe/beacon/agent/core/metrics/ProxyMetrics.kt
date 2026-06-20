package top.wcpe.beacon.agent.core.metrics

/**
 * BC（bungee 代理）专属负载指标载体（FR-34）：随 report 上报的代理自身负载快照。
 *
 * 仅 bc 壳采集并注入（bukkit 不提供，恒不上报本段）。全是「负载计数事实」（连接 / 线程 / 时长 / 后端可达性），
 * 仅供控制面看板展示、不参与调度决策；**不含玩家名单 / 身份**（看人归③层业务插件，越界，守 ADR-0022）。
 *
 * 网络吞吐入 / 出字节数本期不采（BungeeCord 无干净 Netty 注入点，见 ADR-0025），故本载体不含吞吐字段、不留占位。
 *
 * @param onlineConnections    代理当前在线连接数（ProxyServer.getOnlineCount）
 * @param threadCount          JVM 活动线程数（ThreadMXBean.getThreadCount）
 * @param uptimeMs             JVM 运行毫秒数（RuntimeMXBean.getUptime）
 * @param backendUp            可达后端子服数（ServerInfo.ping 成功计数）
 * @param backendTotal         配置的后端子服总数（代理服务器目录条目数）
 * @param backendAvgLatencyMs  到可达后端的平均 ping 延迟（毫秒）；无可达后端时为 [LATENCY_UNAVAILABLE]（-1.0）
 */
data class ProxyMetrics(
    val onlineConnections: Int,
    val threadCount: Int,
    val uptimeMs: Long,
    val backendUp: Int,
    val backendTotal: Int,
    val backendAvgLatencyMs: Double,
) {

    companion object {
        /** 后端平均延迟不可用哨兵：无任何可达后端时上报，与「0ms 真实极低延迟」区分，由控制面判定不可用。 */
        const val LATENCY_UNAVAILABLE: Double = -1.0
    }
}

/**
 * BC 专属指标供给（FR-34）：bc 壳注入；lifecycle 上报时调用取当前一帧代理指标。
 * 返回 null 表示本实例非代理 / 不采集（bukkit 默认即此，上报不带 proxy 段，向后兼容）。
 */
typealias ProxyMetricsProvider = () -> ProxyMetrics?

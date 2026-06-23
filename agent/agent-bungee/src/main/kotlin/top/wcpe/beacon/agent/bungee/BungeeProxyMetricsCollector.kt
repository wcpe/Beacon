package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import top.wcpe.beacon.agent.core.metrics.BackendReachability
import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.TcpBackendProbe
import taboolib.common.platform.function.warning

/**
 * BungeeCord 代理侧 BC 专属负载指标采集（FR-34）：连接数 + 线程 + 运行时长 + 后端可达性·延迟。
 *
 * 注入给 core 的 proxyMetricsProvider，由 lifecycle 在 async 上报线程内调用——绝不在主线程做阻塞 IO。
 * 后端可达性走 **TCP 连接探测**（[TcpBackendProbe]，[ADR-0035] 取代 ADR-0025 的 MC status-ping）：
 * 取代理目录里每个后端的 socket 地址，逐后端在独立守护线程池并发发起带超时的 TCP 连接，连上即可达、
 * 连接被拒/超时即不可达，RTT 取连接耗时。相比 status-ping 更稳健——代理后端常因转发模式不应答
 * server-list-ping，但端口可连即代理可路由（真机实测：Paper 后端 TCP 可连但 status ping 超时）。
 * 聚合是 core 侧无副作用纯函数（[BackendReachability]）；探测不碰 MC 主线程（守架构不变量 #5）。
 *
 * 网络吞吐入 / 出字节数本期不采（BungeeCord 无干净 Netty pipeline 注入点，见 ADR-0025），故本采集器不含吞吐。
 */
object BungeeProxyMetricsCollector {

    /** 后端 TCP 连接探测超时（毫秒）：在此内未建立连接即按不可达计，避免少数慢后端拖长整轮采集。 */
    private const val CONNECT_TIMEOUT_MS = 3_000L

    /** 上次已告警的探测异常 / 空目录签名；仅在内容变化时再打 WARN，避免每个上报周期刷屏。 */
    @Volatile
    private var lastProbeNote: String? = null

    /** 采一帧 BC 专属指标：在线连接数 + JVM 线程 / 运行时长 + 后端可达性·延迟。 */
    fun sample(): ProxyMetrics {
        val reach = probeBackends()
        return ProxyMetrics(
            onlineConnections = onlineCount(),
            threadCount = JvmRuntimeMetrics.threadCount(),
            uptimeMs = JvmRuntimeMetrics.uptimeMs(),
            backendUp = reach.up,
            backendTotal = reach.total,
            backendAvgLatencyMs = reach.avgLatencyMs,
        )
    }

    /** 代理当前在线连接数；异常回退 0，不让采集失败影响上报。 */
    private fun onlineCount(): Int {
        return try {
            ProxyServer.getInstance().onlineCount
        } catch (e: Exception) {
            0
        }
    }

    /**
     * 取代理目录里全部后端的 socket 地址，交 [TcpBackendProbe] 并发 TCP 连接探测，再由 core 纯函数聚合。
     *
     * 异常 / 空目录回退空集（延迟不可用），不抛、不阻断上报；但不静默——变化时打一条 WARN（去重防刷屏），
     * 便于运维定位为何可达性为 0。
     */
    private fun probeBackends(): BackendReachability.Reachability {
        return try {
            val addresses = ProxyServer.getInstance().servers.values.map { it.socketAddress }
            if (addresses.isEmpty()) {
                note("代理后端目录为空，本轮可达性按无后端计")
                return BackendReachability.summarize(emptyList())
            }
            val reach = BackendReachability.summarize(TcpBackendProbe.probe(addresses, CONNECT_TIMEOUT_MS))
            lastProbeNote = null // 成功采到一轮，清告警去重以便下次异常再报一条
            reach
        } catch (e: Exception) {
            note("后端可达性采集异常，本轮按无后端回退：${e::class.java.simpleName}: ${e.message}")
            BackendReachability.summarize(emptyList())
        }
    }

    /** 探测异常 / 空目录告警：仅在内容变化时打一条 WARN，避免每个上报周期重复刷屏。 */
    private fun note(msg: String) {
        if (msg != lastProbeNote) {
            lastProbeNote = msg
            warning("[BC可达性] $msg")
        }
    }
}

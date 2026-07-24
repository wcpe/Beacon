package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import taboolib.common.platform.function.warning
import top.wcpe.beacon.agent.core.metrics.BackendReachability
import top.wcpe.beacon.agent.core.metrics.TcpBackendProbe

/**
 * BungeeCord 代理侧 BC 专属指标的**原始采集件**（FR-34 / FR-144）：在线连接数（廉价）与后端可达性 TCP 探测（阻塞）。
 *
 * 拆分两类读取以适配 FR-144 的 1s 采样：
 * - [onlineCount] 廉价即时读，可每秒采。
 * - [probeReachability] 逐后端 TCP 连接探测（[TcpBackendProbe]，[ADR-0035] 取代 ADR-0025 的 MC status-ping）——
 *   阻塞、耗时（每后端最多 [CONNECT_TIMEOUT_MS]），**不可每秒采**；由 [BungeeProxyMetricsCache] 慢刷缓存，
 *   1s 采样只读缓存值。连上即可达（RTT=连接耗时），连接被拒 / 超时即不可达；聚合是 core 无副作用纯函数
 *   [BackendReachability]。探测在独立守护线程池执行、绝不碰 MC 调度线程（守架构不变量 §5）。
 *
 * 网络吞吐入 / 出字节数本期不采（BungeeCord 无干净 Netty 注入点，见 ADR-0025），不留占位。
 */
object BungeeProxyMetricsCollector {
    /** 后端 TCP 连接探测超时（毫秒）：在此内未建立连接即按不可达计，避免少数慢后端拖长整轮采集。 */
    private const val CONNECT_TIMEOUT_MS = 3_000L

    /** 上次已告警的探测异常 / 空目录签名；仅在内容变化时再打 WARN，避免每个刷新周期刷屏。 */
    @Volatile
    private var lastProbeNote: String? = null

    /** 代理当前在线连接数（廉价即时读）；异常回退 0，不让采集失败影响上报。 */
    fun onlineCount(): Int {
        return try {
            ProxyServer.getInstance().onlineCount
        } catch (e: Exception) {
            0
        }
    }

    /**
     * 取代理目录里全部后端的 socket 地址，交 [TcpBackendProbe] 并发 TCP 连接探测，再由 core 纯函数聚合可达性。
     *
     * **阻塞、耗时**（须在 async 线程调用，由 [BungeeProxyMetricsCache] 慢刷）。异常 / 空目录回退空集
     * （延迟不可用），不抛、不阻断；但不静默——变化时打一条 WARN（去重防刷屏），便于运维定位为何可达性为 0。
     */
    fun probeReachability(): BackendReachability.Reachability {
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

    /** 探测异常 / 空目录告警：仅在内容变化时打一条 WARN，避免每个刷新周期重复刷屏。 */
    private fun note(msg: String) {
        if (msg != lastProbeNote) {
            lastProbeNote = msg
            warning("[BC可达性] $msg")
        }
    }
}

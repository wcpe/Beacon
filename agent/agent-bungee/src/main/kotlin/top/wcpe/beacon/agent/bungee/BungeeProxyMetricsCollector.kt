package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import net.md_5.bungee.api.config.ServerInfo
import top.wcpe.beacon.agent.core.metrics.BackendReachability
import top.wcpe.beacon.agent.core.metrics.BackendReachability.PingProbe
import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * BungeeCord 代理侧 BC 专属负载指标采集（FR-34）：连接数 + 线程 + 运行时长 + 后端可达性·延迟。
 *
 * 注入给 core 的 proxyMetricsProvider，由 lifecycle 在 async 上报线程内调用——绝不在主线程做阻塞 IO。
 * 后端可达性走 `ServerInfo.ping`（BungeeCord 公开 API，异步回调发生在 Netty IO 线程），本采集器用
 * [CountDownLatch] 在上报线程内**有界等待**全部回调汇总，超时未回的后端按不可达计；任何异常按不可达回退，
 * 不让采集失败影响上报（守架构不变量 #5：不碰 MC 主线程、廉价/有界）。
 *
 * 网络吞吐入 / 出字节数本期不采（BungeeCord 无干净 Netty pipeline 注入点，见 ADR-0025），故本采集器不含吞吐。
 */
object BungeeProxyMetricsCollector {

    /** 后端 ping 单后端有界等待超时（毫秒）：超时未回即按不可达计，避免少数慢后端拖长整轮采集。 */
    private const val PING_TIMEOUT_MS = 1_000L

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
     * 逐后端发 `ServerInfo.ping` 并有界等待汇总可达性与延迟（聚合用 core 纯函数 [BackendReachability]）。
     *
     * 全部后端并发 ping（回调在 Netty IO 线程），用单个 [CountDownLatch] 等到全部回调或整体超时；
     * 整体超时上界 = 单后端超时（少数慢后端不叠加），剩余未回的按不可达计。异常一律回退空集（无后端）。
     */
    private fun probeBackends(): BackendReachability.Reachability {
        return try {
            val servers = ProxyServer.getInstance().servers.values.toList()
            if (servers.isEmpty()) {
                return BackendReachability.summarize(emptyList())
            }
            val probes = pingAll(servers)
            BackendReachability.summarize(probes)
        } catch (e: Exception) {
            // 取后端目录 / 发 ping 整体失败：按无后端回退（延迟不可用），不抛、不刷屏。
            BackendReachability.summarize(emptyList())
        }
    }

    /** 对全部后端并发 ping，有界等待回调，返回每后端探测结果（超时 / 未回按不可达）。 */
    private fun pingAll(servers: List<ServerInfo>): List<PingProbe> {
        val latch = CountDownLatch(servers.size)
        // 每后端一个结果槽：reachable 标志 + RTT 毫秒，回调线程写、本线程读（有界等待后）。
        val results = servers.map { PingSlot() }
        servers.forEachIndexed { idx, info ->
            val slot = results[idx]
            val startNanos = System.nanoTime()
            try {
                info.ping { ping, error ->
                    // 回调在 Netty IO 线程：仅记结果、放闩，绝不在此做阻塞操作。
                    if (error == null && ping != null) {
                        slot.reachable.set(true)
                        slot.latencyMs.set((System.nanoTime() - startNanos) / 1_000_000L)
                    }
                    latch.countDown()
                }
            } catch (e: Exception) {
                // 个别后端发 ping 即异常：按不可达计并立刻放闩，不拖累整轮等待。
                latch.countDown()
            }
        }
        // 有界等待全部回调；超时返回后，未回的后端 reachable 仍为 false（按不可达计）。
        try {
            latch.await(PING_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
        }
        return results.map { PingProbe(reachable = it.reachable.get(), latencyMs = it.latencyMs.get()) }
    }

    /** 单后端 ping 结果槽（回调线程写、采集线程读，用原子量隔离可见性）。 */
    private class PingSlot {
        val reachable = AtomicBoolean(false)
        val latencyMs = AtomicLong(0L)
    }
}

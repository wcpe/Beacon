package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics

/**
 * BungeeCord 代理侧运行指标采集（FR-32 相位1）：代理在线人数 + JVM 内存 / CPU。
 *
 * 注入给 core 的 metricsProvider，由 lifecycle 在 async 上报线程内调用——
 * 廉价读取（在线数、MXBean / Runtime），不阻塞调度线程（守架构不变量 #5）。
 *
 * 代理无 TPS 概念（无游戏主循环），TPS 恒上报 0。
 */
object BungeeMetricsCollector {

    /** 采一帧完整运行指标：内存 / CPU（core 平台无关采集）+ 代理在线人数；TPS 恒 0。 */
    fun sample(): RuntimeMetrics {
        val playerCount = onlineCount()
        return JvmRuntimeMetrics.sampleMemoryAndCpu().withPlayerCountAndTps(playerCount, tps = 0.0)
    }

    /** 代理当前在线人数；异常回退 0，不让采集失败影响上报。 */
    private fun onlineCount(): Int {
        return try {
            ProxyServer.getInstance().onlineCount
        } catch (e: Exception) {
            0
        }
    }
}

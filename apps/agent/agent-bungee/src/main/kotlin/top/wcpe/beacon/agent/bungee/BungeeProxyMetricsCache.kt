package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.core.metrics.BackendReachability
import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.atomic.AtomicBoolean

/**
 * BC 专属指标缓存（FR-144）：把**阻塞耗时**的后端可达性探测与 1s 采样解耦。
 *
 * 后端可达性 TCP 探测每轮最多耗数秒（[BungeeProxyMetricsCollector.probeReachability]），绝不能每秒采样时同步跑
 * ——否则采样线程被拖住、1s 节奏破坏、探测线程叠加。故本缓存在独立 async 循环里按 [refreshIntervalMs] 慢刷可达性，
 * [current] 由采样 / 上报线程调用时**现采**廉价的连接数 / 线程 / 运行时长、只**读缓存**的后端可达性，O(1) 不阻塞。
 *
 * 由 [BeaconAgentBungee] 持有实例，ENABLE 时 [start]、DISABLE 时 [stop]。探测在 adapter 的 async 线程执行，
 * 绝不碰 MC 调度线程（守架构不变量 §5）。
 */
class BungeeProxyMetricsCache(
    private val adapter: PlatformAdapter,
    private val refreshIntervalMs: Long = DEFAULT_REFRESH_INTERVAL_MS,
) {
    private val running = AtomicBoolean(false)

    /** 缓存的后端可达性（慢刷线程写、采样线程读）；初始无可达（延迟不可用）。 */
    @Volatile
    private var cachedReach: BackendReachability.Reachability =
        BackendReachability.Reachability(up = 0, total = 0, avgLatencyMs = ProxyMetrics.LATENCY_UNAVAILABLE)

    /** 启动慢刷循环（幂等）：首轮立即探一次，其后按 [refreshIntervalMs] 续杯。 */
    fun start() {
        if (!running.compareAndSet(false, true)) return
        scheduleRefresh()
    }

    /** 停止慢刷循环。 */
    fun stop() {
        running.set(false)
    }

    /**
     * 供采样 / 上报读一帧 BC 专属指标：现采连接 / 线程 / 运行时长（廉价 MXBean / 即时读）+ 缓存的后端可达性。
     */
    fun current(): ProxyMetrics {
        val reach = cachedReach
        return ProxyMetrics(
            onlineConnections = BungeeProxyMetricsCollector.onlineCount(),
            threadCount = JvmRuntimeMetrics.threadCount(),
            uptimeMs = JvmRuntimeMetrics.uptimeMs(),
            backendUp = reach.up,
            backendTotal = reach.total,
            backendAvgLatencyMs = reach.avgLatencyMs,
        )
    }

    private fun scheduleRefresh() {
        if (!running.get()) return
        adapter.runAsync {
            if (!running.get()) return@runAsync
            cachedReach = BungeeProxyMetricsCollector.probeReachability()
            if (running.get()) {
                adapter.runAsyncDelayed(refreshIntervalMs) { scheduleRefresh() }
            }
        }
    }

    private companion object {
        /** 后端可达性慢刷间隔（毫秒）：远大于探测耗时，避免探测叠加；采样读缓存值即可，秒级新鲜度足够。 */
        const val DEFAULT_REFRESH_INTERVAL_MS = 5_000L
    }
}

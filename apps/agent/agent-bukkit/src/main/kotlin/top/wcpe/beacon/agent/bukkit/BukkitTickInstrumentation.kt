package top.wcpe.beacon.agent.bukkit

import taboolib.common.platform.function.submit
import taboolib.common.platform.service.PlatformExecutor
import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import java.lang.reflect.Method

/**
 * Bukkit 子服侧主线程原子埋点（FR-144 §4.1，见 v2-metrics-health-scheduling.md）。
 *
 * **本类是 FR-144 核心改造**：此前指标采集在 async 上报线程反射调线程不安全的 `Bukkit.getOnlinePlayers()`
 * / `getTPS()`（违反架构不变量 §5「不在 MC 主线程做阻塞 IO」的反面——把主线程 API 拿到别的线程调同样是错）。
 * 改为：MC **主线程**每 tick 只做零成本埋点（tick 计数自增、每秒读一次在线数写 volatile），采样线程只读
 * volatile 值（TPS 由 tick 增量按墙钟间隔推算，见 [JvmRuntimeMetrics.estimateTps]），绝不在采样线程碰
 * 线程不安全的 Bukkit API。
 *
 * TPS 不再读 `Bukkit.getTPS()`（Spigot 无、且仍是主线程 API）：直接由主线程 tick 计数**测量**，跨平台统一。
 * 平台访问走反射（本壳无 Bukkit 编译期依赖）；反射 Method 缓存一次，避免每秒重取。
 *
 * 由 [BeaconAgentBukkit] 持有实例（非有状态单例，守 AGENTS §10.10），ENABLE 时 [start]、DISABLE 时 [stop]。
 */
class BukkitTickInstrumentation {
    /** 主线程周期 tick 任务句柄；stop 时取消。 */
    private var task: PlatformExecutor.PlatformTask? = null

    /** 累计 tick 数（仅主线程 tick 任务读写，无需同步）。 */
    private var tickCounter = 0L

    /** 上次推算 TPS 时的 tick 基线（仅主线程）。 */
    private var lastCalcTick = 0L

    /** 上次推算 TPS 时的纳秒时刻（仅主线程；0 表示尚未播种基线）。 */
    private var lastCalcNanos = 0L

    /** 最近一秒推算的 TPS（主线程写、采样线程读）。 */
    @Volatile
    private var tpsValue = 0.0

    /** 最近一次读到的在线人数（主线程写、采样线程读）。 */
    @Volatile
    private var onlineValue = 0

    /** 缓存 `Bukkit.getOnlinePlayers` 反射 Method（首个 tick 于主线程解析一次）。 */
    private val onlinePlayersMethod: Method? by lazy(::resolveOnlinePlayersMethod)

    /** 启动主线程每 tick 埋点任务（幂等：已启动则不重复启）。 */
    fun start() {
        if (task != null) return
        task = submit(async = false, period = 1L) { tick() }
    }

    /** 停止埋点任务。 */
    fun stop() {
        task?.cancel()
        task = null
    }

    /** 采样线程读：最近一秒推算的 TPS（0~20）。 */
    fun currentTps(): Double = tpsValue

    /** 采样线程读：最近一次在线人数。 */
    fun onlineCount(): Int = onlineValue

    /** 主线程每 tick：自增计数，约每秒（墙钟）推算一次 TPS 并刷新在线数。 */
    private fun tick() {
        tickCounter++
        val now = System.nanoTime()
        if (lastCalcNanos == 0L) {
            // 首个 tick：播种基线并立即读一次在线数（避免头一秒读到 0）。
            lastCalcNanos = now
            lastCalcTick = tickCounter
            onlineValue = readOnline()
            return
        }
        val elapsed = now - lastCalcNanos
        if (elapsed >= RECALC_INTERVAL_NANOS) {
            tpsValue = JvmRuntimeMetrics.estimateTps(tickCounter - lastCalcTick, elapsed)
            onlineValue = readOnline()
            lastCalcTick = tickCounter
            lastCalcNanos = now
        }
    }

    /** 在主线程读在线人数：反射 `Bukkit.getOnlinePlayers().size()`；不可得维持上次值，异常回退 0。 */
    private fun readOnline(): Int {
        val method = onlinePlayersMethod ?: return onlineValue
        return try {
            (method.invoke(null) as? Collection<*>)?.size ?: 0
        } catch (e: Exception) {
            0
        }
    }

    private fun resolveOnlinePlayersMethod(): Method? =
        try {
            Class.forName("org.bukkit.Bukkit").getMethod("getOnlinePlayers")
        } catch (e: Exception) {
            null
        }

    private companion object {
        /** TPS 重算的墙钟间隔（纳秒）：约每秒一次。 */
        const val RECALC_INTERVAL_NANOS = 1_000_000_000L
    }
}

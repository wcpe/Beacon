package top.wcpe.beacon.agent.bukkit

import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics

/**
 * Bukkit 子服侧运行指标组装（FR-32 / FR-144）：把主线程原子埋点采到的在线人数 / TPS 与 JVM 内存 / CPU 合成一帧。
 *
 * **纯组装、无平台调用、无状态**（守 AGENTS §10.10）：在线人数与 TPS 由 [BukkitTickInstrumentation] 在
 * **MC 主线程**埋点后以入参传入，本函数只叠加平台无关的 JVM 内存 / CPU 采集（廉价 MXBean / Runtime 读）。
 *
 * 由 lifecycle 在 async 上报 / 采样线程内调用——**不再**像旧实现那样在 async 线程反射调线程不安全的
 * `Bukkit.getOnlinePlayers()` / `getTPS()`（那是 FR-144 §4.1 明令禁止的做法，改造见 [BukkitTickInstrumentation]）。
 */
object BukkitMetricsCollector {
    /**
     * 组装一帧完整运行指标：JVM 内存 / CPU（平台无关）+ 主线程埋点传入的在线人数 / TPS。
     *
     * @param tps    主线程 tick 埋点推算的 TPS（0~20）
     * @param online 主线程埋点读到的在线人数
     */
    fun sample(
        tps: Double,
        online: Int,
    ): RuntimeMetrics = JvmRuntimeMetrics.sampleMemoryAndCpu().withPlayerCountAndTps(online, tps)
}

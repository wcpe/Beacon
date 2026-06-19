package top.wcpe.beacon.agent.bukkit

import top.wcpe.beacon.agent.core.metrics.JvmRuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics

/**
 * Bukkit 子服侧运行指标采集（FR-32 相位1）：在线人数 + 服务器 TPS + JVM 内存 / CPU。
 *
 * 注入给 core 的 metricsProvider，由 lifecycle 在 async 上报线程内调用——
 * 全是廉价读取（在线人数 size、TPS 数组、MXBean / Runtime），不在 MC 主线程做阻塞 IO（守架构不变量 #5）。
 *
 * 平台访问全走反射（`org.bukkit.Bukkit`）：本壳无 Bukkit/Spigot API 编译期依赖，避免为采集
 * 引入新第三方编译依赖；运行期由服务器自身提供 org.bukkit.* 类。
 * TPS：Paper 提供 `Server.getTPS()`（返回近 1/5/15 分钟三元组）；Spigot 无此 API，
 * 反射取不到回退 0（由 [JvmRuntimeMetrics.normalizeTps] 归一化）。
 */
object BukkitMetricsCollector {

    /** 采一帧完整运行指标：内存 / CPU（core 平台无关采集）+ 平台在线人数 / TPS。 */
    fun sample(): RuntimeMetrics {
        val playerCount = onlinePlayerCount()
        val tps = JvmRuntimeMetrics.normalizeTps(readServerTps())
        return JvmRuntimeMetrics.sampleMemoryAndCpu().withPlayerCountAndTps(playerCount, tps)
    }

    /**
     * 当前在线人数：反射 `Bukkit.getOnlinePlayers().size()`；异常（极端时序 / 类不可得）回退 0，
     * 不让采集失败影响上报。
     */
    private fun onlinePlayerCount(): Int {
        return try {
            val bukkit = Class.forName("org.bukkit.Bukkit")
            val players = bukkit.getMethod("getOnlinePlayers").invoke(null) as? Collection<*> ?: return 0
            players.size
        } catch (e: Exception) {
            0
        }
    }

    /**
     * 读服务器近 1 分钟 TPS：反射 `Bukkit.getServer().getTPS()`（Paper 有、Spigot 无）。
     *
     * 反射避免编译期硬绑 Bukkit/Paper API（本壳无 org.bukkit.* 编译依赖）；取不到返回 null，
     * 上层归一化为 0。
     */
    private fun readServerTps(): Double? {
        return try {
            val bukkit = Class.forName("org.bukkit.Bukkit")
            val server = bukkit.getMethod("getServer").invoke(null) ?: return null
            val tps = server.javaClass.getMethod("getTPS").invoke(server) as? DoubleArray ?: return null
            if (tps.isEmpty()) null else tps[0]
        } catch (e: Exception) {
            // Spigot 无 getTPS / 类或方法不可得：TPS 不可得，回退 null（归一化为 0）。
            null
        }
    }
}

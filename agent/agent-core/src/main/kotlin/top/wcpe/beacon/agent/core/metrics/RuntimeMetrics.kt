package top.wcpe.beacon.agent.core.metrics

/**
 * 平台无关的运行指标载体（FR-32 相位1）：随 report 上报的子服负载快照。
 *
 * 这些都是「健康事实」（负载数字），仅供控制面看板展示、不参与调度决策（ADR-0023）。
 * 玩家名单 / 身份不在此（看人归③层业务插件，越界）。
 *
 * @param playerCount 当前在线人数（壳层平台 API 采集；代理为代理在线数）
 * @param tps         服务器 TPS（Bukkit 取服务器 TPS；代理无 TPS 概念，恒 0）
 * @param memUsed     JVM 已用堆字节（totalMemory - freeMemory）
 * @param memMax      JVM 最大堆字节（maxMemory）
 * @param cpuLoad     进程 CPU 负载 [0,1]；取不到为 [CPU_UNAVAILABLE]（-1.0）表示不可用
 */
data class RuntimeMetrics(
    val playerCount: Int,
    val tps: Double,
    val memUsed: Long,
    val memMax: Long,
    val cpuLoad: Double,
) {

    /**
     * 在当前内存 / CPU 指标基础上合入平台采到的人数与 TPS。
     *
     * 壳层组装路径：先经 [JvmRuntimeMetrics] 采内存 / CPU（平台无关），再用平台 API 采到的
     * 人数 / TPS 合入，得到完整的一帧运行指标。
     */
    fun withPlayerCountAndTps(playerCount: Int, tps: Double): RuntimeMetrics =
        copy(playerCount = playerCount, tps = tps)

    companion object {
        /** CPU 负载不可用哨兵：与「0.0 表示真实空载」区分，由控制面判定不可用。 */
        const val CPU_UNAVAILABLE: Double = -1.0

        /** 零指标缺省：未注入指标供给时上报，向后兼容旧行为（人数 / TPS 恒 0、CPU 不可用）。 */
        val ZERO: RuntimeMetrics = RuntimeMetrics(
            playerCount = 0,
            tps = 0.0,
            memUsed = 0L,
            memMax = 0L,
            cpuLoad = CPU_UNAVAILABLE,
        )
    }
}

/** 运行指标供给：lifecycle 上报时调用取当前一帧指标；壳层注入真实采集实现。 */
typealias RuntimeMetricsProvider = () -> RuntimeMetrics

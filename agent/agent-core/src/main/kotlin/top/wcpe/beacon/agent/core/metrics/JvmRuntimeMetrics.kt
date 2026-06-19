package top.wcpe.beacon.agent.core.metrics

import java.lang.management.ManagementFactory

/**
 * JVM 侧内存 / CPU 采集（平台无关，FR-32 相位1）。
 *
 * 内存走 [Runtime]（已用堆 = total - free，最大堆 = max）；
 * CPU 走 `com.sun.management.OperatingSystemMXBean.getProcessCpuLoad`，经反射调用——
 * 该接口属 JDK 扩展，并非所有 JVM 都提供，故反射 + 取不到回退 [RuntimeMetrics.CPU_UNAVAILABLE]，
 * 不在 core 硬绑 `com.sun.*` 类型（守 DB 之外的可移植性，避免编译期依赖私有接口）。
 *
 * 均为廉价 MXBean / Runtime 调用，无阻塞 IO；由壳层在既有 async 上报线程内调用。
 */
object JvmRuntimeMetrics {

    /**
     * 采一帧内存 + CPU 指标（人数 / TPS 由壳层后续合入，见 [RuntimeMetrics.withPlayerCountAndTps]）。
     *
     * 返回的 playerCount / tps 为 0 占位；CPU 取不到为 -1.0（不可用）。
     */
    fun sampleMemoryAndCpu(): RuntimeMetrics {
        val runtime = Runtime.getRuntime()
        val total = runtime.totalMemory()
        val free = runtime.freeMemory()
        val memUsed = (total - free).coerceAtLeast(0L)
        // maxMemory 在未设上界时返回 Long.MAX_VALUE；此处原样上报，由控制面侧自行呈现。
        val memMax = runtime.maxMemory().coerceAtLeast(0L)
        return RuntimeMetrics(
            playerCount = 0,
            tps = 0.0,
            memUsed = memUsed,
            memMax = memMax,
            cpuLoad = readProcessCpuLoad(),
        )
    }

    /**
     * 归一化平台上报的 TPS 采样值（壳层可单测的纯逻辑）。
     *
     * 入参为平台读到的近 1 分钟 TPS（Paper `getTPS()[0]`）；null（接口不存在 / Spigot 取不到）或
     * NaN / 负值归 0.0；上界裁剪到 [maxTps]（默认 20.0，Paper 偶尔报略大于 20 的值，统一封顶）。
     */
    fun normalizeTps(raw: Double?, maxTps: Double = 20.0): Double {
        if (raw == null || raw.isNaN() || raw < 0.0) return 0.0
        return raw.coerceAtMost(maxTps)
    }

    /**
     * 读进程 CPU 负载 [0,1]；接口不可用 / 调用异常 / 越界一律回退 -1.0（不可用）。
     *
     * `getProcessCpuLoad` 在 JVM 刚启动的极短窗口可能返回负值（尚无采样），归一化为不可用。
     */
    private fun readProcessCpuLoad(): Double {
        return try {
            val osBean = ManagementFactory.getOperatingSystemMXBean()
            // com.sun.management.OperatingSystemMXBean.getProcessCpuLoad()：反射避免编译期硬绑 JDK 扩展接口。
            val method = osBean.javaClass.getMethod("getProcessCpuLoad")
            method.isAccessible = true
            val load = (method.invoke(osBean) as? Double) ?: return RuntimeMetrics.CPU_UNAVAILABLE
            if (load < 0.0 || load.isNaN()) RuntimeMetrics.CPU_UNAVAILABLE else load.coerceAtMost(1.0)
        } catch (e: Exception) {
            // 接口缺失 / 反射失败：CPU 不可用，回退哨兵（不抛、不刷屏）。
            RuntimeMetrics.CPU_UNAVAILABLE
        }
    }
}

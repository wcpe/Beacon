package top.wcpe.beacon.agent.core.metrics

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 运行指标载体 [RuntimeMetrics] 与默认值的单测（FR-32 相位1）。
 *
 * 覆盖：默认零值（向后兼容旧上报）、cpuLoad 不可用回退 -1.0、字段透传。
 */
class RuntimeMetricsTest {
    @Test
    fun `默认零指标各字段为安全缺省值`() {
        val zero = RuntimeMetrics.ZERO
        assertEquals(0, zero.playerCount, "默认在线人数应为 0")
        assertEquals(0.0, zero.tps, "默认 TPS 应为 0")
        assertEquals(0L, zero.memUsed, "默认已用堆应为 0")
        assertEquals(0L, zero.memMax, "默认最大堆应为 0")
        // CPU 不可用以 -1.0 表达「不可用」，与「0.0 表示真实空载」区分。
        assertEquals(RuntimeMetrics.CPU_UNAVAILABLE, zero.cpuLoad, "默认 cpuLoad 应为不可用哨兵 -1.0")
        assertEquals(-1.0, RuntimeMetrics.CPU_UNAVAILABLE, "不可用哨兵常量应为 -1.0")
    }

    @Test
    fun `构造指标按入参透传`() {
        val m =
            RuntimeMetrics(
                playerCount = 12,
                tps = 19.8,
                memUsed = 256L * 1024 * 1024,
                memMax = 1024L * 1024 * 1024,
                cpuLoad = 0.42,
            )
        assertEquals(12, m.playerCount)
        assertEquals(19.8, m.tps)
        assertEquals(256L * 1024 * 1024, m.memUsed)
        assertEquals(1024L * 1024 * 1024, m.memMax)
        assertEquals(0.42, m.cpuLoad)
    }

    @Test
    fun `JVM 内存采集得到非负且 used 不超 max`() {
        // 廉价 MXBean 调用：used 必非负，max 取到时 used 不应超过 max。
        val m = JvmRuntimeMetrics.sampleMemoryAndCpu()
        assertTrue(m.memUsed >= 0L, "已用堆应非负")
        assertTrue(m.memMax >= 0L, "最大堆应非负")
        if (m.memMax > 0L) {
            assertTrue(m.memUsed <= m.memMax, "已用堆不应超过最大堆")
        }
    }

    @Test
    fun `CPU 负载取到时落在 0 到 1 之间或为不可用哨兵`() {
        val cpu = JvmRuntimeMetrics.sampleMemoryAndCpu().cpuLoad
        // 不同 JDK / 容器下可能取不到（回退 -1.0），取到则须在 [0,1]。
        assertTrue(
            cpu == RuntimeMetrics.CPU_UNAVAILABLE || (cpu in 0.0..1.0),
            "cpuLoad 应为 -1.0（不可用）或落在 [0,1]，实得 $cpu",
        )
    }

    @Test
    fun `平台具备扩展接口时 CPU 负载应取到真值而非哨兵`() {
        // 本测试 JVM（JDK 9+ HotSpot 系）必有 com.sun.management 扩展接口；异构 JVM 取不到则放行（等价旧容忍语义）。
        val iface = runCatching { Class.forName("com.sun.management.OperatingSystemMXBean") }.getOrNull()
        val osBean = java.lang.management.ManagementFactory.getOperatingSystemMXBean()
        if (iface == null || !iface.isInstance(osBean)) return
        // getProcessCpuLoad 首采可能尚无窗口返回负值，小重试内必须出现真值；
        // 若恒为哨兵 = 反射路径被模块封装拦截（按实现类反射在 JDK 9+ 恒失败），即本测试要防的回归。
        var cpu = RuntimeMetrics.CPU_UNAVAILABLE
        var attempts = 0
        while (cpu < 0.0 && attempts < 20) {
            cpu = JvmRuntimeMetrics.sampleMemoryAndCpu().cpuLoad
            attempts++
            if (cpu < 0.0) Thread.sleep(100)
        }
        assertTrue(cpu in 0.0..1.0, "扩展接口可用时 cpuLoad 应取到真值，实得 $cpu（恒 -1 即反射被模块封装拦截）")
    }

    @Test
    fun `normalizeTps 归一化平台 TPS 采样`() {
        // 正常值原样保留。
        assertEquals(19.8, JvmRuntimeMetrics.normalizeTps(19.8))
        // 取不到（null）→ 0（Spigot 无 getTPS / 反射失败）。
        assertEquals(0.0, JvmRuntimeMetrics.normalizeTps(null))
        // 负值 / NaN → 0。
        assertEquals(0.0, JvmRuntimeMetrics.normalizeTps(-1.0))
        assertEquals(0.0, JvmRuntimeMetrics.normalizeTps(Double.NaN))
        // 略大于 20（Paper 偶发）→ 封顶 20。
        assertEquals(20.0, JvmRuntimeMetrics.normalizeTps(20.5))
    }

    @Test
    fun `estimateTps 由 tick 增量与墙钟间隔推算并封顶`() {
        val oneSecond = 1_000_000_000L
        // 1 秒内 20 tick → 20 TPS（满速）。
        assertEquals(20.0, JvmRuntimeMetrics.estimateTps(deltaTicks = 20L, elapsedNanos = oneSecond))
        // 1 秒内 10 tick（卡顿）→ 10 TPS。
        assertEquals(10.0, JvmRuntimeMetrics.estimateTps(deltaTicks = 10L, elapsedNanos = oneSecond))
        // 2 秒内 40 tick → 20 TPS（按墙钟间隔而非 tick 数）。
        assertEquals(20.0, JvmRuntimeMetrics.estimateTps(deltaTicks = 40L, elapsedNanos = 2 * oneSecond))
        // 超速采样（1 秒 25 tick，理论 25）→ 封顶 20。
        assertEquals(20.0, JvmRuntimeMetrics.estimateTps(deltaTicks = 25L, elapsedNanos = oneSecond))
        // 非法入参（elapsed ≤ 0 / delta < 0）→ 0。
        assertEquals(0.0, JvmRuntimeMetrics.estimateTps(deltaTicks = 20L, elapsedNanos = 0L))
        assertEquals(0.0, JvmRuntimeMetrics.estimateTps(deltaTicks = -1L, elapsedNanos = oneSecond))
    }

    @Test
    fun `withPlayerCountAndTps 在内存CPU基础上合入人数与TPS`() {
        // 壳层组装路径：先采内存/CPU（平台无关），再合入平台采到的人数/TPS。
        val base =
            RuntimeMetrics(
                playerCount = 0,
                tps = 0.0,
                memUsed = 100L,
                memMax = 200L,
                cpuLoad = 0.3,
            )
        val merged = base.withPlayerCountAndTps(playerCount = 7, tps = 20.0)
        assertEquals(7, merged.playerCount)
        assertEquals(20.0, merged.tps)
        // 内存/CPU 保持不变。
        assertEquals(100L, merged.memUsed)
        assertEquals(200L, merged.memMax)
        assertEquals(0.3, merged.cpuLoad)
    }
}

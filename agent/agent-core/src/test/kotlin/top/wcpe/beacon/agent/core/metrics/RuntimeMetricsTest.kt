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
        val m = RuntimeMetrics(
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
    fun `withPlayerCountAndTps 在内存CPU基础上合入人数与TPS`() {
        // 壳层组装路径：先采内存/CPU（平台无关），再合入平台采到的人数/TPS。
        val base = RuntimeMetrics(
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

package top.wcpe.beacon.agent.core.id

import java.util.UUID
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

/**
 * UUIDv7 生成与时间戳解析单测（RFC 9562，与控制面日表路由口径一致）。
 *
 * 覆盖：内嵌时间戳可原样解析、version=7、variant=0b10、同毫秒多次生成不重复、合法 UUID 可解析。
 */
class Uuid7Test {
    @Test
    fun `内嵌时间戳可原样解析`() {
        val now = 1_752_307_200_123L
        val id = Uuid7.generate(now)
        assertEquals(now, Uuid7.extractTimestampMs(id), "高 48 位时间戳须与生成时刻一致")
    }

    @Test
    fun `版本号为 7`() {
        val id = Uuid7.generate(1_000L)
        // UUID 字符串第 15 个字符（第三段首位）即 version 十六进制位。
        assertEquals('7', id[14], "version nibble 须为 7")
        assertEquals(7, UUID.fromString(id).version())
    }

    @Test
    fun `variant 为 RFC 4122 的 0b10`() {
        val id = Uuid7.generate(1_000L)
        assertEquals(2, UUID.fromString(id).variant(), "variant 须为 IETF 变体（2）")
    }

    @Test
    fun `同毫秒多次生成不重复`() {
        val now = 2_000_000L
        val ids = (1..1000).map { Uuid7.generate(now) }.toSet()
        assertEquals(1000, ids.size, "同毫秒内随机位应保证唯一")
    }

    @Test
    fun `生成结果为合法 UUID 字符串`() {
        val id = Uuid7.generate()
        // 不抛异常即合法。
        val parsed = UUID.fromString(id)
        assertNotEquals(0L, parsed.mostSignificantBits)
    }

    @Test
    fun `时间戳按毫秒单调递增可比较`() {
        val early = Uuid7.extractTimestampMs(Uuid7.generate(1_000L))
        val late = Uuid7.extractTimestampMs(Uuid7.generate(2_000L))
        assertTrue(late > early, "较晚生成的 UUIDv7 内嵌时间戳应更大")
    }
}

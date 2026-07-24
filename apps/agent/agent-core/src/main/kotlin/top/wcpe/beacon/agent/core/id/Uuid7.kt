package top.wcpe.beacon.agent.core.id

import java.util.UUID
import java.util.concurrent.ThreadLocalRandom

/**
 * UUIDv7 最小生成实现（RFC 9562）：高 48 位 = Unix 毫秒时间戳大端，供控制面据 ID 反推时间戳路由日表。
 *
 * agent 侧生成 conn_id / message_id（P5 连接明细与跨服消息主键）。不引第三方库、无可变全局状态：
 * 随机位取自 [ThreadLocalRandom]（线程本地、无共享），时间戳可注入以便测试。
 *
 * 位布局（128 位）：
 * - bit 0..47   ：unix_ts_ms（48 位，大端）
 * - bit 48..51  ：version = 0b0111（7）
 * - bit 52..63  ：rand_a（12 位随机）
 * - bit 64..65  ：variant = 0b10
 * - bit 66..127 ：rand_b（62 位随机）
 */
object Uuid7 {
    /** version 7 的 4 位版本号（左移到高 64 位的 bit 12..15）。 */
    private const val VERSION_BITS: Long = 0x7L shl 12

    /** variant 0b10 的 2 位（左移到低 64 位的 bit 62..63）。 */
    private const val VARIANT_BITS: Long = 0x2L shl 62

    /** 48 位时间戳掩码。 */
    private const val TS_MASK: Long = 0xFFFF_FFFF_FFFFL

    /** 12 位 rand_a 掩码。 */
    private const val RAND_A_MASK: Int = 0x0FFF

    /** 62 位 rand_b 掩码。 */
    private const val RAND_B_MASK: Long = 0x3FFF_FFFF_FFFF_FFFFL

    /**
     * 生成一个 UUIDv7 字符串。
     *
     * @param nowMs 生成时刻（Unix 毫秒，默认取系统时钟）；超出 48 位的高位被截断（正常时间范围内不发生）
     */
    fun generate(nowMs: Long = System.currentTimeMillis()): String {
        val random = ThreadLocalRandom.current()
        val ts = nowMs and TS_MASK
        val randA = (random.nextInt() and RAND_A_MASK).toLong()
        // 高 64 位：48 位时间戳 + 4 位版本 + 12 位随机。
        val msb = (ts shl 16) or VERSION_BITS or randA
        val randB = random.nextLong() and RAND_B_MASK
        // 低 64 位：2 位 variant + 62 位随机。
        val lsb = VARIANT_BITS or randB
        return UUID(msb, lsb).toString()
    }

    /**
     * 从 UUIDv7 字符串还原内嵌的 Unix 毫秒时间戳（高 48 位）。
     *
     * @throws IllegalArgumentException 字符串非合法 UUID
     */
    fun extractTimestampMs(uuid: String): Long {
        val parsed = UUID.fromString(uuid)
        return (parsed.mostSignificantBits ushr 16) and TS_MASK
    }
}

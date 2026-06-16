package top.wcpe.beacon.agent.core.settings

/**
 * 配置读取抽象：由壳层基于 TabooLib Configuration 适配实现，使 core 不依赖 TabooLib。
 *
 * 路径用点分（如 "identity.serverId"、"timing.pollTimeoutMs"）。
 */
interface ConfigReader {

    /** 读字符串，缺失返回默认值。 */
    fun string(path: String, default: String): String

    /** 读整数，缺失返回默认值。 */
    fun int(path: String, default: Int): Int

    /** 读长整数，缺失返回默认值。 */
    fun long(path: String, default: Long): Long

    /** 读浮点，缺失返回默认值。 */
    fun double(path: String, default: Double): Double

    /** 读布尔，缺失返回默认值。 */
    fun boolean(path: String, default: Boolean): Boolean

    /** 读字符串列表，缺失返回空列表。 */
    fun stringList(path: String): List<String>

    /** 列出某节点下的直接子键（用于读 metadata map）；节点不存在返回空。 */
    fun keys(path: String): Set<String>
}

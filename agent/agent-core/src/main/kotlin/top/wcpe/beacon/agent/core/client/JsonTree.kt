package top.wcpe.beacon.agent.core.client

/**
 * 从 JsonCodec 解码出的泛型树中安全读取字段的工具。
 *
 * 解码结果约定为 Map<String,Any?> / List<Any?> / String / Number / Boolean / null。
 */
internal object JsonTree {

    /** 将任意解码结果视作对象（Map）；非对象返回空 map。 */
    fun asObject(value: Any?): Map<String, Any?> {
        @Suppress("UNCHECKED_CAST")
        return (value as? Map<String, Any?>) ?: emptyMap()
    }

    /** 将任意值视作列表；非列表返回空列表。 */
    fun asList(value: Any?): List<Any?> {
        return (value as? List<*>) ?: emptyList<Any?>()
    }

    /** 将任意值视作字符串；非字符串返回空串（用于字符串数组元素，如成员 path 列表）。 */
    fun asString(value: Any?): String = (value as? String) ?: ""

    /** 读字符串字段；缺失或非字符串返回 null。 */
    fun str(obj: Map<String, Any?>, key: String): String? = obj[key] as? String

    /** 读字符串字段，缺失给默认值。 */
    fun strOr(obj: Map<String, Any?>, key: String, default: String): String = str(obj, key) ?: default

    /** 读整数字段；数值类型统一转 Int，缺失给默认值。 */
    fun intOr(obj: Map<String, Any?>, key: String, default: Int): Int {
        return when (val v = obj[key]) {
            is Number -> v.toInt()
            else -> default
        }
    }

    /** 读长整数字段；数值类型统一转 Long（保全 id 等大整数范围），缺失给默认值。 */
    fun longOr(obj: Map<String, Any?>, key: String, default: Long): Long {
        return when (val v = obj[key]) {
            is Number -> v.toLong()
            else -> default
        }
    }

    /** 读布尔字段，缺失给默认值。 */
    fun boolOr(obj: Map<String, Any?>, key: String, default: Boolean): Boolean {
        return obj[key] as? Boolean ?: default
    }
}

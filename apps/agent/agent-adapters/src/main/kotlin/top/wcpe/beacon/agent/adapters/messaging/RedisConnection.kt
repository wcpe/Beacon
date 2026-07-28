package top.wcpe.beacon.agent.adapters.messaging

import java.math.BigDecimal
import java.math.BigInteger

/**
 * Redis 连接参数（由 Beacon 配置中心下发，ADR-0016 决策 15）。
 *
 * 密码经 FR-20 配置加密、Beacon 解密后下发明文到内网可信 agent；本结构只持有运行期连接所需值，
 * 不负责加解密（加解密在 Beacon 侧）。
 *
 * @param host          Redis 主机（容器服务名或可路由 IP，非 localhost，ADR-0016 决策 10）
 * @param port          Redis 端口
 * @param database      逻辑库号
 * @param password      鉴权密码；空串 = 无密码（仅限内网临时验证）
 * @param connectTimeoutMs 连接 / Socket 超时（毫秒）
 */
data class RedisConnection(
    val host: String,
    val port: Int,
    val database: Int,
    val password: String,
    val connectTimeoutMs: Int,
) {
    override fun toString(): String =
        "RedisConnection(host=$host, port=$port, database=$database, password=***, connectTimeoutMs=$connectTimeoutMs)"

    companion object {
        private const val DEFAULT_PORT = 6379
        private const val DEFAULT_DATABASE = 0
        private const val MIN_PORT = 1
        private const val MAX_PORT = 65535
        private val minIntBigInteger = BigInteger.valueOf(Int.MIN_VALUE.toLong())
        private val maxIntBigInteger = BigInteger.valueOf(Int.MAX_VALUE.toLong())
        private val minIntBigDecimal = BigDecimal.valueOf(Int.MIN_VALUE.toLong())
        private val maxIntBigDecimal = BigDecimal.valueOf(Int.MAX_VALUE.toLong())

        /**
         * 从 Beacon 下发配置解析出的泛型树（Map）构造连接参数（纯逻辑，便于单测）。
         *
         * 期望键：host / port / db / password（password 可缺省）。缺 host 视为「配置未下发」返回 null，
         * 由壳层据此保持消息模块降级（决策 15：冷启动未取得配置时消息模块先关）。
         * 任何已提供但类型错误、非精确整数或超出范围的连接参数均返回 null。
         *
         * @param tree            解码后的配置树（JsonCodec.decode 结果）
         * @param connectTimeoutMs 连接超时（由壳层按本地 timing 传入）
         */
        fun fromTree(
            tree: Any?,
            connectTimeoutMs: Int,
        ): RedisConnection? {
            val map = tree as? Map<*, *> ?: return null
            val host = (map["host"] as? String)?.takeIf { it.isNotBlank() } ?: return null
            val port = map.exactIntOrDefault("port", DEFAULT_PORT)?.takeIf { it in MIN_PORT..MAX_PORT } ?: return null
            val database = map.exactIntOrDefault("db", DEFAULT_DATABASE)?.takeIf { it >= 0 } ?: return null
            val password = if (map.containsKey("password")) map["password"] as? String ?: return null else ""
            if (connectTimeoutMs <= 0) return null
            return RedisConnection(host, port, database, password, connectTimeoutMs)
        }

        private fun Map<*, *>.exactIntOrDefault(
            key: String,
            defaultValue: Int,
        ): Int? {
            if (!containsKey(key)) return defaultValue
            return (this[key] as? Number)?.toExactIntOrNull()
        }

        private fun Number.toExactIntOrNull(): Int? =
            when (this) {
                is Byte, is Short, is Int -> toInt()
                is Long -> takeIf { it in Int.MIN_VALUE.toLong()..Int.MAX_VALUE.toLong() }?.toInt()
                is BigInteger -> takeIf { it in minIntBigInteger..maxIntBigInteger }?.toInt()
                is BigDecimal -> toExactIntOrNull()
                is Float -> toDouble().toExactIntOrNull()
                is Double -> toExactIntOrNull()
                else -> null
            }

        private fun BigDecimal.toExactIntOrNull(): Int? {
            if (stripTrailingZeros().scale() > 0) return null
            return takeIf { it >= minIntBigDecimal && it <= maxIntBigDecimal }?.toInt()
        }

        private fun Double.toExactIntOrNull(): Int? {
            if (!isFinite() || this < Int.MIN_VALUE.toDouble() || this > Int.MAX_VALUE.toDouble()) return null
            return toInt().takeIf { it.toDouble() == this }
        }
    }
}

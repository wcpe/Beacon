package top.wcpe.beacon.agent.adapters.messaging

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

    companion object {

        /**
         * 从 Beacon 下发配置解析出的泛型树（Map）构造连接参数（纯逻辑，便于单测）。
         *
         * 期望键：host / port / db / password（password 可缺省）。缺 host 视为「配置未下发」返回 null，
         * 由壳层据此保持消息模块降级（决策 15：冷启动未取得配置时消息模块先关）。
         *
         * @param tree            解码后的配置树（JsonCodec.decode 结果）
         * @param connectTimeoutMs 连接超时（由壳层按本地 timing 传入）
         */
        fun fromTree(tree: Any?, connectTimeoutMs: Int): RedisConnection? {
            val map = tree as? Map<*, *> ?: return null
            val host = (map["host"] as? String)?.takeIf { it.isNotBlank() } ?: return null
            val port = (map["port"] as? Number)?.toInt() ?: 6379
            val database = (map["db"] as? Number)?.toInt() ?: 0
            val password = (map["password"] as? String) ?: ""
            return RedisConnection(
                host = host,
                port = port,
                database = database,
                password = password,
                connectTimeoutMs = connectTimeoutMs,
            )
        }
    }
}

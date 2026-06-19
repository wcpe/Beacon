package top.wcpe.beacon.agent.adapters.messaging

import redis.clients.jedis.JedisPool
import redis.clients.jedis.JedisPoolConfig

/**
 * 据 [RedisConnection] 构造 [JedisPool]（proxy 侧维护玩家名册等场景复用）。
 *
 * 抽出工厂避免在多处重复连接池构造细节；Jedis 类型仅在适配器出现（守不变量 #5）。
 */
object JedisPoolFactory {

    /** 构造一个小容量连接池。 */
    fun create(connection: RedisConnection): JedisPool {
        val config = JedisPoolConfig().apply {
            maxTotal = 8
            maxIdle = 4
            minIdle = 1
            testOnBorrow = true
        }
        val password = connection.password.takeIf { it.isNotBlank() }
        return JedisPool(
            config,
            connection.host,
            connection.port,
            connection.connectTimeoutMs,
            password,
            connection.database,
        )
    }
}

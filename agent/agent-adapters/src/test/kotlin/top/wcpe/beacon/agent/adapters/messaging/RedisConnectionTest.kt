package top.wcpe.beacon.agent.adapters.messaging

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** RedisConnection.fromTree 解析下发配置的纯逻辑单测。 */
class RedisConnectionTest {

    @Test
    fun `完整配置解析`() {
        val tree = mapOf("host" to "redis", "port" to 6380L, "db" to 2L, "password" to "secret")
        val conn = RedisConnection.fromTree(tree, connectTimeoutMs = 3000)
        assertEquals("redis", conn?.host)
        assertEquals(6380, conn?.port)
        assertEquals(2, conn?.database)
        assertEquals("secret", conn?.password)
        assertEquals(3000, conn?.connectTimeoutMs)
    }

    @Test
    fun `端口与库缺省取默认 密码缺省为空`() {
        val tree = mapOf("host" to "10.0.0.5")
        val conn = RedisConnection.fromTree(tree, connectTimeoutMs = 5000)
        assertEquals("10.0.0.5", conn?.host)
        assertEquals(6379, conn?.port)
        assertEquals(0, conn?.database)
        assertEquals("", conn?.password)
    }

    @Test
    fun `缺 host 视为未下发返回 null`() {
        assertNull(RedisConnection.fromTree(mapOf("port" to 6379L), connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree(mapOf("host" to ""), connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree(null, connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree("not-a-map", connectTimeoutMs = 5000))
    }
}

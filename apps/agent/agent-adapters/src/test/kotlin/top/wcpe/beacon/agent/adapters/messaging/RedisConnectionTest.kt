package top.wcpe.beacon.agent.adapters.messaging

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** RedisConnection.fromTree 解析下发配置的纯逻辑单测。 */
class RedisConnectionTest {
    @Test
    fun `完整配置解析`() {
        val password = "test-credential"
        val tree = mapOf("host" to "redis", "port" to 6380L, "db" to 2L, "password" to password)
        val conn = RedisConnection.fromTree(tree, connectTimeoutMs = 3000)
        assertEquals("redis", conn?.host)
        assertEquals(6380, conn?.port)
        assertEquals(2, conn?.database)
        assertTrue(conn?.password == password)
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
    fun `字符串表示固定脱敏密码且不泄漏真实值`() {
        val password = "credential-that-must-not-appear"
        val connection =
            RedisConnection(
                host = "redis.internal",
                port = 6380,
                database = 2,
                password = password,
                connectTimeoutMs = 3000,
            )

        val rendered = connection.toString()

        assertFalse(rendered.contains(password))
        assertEquals(
            "RedisConnection(host=redis.internal, port=6380, database=2, password=***, connectTimeoutMs=3000)",
            rendered.replace(password, "[测试凭据]"),
        )
    }

    @Test
    fun `脱敏字符串不改变连接值相等语义`() {
        val connection = RedisConnection("redis", 6379, 0, "credential-a", 3000)

        assertEquals(connection, connection.copy())
        assertNotEquals(connection, connection.copy(password = "credential-b"))
        assertNotEquals(connection, connection.copy(port = 6380))
    }

    @Test
    fun `password 键存在但不是字符串时拒绝配置`() {
        assertNull(connection(mapOf("password" to 1L)))
        assertNull(connection(mapOf("password" to null)))
    }

    @Test
    fun `端口必须是范围内的精确整数`() {
        assertEquals(1, connection(mapOf("port" to 1L))?.port)
        assertEquals(65535, connection(mapOf("port" to 65535.0))?.port)
        assertNull(connection(mapOf("port" to 0L)))
        assertNull(connection(mapOf("port" to 65536L)))
        assertNull(connection(mapOf("port" to 6379.5)))
        assertNull(connection(mapOf("port" to "6379")))
        assertNull(connection(mapOf("port" to null)))
        assertNull(connection(mapOf("port" to Long.MAX_VALUE)))
        assertNull(connection(mapOf("port" to Double.NaN)))
        assertNull(connection(mapOf("port" to Double.POSITIVE_INFINITY)))
        assertNull(connection(mapOf("port" to Double.NEGATIVE_INFINITY)))
    }

    @Test
    fun `数据库编号必须是非负精确整数`() {
        assertEquals(0, connection(mapOf("db" to 0L))?.database)
        assertEquals(Int.MAX_VALUE, connection(mapOf("db" to Int.MAX_VALUE.toLong()))?.database)
        assertNull(connection(mapOf("db" to -1L)))
        assertNull(connection(mapOf("db" to 1.5)))
        assertNull(connection(mapOf("db" to "1")))
        assertNull(connection(mapOf("db" to null)))
        assertNull(connection(mapOf("db" to Long.MAX_VALUE)))
        assertNull(connection(mapOf("db" to Double.NaN)))
        assertNull(connection(mapOf("db" to Double.POSITIVE_INFINITY)))
        assertNull(connection(mapOf("db" to Double.NEGATIVE_INFINITY)))
    }

    @Test
    fun `未知 Number 即使字符串是合法整数也必须拒绝`() {
        assertNull(connection(mapOf("port" to CustomNumber { "6379" })))
    }

    @Test
    fun `未知 Number 的字符串转换异常不得泄漏`() {
        assertNull(connection(mapOf("port" to CustomNumber { error("不应调用未知 Number.toString") })))
    }

    @Test
    fun `连接超时必须为正整数`() {
        assertEquals(1, connection(connectTimeoutMs = 1)?.connectTimeoutMs)
        assertEquals(Int.MAX_VALUE, connection(connectTimeoutMs = Int.MAX_VALUE)?.connectTimeoutMs)
        assertNull(connection(connectTimeoutMs = 0))
        assertNull(connection(connectTimeoutMs = -1))
    }

    @Test
    fun `缺 host 视为未下发返回 null`() {
        assertNull(RedisConnection.fromTree(mapOf("port" to 6379L), connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree(mapOf("host" to ""), connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree(null, connectTimeoutMs = 5000))
        assertNull(RedisConnection.fromTree("not-a-map", connectTimeoutMs = 5000))
    }

    private fun connection(
        overrides: Map<String, Any?> = emptyMap(),
        connectTimeoutMs: Int = 5000,
    ): RedisConnection? = RedisConnection.fromTree(mapOf("host" to "redis") + overrides, connectTimeoutMs)

    private class CustomNumber(
        private val render: () -> String,
    ) : Number() {
        override fun toByte(): Byte = 0

        override fun toDouble(): Double = 0.0

        override fun toFloat(): Float = 0.0f

        override fun toInt(): Int = 0

        override fun toLong(): Long = 0L

        override fun toShort(): Short = 0

        override fun toString(): String = render()
    }
}

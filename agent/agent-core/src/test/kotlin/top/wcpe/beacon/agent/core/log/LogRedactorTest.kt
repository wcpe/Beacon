package top.wcpe.beacon.agent.core.log

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 日志脱敏纯函数 [LogRedactor] 穷举单测（FR-88，见 ADR-0040）：
 * - 常见敏感键（token/password/secret/authorization/bootstrap-token）的值掩码；
 * - 形似密钥的长串掩码；
 * - 普通日志不被误伤；
 * - 大小写 / 分隔符（= 或 :）变体均覆盖。
 */
class LogRedactorTest {

    @Test
    fun `token 键值被掩码`() {
        val out = LogRedactor.redact("注册请求头 X-Beacon-Token=abcdef123456 已发送")
        assertTrue(!out.contains("abcdef123456"), "token 值不应保留：$out")
        assertTrue(out.contains("***"), "应有掩码：$out")
    }

    @Test
    fun `password 与 secret 键值被掩码`() {
        assertTrue(!LogRedactor.redact("password: hunter2pass").contains("hunter2pass"))
        assertTrue(!LogRedactor.redact("db.secret = myDbSecretValue").contains("myDbSecretValue"))
    }

    @Test
    fun `authorization 头值被掩码`() {
        val out = LogRedactor.redact("Authorization: Bearer eyJhbGciOiJIUzI1NiwidHlwIjoiSldUIn0")
        assertTrue(!out.contains("eyJhbGciOiJIUzI1NiwidHlwIjoiSldUIn0"), "JWT 不应保留：$out")
    }

    @Test
    fun `bootstrap-token 键值被掩码`() {
        val out = LogRedactor.redact("bootstrap-token=verylongsecrettokenvalue99887766")
        assertTrue(!out.contains("verylongsecrettokenvalue99887766"), "$out")
    }

    @Test
    fun `大小写不敏感`() {
        assertTrue(!LogRedactor.redact("TOKEN=ABCDEF123456").contains("ABCDEF123456"))
        assertTrue(!LogRedactor.redact("PassWord=SomePass123").contains("SomePass123"))
    }

    @Test
    fun `普通日志不被误伤`() {
        val msg = "已应用有效配置 md5=abc，人数=12，TPS=19.8"
        // md5 / 人数 / TPS 是正常运维事实，不属敏感键，不应被改写。
        assertEquals(msg, LogRedactor.redact(msg))
    }

    @Test
    fun `普通短词不被当作密钥误掩`() {
        val msg = "连接 lobby-1 成功，group=area1"
        assertEquals(msg, LogRedactor.redact(msg))
    }
}

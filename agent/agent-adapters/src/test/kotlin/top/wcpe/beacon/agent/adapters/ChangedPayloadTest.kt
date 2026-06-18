package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.core.stream.ChangedPayload
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * ChangedPayload 用真实 KotlinxJsonCodec 解析 *-changed 事件 data 行的新 md5。
 */
class ChangedPayloadTest {

    private val codec = KotlinxJsonCodec()

    @Test
    fun `解析合法载荷取出新 md5`() {
        assertEquals("abc123", ChangedPayload.md5Of(codec, """{"md5":"abc123"}"""))
    }

    @Test
    fun `缺 md5 字段返回空串`() {
        assertEquals("", ChangedPayload.md5Of(codec, """{"other":"x"}"""))
    }

    @Test
    fun `空 data 返回空串`() {
        assertEquals("", ChangedPayload.md5Of(codec, ""))
    }

    @Test
    fun `非法 JSON 返回空串不抛异常`() {
        assertEquals("", ChangedPayload.md5Of(codec, "not-json"))
    }
}

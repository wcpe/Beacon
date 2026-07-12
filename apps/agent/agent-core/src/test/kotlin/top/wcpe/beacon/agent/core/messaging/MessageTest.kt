package top.wcpe.beacon.agent.core.messaging

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** 信封序列化与还原的纯逻辑单测（不依赖 transport / codec 实现）。 */
class MessageTest {
    @Test
    fun `toMap 含必填字段且省略空可选字段`() {
        val message = Message(type = "ping", payload = mapOf("k" to "v"))
        val map = message.toMap()

        assertEquals("ping", map[Message.FIELD_TYPE])
        assertEquals(Message.CURRENT_VERSION, map[Message.FIELD_VERSION])
        assertEquals(mapOf("k" to "v"), map[Message.FIELD_PAYLOAD])
        // 非 RPC：correlationId/replyTo/source 不出现（只增不改的宽进）。
        assertFalse(map.containsKey(Message.FIELD_CORRELATION_ID))
        assertFalse(map.containsKey(Message.FIELD_REPLY_TO))
        assertFalse(map.containsKey(Message.FIELD_SOURCE))
    }

    @Test
    fun `RPC 请求带 correlationId 与 replyTo`() {
        val message =
            Message(
                type = "q",
                payload = null,
                correlationId = "cid-1",
                replyTo = "reply:A",
                source = "A",
            )
        assertTrue(message.isRequest())
        val map = message.toMap()
        assertEquals("cid-1", map[Message.FIELD_CORRELATION_ID])
        assertEquals("reply:A", map[Message.FIELD_REPLY_TO])
        assertEquals("A", map[Message.FIELD_SOURCE])
    }

    @Test
    fun `fromMap 往返一致`() {
        val original =
            Message(
                type = "q",
                payload = listOf(1L, "x", true),
                correlationId = "cid-2",
                replyTo = "reply:B",
                source = "B",
            )
        val restored = Message.fromMap(original.toMap())
        assertEquals(original, restored)
    }

    @Test
    fun `fromMap 缺 type 返回 null`() {
        val tree = mapOf(Message.FIELD_VERSION to 1, Message.FIELD_PAYLOAD to "x")
        assertNull(Message.fromMap(tree))
    }

    @Test
    fun `fromMap 非 Map 返回 null`() {
        assertNull(Message.fromMap("not-a-map"))
        assertNull(Message.fromMap(null))
    }

    @Test
    fun `fromMap 缺 version 兜底为当前版本`() {
        val tree = mapOf(Message.FIELD_TYPE to "t", Message.FIELD_PAYLOAD to 1L)
        val restored = Message.fromMap(tree)
        assertEquals(Message.CURRENT_VERSION, restored?.version)
    }

    @Test
    fun `isRequest 以带 correlationId 为判据`() {
        // HTTP 中转下 replyTo 不随控制面转发投递，改以 correlationId 判定「期待回信的 RPC 请求」。
        assertTrue(Message(type = "t", payload = null, correlationId = "c").isRequest())
        assertTrue(Message(type = "t", payload = null, correlationId = "c", replyTo = "r").isRequest())
        assertFalse(Message(type = "t", payload = null).isRequest())
        assertFalse(Message(type = "t", payload = null, replyTo = "r").isRequest())
    }

    @Test
    fun `messageId 与 sentAt 及目标字段 toMap 只增可往返`() {
        val message =
            Message(
                type = "evt",
                payload = mapOf("k" to "v"),
                source = "A",
                messageId = "0190a1b2-c3d4-7000-8000-000000000000",
                sentAt = 1_752_307_200_123L,
                targetKind = Message.TARGET_SERVER,
                targetId = "B",
            )
        val map = message.toMap()
        assertEquals("0190a1b2-c3d4-7000-8000-000000000000", map[Message.FIELD_MESSAGE_ID])
        assertEquals(1_752_307_200_123L, map[Message.FIELD_SENT_AT])
        assertEquals("server", map[Message.FIELD_TARGET_KIND])
        assertEquals("B", map[Message.FIELD_TARGET_ID])
        assertEquals(message, Message.fromMap(map))
    }

    @Test
    fun `toMap 省略空的新增字段`() {
        val map = Message(type = "t", payload = null).toMap()
        assertFalse(map.containsKey(Message.FIELD_MESSAGE_ID))
        assertFalse(map.containsKey(Message.FIELD_SENT_AT))
        assertFalse(map.containsKey(Message.FIELD_TARGET_KIND))
        assertFalse(map.containsKey(Message.FIELD_TARGET_ID))
    }
}

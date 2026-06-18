package top.wcpe.beacon.agent.core.stream

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * SseFrameParser 纯逻辑单测：按空行分帧、注释行（保活心跳）忽略、event/data 字段解析。
 */
class SseFrameParserTest {

    @Test
    fun `空行前未凑齐不产出事件`() {
        val p = SseFrameParser()
        assertNull(p.feed("event: config-changed"), "仅 event 行不应产出")
        assertNull(p.feed("data: {\"md5\":\"abc\"}"), "data 行不应产出")
    }

    @Test
    fun `空行凑齐一帧产出事件`() {
        val p = SseFrameParser()
        p.feed("event: config-changed")
        p.feed("data: {\"md5\":\"abc\"}")
        val e = p.feed("")
        assertEquals("config-changed", e?.type)
        assertEquals("{\"md5\":\"abc\"}", e?.data)
    }

    @Test
    fun `注释行被忽略不影响当前帧`() {
        val p = SseFrameParser()
        assertNull(p.feed(": ping"), "保活注释行不应产出事件")
        p.feed("event: ready")
        val e = p.feed("")
        assertEquals("ready", e?.type, "注释行后仍能正常解析后续帧")
    }

    @Test
    fun `连续多帧各自独立产出`() {
        val p = SseFrameParser()
        p.feed("event: config-changed")
        p.feed("data: {\"md5\":\"c1\"}")
        val first = p.feed("")
        assertEquals("config-changed", first?.type)

        p.feed("event: file-changed")
        p.feed("data: {\"md5\":\"f1\"}")
        val second = p.feed("")
        assertEquals("file-changed", second?.type)
        assertEquals("{\"md5\":\"f1\"}", second?.data)
    }

    @Test
    fun `纯空行不产出事件`() {
        val p = SseFrameParser()
        assertNull(p.feed(""), "无累积态的空行不应产出")
    }
}

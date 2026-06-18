package top.wcpe.beacon.agent.core.stream

import top.wcpe.beacon.agent.core.transport.StreamEvent

/**
 * SSE 帧解析器（纯逻辑，无 IO）：逐行喂入，按空行分隔成帧，凑齐一帧即产出一条 [StreamEvent]。
 *
 * 放 core 而非适配器，使"如何把字节流切成事件"可被穷举单测；适配器只负责读字节流逐行喂入。
 *
 * 规则（SSE 子集，够用即可）：
 * - `event: <type>` 行设置事件类型；
 * - `data: <text>` 行设置数据（本场景单行 data，不做多行拼接）；
 * - 注释行（`:` 开头，如保活心跳 `: ping`）整行忽略；
 * - 空行表示一帧结束：有 type 才产出事件，随即重置累积态。
 */
class SseFrameParser {

    private var type: String? = null
    private var data: String = ""

    /**
     * 喂入一行（不含换行符）。凑齐一帧（遇空行）返回该事件，否则返回 null。
     */
    fun feed(line: String): StreamEvent? {
        // 空行：帧结束。
        if (line.isEmpty()) {
            val t = type
            val event = if (t != null) StreamEvent(type = t, data = data) else null
            reset()
            return event
        }
        // 注释行（含保活心跳）：忽略，不影响当前帧累积。
        if (line.startsWith(":")) {
            return null
        }
        when {
            line.startsWith("event:") -> type = line.removePrefix("event:").trim()
            line.startsWith("data:") -> data = line.removePrefix("data:").trim()
            // 其它字段（id/retry 等）本场景不用，忽略。
        }
        return null
    }

    private fun reset() {
        type = null
        data = ""
    }
}

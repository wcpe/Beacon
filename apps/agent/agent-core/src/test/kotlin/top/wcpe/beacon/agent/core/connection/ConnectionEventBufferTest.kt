package top.wcpe.beacon.agent.core.connection

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 连接事件有界缓冲 [ConnectionEventBuffer] 单测（FR-145 §4.1/§4.5，高风险区穷举）。
 *
 * 覆盖：写满覆盖最旧 + droppedCount、单批上限截取、ack 按 seq 精确移除前缀、
 * 上报在途覆盖丢弃不误删、droppedSinceLast 随成功批扣减且残余结转、满阈值触发标志。
 */
class ConnectionEventBufferTest {
    private fun openEvent(connId: String): ConnectionEvent =
        ConnectionEvent(
            kind = ConnectionEventKind.OPEN,
            connId = connId,
            playerUuid = "u-$connId",
            playerName = "p",
            clientIp = null,
            protocolVersion = null,
            openedAtMs = 1000L,
            closedAtMs = null,
            closeKind = null,
            closeReason = null,
            firstBackend = null,
            lastBackend = null,
            backendSwitchCount = null,
        )

    @Test
    fun `写满覆盖最旧并累加丢弃计数`() {
        val buf = ConnectionEventBuffer(capacity = 3)
        for (i in 1..5) buf.add(openEvent("c$i"))
        assertEquals(3, buf.size(), "容量满后应稳定在容量大小")
        assertEquals(2L, buf.droppedCount(), "应累加被覆盖丢弃的事件数")
    }

    @Test
    fun `add 达到阈值返回触发标志`() {
        val buf = ConnectionEventBuffer(capacity = 10_000)
        // FLUSH_THRESHOLD=200：第 199 条前不触发，第 200 条触发。
        for (i in 1 until ConnectionEventBuffer.FLUSH_THRESHOLD) assertFalse(buf.add(openEvent("c$i")))
        assertTrue(buf.add(openEvent("c-threshold")), "达到 200 条应返回触发即时上报标志")
    }

    @Test
    fun `peek 取最旧至多 max 条不移除`() {
        val buf = ConnectionEventBuffer(capacity = 100)
        for (i in 1..5) buf.add(openEvent("c$i"))
        val snap = buf.peek(max = 3)
        assertEquals(3, snap.events.size)
        assertEquals("c1", snap.events.first().connId, "应取最旧")
        assertEquals(5, buf.size(), "peek 不移除")
    }

    @Test
    fun `ack 按 seq 移除已上报前缀`() {
        val buf = ConnectionEventBuffer(capacity = 100)
        for (i in 1..4) buf.add(openEvent("c$i"))
        val snap = buf.peek(max = 2) // c1,c2；lastSeq=1
        buf.ack(snap.lastSeq, snap.droppedSinceLast)
        assertEquals(2, buf.size())
        assertEquals("c3", buf.peek(max = 1).events.first().connId, "移除已上报前缀后余最旧为 c3")
    }

    @Test
    fun `上报在途覆盖丢弃不误删更新的事件`() {
        val buf = ConnectionEventBuffer(capacity = 3)
        buf.add(openEvent("c1"))
        buf.add(openEvent("c2"))
        val snap = buf.peek() // c1,c2；lastSeq=1
        // 上报在途：新事件把最旧挤掉。
        buf.add(openEvent("c3"))
        buf.add(openEvent("c4")) // 覆盖 c1
        buf.add(openEvent("c5")) // 覆盖 c2
        // ack 只移除仍在缓冲且 seq<=lastSeq 者（c1/c2 已被覆盖不在缓冲），不误删 c3/c4/c5。
        buf.ack(snap.lastSeq, snap.droppedSinceLast)
        assertEquals(3, buf.size(), "不应误删上报后新入的事件")
        assertEquals("c3", buf.peek(max = 1).events.first().connId)
    }

    @Test
    fun `droppedSinceLast 随成功批扣减且上报在途新增残余结转`() {
        val buf = ConnectionEventBuffer(capacity = 2)
        buf.add(openEvent("c1"))
        buf.add(openEvent("c2"))
        buf.add(openEvent("c3")) // 覆盖 c1，dropped=1
        val snap = buf.peek() // 携 droppedSinceLast=1
        assertEquals(1L, snap.droppedSinceLast)
        // 上报在途又覆盖一条（dropped 累到 2）。
        buf.add(openEvent("c4")) // 覆盖 c2，dropped=2
        buf.ack(snap.lastSeq, snap.droppedSinceLast) // 扣减本批已报的 1，残 1 结转
        assertEquals(1L, buf.droppedCount(), "已报丢弃扣减、在途新增残余结转下批")
    }
}

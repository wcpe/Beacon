package top.wcpe.beacon.agent.core.connection

import top.wcpe.beacon.agent.core.id.Uuid7
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 玩家连接会话追踪器 [ProxyConnectionTracker] 单测（FR-145 §4.1）。
 *
 * 覆盖：open 生成 connId（内嵌时间戳）+ 发 open 事件、后端首连与换服累加摘要、close 复用同 connId
 * 携时长/closeKind/首末后端/切换数、会话移除、多玩家独立、无会话的 close/backend 忽略。
 */
class ProxyConnectionTrackerTest {
    private val events = mutableListOf<ConnectionEvent>()
    private var clock = 1_000_000L
    private val tracker = ProxyConnectionTracker(sink = events::add, now = { clock })

    @Test
    fun `登入发 open 事件且 connId 内嵌时间戳`() {
        tracker.onConnect("u1", "Steve", "1.2.3.4", 763)
        assertEquals(1, events.size)
        val open = events.single()
        assertEquals(ConnectionEventKind.OPEN, open.kind)
        assertEquals("Steve", open.playerName)
        assertEquals("1.2.3.4", open.clientIp)
        assertEquals(763, open.protocolVersion)
        assertEquals(clock, open.openedAtMs)
        assertEquals(clock, Uuid7.extractTimestampMs(open.connId), "connId 应内嵌登入时刻")
        assertNull(open.closedAtMs)
        assertNull(open.backendSwitchCount)
    }

    @Test
    fun `后端首连与换服累加摘要 close 事件携首末后端与切换数`() {
        tracker.onConnect("u1", "Steve", null, null)
        val connId = events.single().connId
        tracker.onBackend("u1", "lobby-1") // 首连：first=last=lobby-1，switch=0
        tracker.onBackend("u1", "lobby-1") // 同服重复：不计切换
        tracker.onBackend("u1", "game-7") // 换服：last=game-7，switch=1
        tracker.onBackend("u1", "game-9") // 换服：last=game-9，switch=2

        clock += 5_000L
        tracker.onDisconnect("u1", "quit", "客户端断开")

        val close = events.last()
        assertEquals(ConnectionEventKind.CLOSE, close.kind)
        assertEquals(connId, close.connId, "close 应复用 open 的 connId")
        assertEquals("lobby-1", close.firstBackend)
        assertEquals("game-9", close.lastBackend)
        assertEquals(2, close.backendSwitchCount)
        assertEquals("quit", close.closeKind)
        assertEquals("客户端断开", close.closeReason)
        assertEquals(clock, close.closedAtMs)
        assertEquals(0, tracker.activeSessions(), "close 后会话应移除")
    }

    @Test
    fun `多玩家会话相互独立`() {
        tracker.onConnect("u1", "A", null, null)
        tracker.onConnect("u2", "B", null, null)
        tracker.onBackend("u1", "lobby-1")
        tracker.onBackend("u2", "lobby-2")
        assertEquals(2, tracker.activeSessions())

        tracker.onDisconnect("u1", "quit", null)
        val closeA = events.last()
        assertEquals("A", closeA.playerName)
        assertEquals("lobby-1", closeA.lastBackend)
        assertEquals(1, tracker.activeSessions(), "u2 会话不受 u1 断开影响")
    }

    @Test
    fun `无对应会话的 close 与 backend 被忽略`() {
        tracker.onDisconnect("ghost", "quit", null)
        tracker.onBackend("ghost", "lobby-1")
        assertTrue(events.isEmpty(), "无会话不产生事件")
    }
}

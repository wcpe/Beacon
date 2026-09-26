package top.wcpe.beacon.agent.core.connection

import top.wcpe.beacon.agent.core.id.Uuid7
import java.util.concurrent.ConcurrentHashMap

/**
 * 玩家连接会话追踪器（proxy 侧，FR-145 §4.1）：把「登入 / 后端切换 / 登出」平台事件映射为 open/close 事件。
 *
 * 会话行语义：登入时生成 connId（UUIDv7）并发 open 事件、缓存会话；后端连接 / 切换时**补发一条 open**
 * 携当前所在服（控制面按 connId 更新位置列，控制面名册据此得知「玩家此刻在哪」）；登出时用同 connId 发
 * close 事件（携时长/closeKind/首末后端/切换数）。
 *
 * core 纯逻辑、无 IO、无平台依赖：事件经 [sink] 交有界缓冲（[ConnectionEventBuffer.add]）。
 * 埋点零成本（map + UUID 生成，无阻塞），可在 BC 事件线程直接调用，绝不阻塞 MC/BC 主线程。
 *
 * @param sink 事件汇（通常为 [ConnectionEventBuffer.add]）
 * @param now  时钟（默认系统时钟，测试可注入）
 */
class ProxyConnectionTracker(
    private val sink: (ConnectionEvent) -> Unit,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    /** 会话内累积状态（首末后端 + 切换次数按 backend 事件更新）。 */
    private data class Session(
        val connId: String,
        val playerName: String,
        val clientIp: String?,
        val protocolVersion: Int?,
        val openedAtMs: Long,
        var firstBackend: String?,
        var lastBackend: String?,
        var switchCount: Int,
    )

    /** playerUuid → 当前会话。 */
    private val sessions = ConcurrentHashMap<String, Session>()

    /** 玩家登入代理：生成 connId、缓存会话、发 open 事件。重复登入（未收到 close）以新会话覆盖旧的。 */
    fun onConnect(
        playerUuid: String,
        playerName: String,
        clientIp: String?,
        protocolVersion: Int?,
    ) {
        val openedAtMs = now()
        val connId = Uuid7.generate(openedAtMs)
        sessions[playerUuid] =
            Session(
                connId = connId,
                playerName = playerName,
                clientIp = clientIp,
                protocolVersion = protocolVersion,
                openedAtMs = openedAtMs,
                firstBackend = null,
                lastBackend = null,
                switchCount = 0,
            )
        sink(
            ConnectionEvent(
                kind = ConnectionEventKind.OPEN,
                connId = connId,
                playerUuid = playerUuid,
                playerName = playerName,
                clientIp = clientIp,
                protocolVersion = protocolVersion,
                openedAtMs = openedAtMs,
                closedAtMs = null,
                closeKind = null,
                closeReason = null,
                firstBackend = null,
                lastBackend = null,
                backendSwitchCount = null,
            ),
        )
    }

    /**
     * 玩家连接到某后端子服（首次进服与换服都触发）：累加会话摘要，并**补发一条 open 事件**携当前所在服。
     *
     * 为什么补发：控制面名册要答「玩家此刻在哪」，而 open（登录时发）尚不知后端、close 要等断线，
     * 后端切换本身又不发独立事件（见 v2-connection-message-storage spec §4.1 的会话行取舍）。
     * 缺这条补发时名册只能回退到代理，按玩家寻址的消息会被投到代理、因无对应 handler 而失败（真机暴露）。
     *
     * 复用 open 事件而非新增 kind：wire 的 kind 枚举是冻结契约（spec §4.1 只允许 open|close），
     * 而 open 行的位置列本就存在（first/last_backend），冲突时按位置列更新即可（见控制面 insertConnOpens）。
     */
    fun onBackend(
        playerUuid: String,
        backendServerId: String,
    ) {
        val session = sessions[playerUuid] ?: return
        synchronized(session) {
            if (session.firstBackend == null) {
                session.firstBackend = backendServerId
                session.lastBackend = backendServerId
            } else if (session.lastBackend != backendServerId) {
                session.lastBackend = backendServerId
                session.switchCount++
            }
        }
        // 补发 open 携当前位置（同 connId；控制面按 connId 更新位置列，不新增会话行）
        sink(
            ConnectionEvent(
                kind = ConnectionEventKind.OPEN,
                connId = session.connId,
                playerUuid = playerUuid,
                playerName = session.playerName,
                clientIp = session.clientIp,
                protocolVersion = session.protocolVersion,
                openedAtMs = session.openedAtMs,
                closedAtMs = null,
                closeKind = null,
                closeReason = null,
                firstBackend = session.firstBackend,
                lastBackend = backendServerId,
                backendSwitchCount = null,
            ),
        )
    }

    /** 玩家登出：用同 connId 发 close 事件（携时长/closeKind/首末后端/切换数），移除会话。无对应会话则忽略。 */
    fun onDisconnect(
        playerUuid: String,
        closeKind: String,
        closeReason: String?,
    ) {
        val session = sessions.remove(playerUuid) ?: return
        val closedAtMs = now()
        val event =
            synchronized(session) {
                ConnectionEvent(
                    kind = ConnectionEventKind.CLOSE,
                    connId = session.connId,
                    playerUuid = playerUuid,
                    playerName = session.playerName,
                    clientIp = session.clientIp,
                    protocolVersion = session.protocolVersion,
                    openedAtMs = session.openedAtMs,
                    closedAtMs = closedAtMs,
                    closeKind = closeKind,
                    closeReason = closeReason,
                    firstBackend = session.firstBackend,
                    lastBackend = session.lastBackend,
                    backendSwitchCount = session.switchCount,
                )
            }
        sink(event)
    }

    /** 当前在册会话数（测试 / 观测用）。 */
    fun activeSessions(): Int = sessions.size
}

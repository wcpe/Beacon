package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.ServerConnectEvent
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.bungee.support.FakePlayer
import top.wcpe.beacon.agent.bungee.support.FakeServer
import top.wcpe.beacon.agent.bungee.support.FakeServerInfo
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.proxy.InitialLobbyRouter
import top.wcpe.beacon.agent.core.proxy.ManagedDirectorySnapshot
import top.wcpe.beacon.agent.core.proxy.ManagedLobbySnapshot
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertSame
import kotlin.test.assertTrue

/**
 * 首次入服大厅路由的事件层单测：[BungeeInitialLobbyListener] 对 `ServerConnectEvent` 的处理。
 *
 * 选路算法本身（候选筛选、就绪态、大厅成员判定、同分随机）由 agent-core 的
 * `InitialLobbyRouterTest` 覆盖，这里不重复；本类只盯三件平台相关的事：
 *
 * 1. **只接管首次入服**：`Reason.JOIN_PROXY` 且玩家当前无归属，其余一律放过——
 *    这是该能力「不干预任何后续业务转服」的边界所在；
 * 2. **命中时改写 `target` 且不取消**；
 * 3. **无可用大厅时取消事件并断开玩家**，理由文案必须稳定（玩家看得见）。
 *
 * 平台对象用 `support/` 下的手写替身构造，事件用 BungeeCord 真实类，不引入 mock 框架与反射。
 * 监听器是可变全局单例，故每个用例都在 `finally` 里 [BungeeInitialLobbyListener.stop]。
 */
class BungeeInitialLobbyListenerTest {
    @Test
    fun `首次入服命中时改写到受管大厅`() {
        val lobby = FakeServerInfo("lobby-1")
        val native = FakeServerInfo("fallback")
        val player = playerOn(null)
        val event = connectEvent(player, native, ServerConnectEvent.Reason.JOIN_PROXY)
        startListener(routerSelecting("lobby-1"), ManagedServerLookup { if (it == "lobby-1") lobby else null })
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertSame(lobby, event.target, "首次入服应改写为受管目录返回的 ServerInfo 实例")
            assertFalse(event.isCancelled, "命中时不得取消连接")
            assertTrue(player.disconnectReasons.isEmpty(), "命中时不得断开玩家")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    @Test
    fun `玩家已有归属时不改写`() {
        // 已经有服的玩家是在转服，不是首次入服——插手会抢走业务插件或玩家自己的选择。
        val native = FakeServerInfo("fallback")
        val player = playerOn("game-1")
        val event = connectEvent(player, native, ServerConnectEvent.Reason.JOIN_PROXY)
        startListener(routerSelecting("lobby-1"), ManagedServerLookup { FakeServerInfo("lobby-1") })
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertSame(native, event.target, "非首次入服不得改写目标")
            assertFalse(event.isCancelled, "非首次入服不得取消连接")
            assertTrue(player.disconnectReasons.isEmpty(), "非首次入服不得断开玩家")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    @Test
    fun `非首次入服原因时不改写`() {
        // COMMAND 是玩家主动 /server，属于显式意图。
        val native = FakeServerInfo("fallback")
        val player = playerOn(null)
        val event = connectEvent(player, native, ServerConnectEvent.Reason.COMMAND)
        startListener(routerSelecting("lobby-1"), ManagedServerLookup { FakeServerInfo("lobby-1") })
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertSame(native, event.target, "非 JOIN_PROXY 不得改写目标")
            assertFalse(event.isCancelled, "非 JOIN_PROXY 不得取消连接")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    @Test
    fun `选中目标不在受管目录时取消并断开`() {
        // 选路命中了大厅，但目录里还没注入它（discovery 尚未到达）→ 宁可明确拒绝，也不把玩家送进空地址。
        val player = playerOn(null)
        val event = connectEvent(player, FakeServerInfo("fallback"), ServerConnectEvent.Reason.JOIN_PROXY)
        startListener(routerSelecting("lobby-1"), ManagedServerLookup { null })
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertTrue(event.isCancelled, "取不到 ServerInfo 时必须取消本次连接")
            assertEquals(listOf(NO_LOBBY_MESSAGE), player.disconnectReasons, "必须以稳定文案断开玩家")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    @Test
    fun `选路明确拒绝时取消并断开`() {
        val player = playerOn(null)
        val event = connectEvent(player, FakeServerInfo("fallback"), ServerConnectEvent.Reason.JOIN_PROXY)
        startListener(routerRejecting(), ManagedServerLookup { FakeServerInfo("lobby-1") })
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertTrue(event.isCancelled, "无可用大厅时必须取消本次连接")
            assertEquals(listOf(NO_LOBBY_MESSAGE), player.disconnectReasons, "必须以稳定文案断开玩家")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    @Test
    fun `未装配时不改写也不断开`() {
        // 单例上下文里别的用例可能已装配过，先显式回到未装配态再验短路。
        BungeeInitialLobbyListener.stop()
        val native = FakeServerInfo("fallback")
        val player = playerOn(null)
        val event = connectEvent(player, native, ServerConnectEvent.Reason.JOIN_PROXY)
        try {
            BungeeInitialLobbyListener.onServerConnect(event)

            assertSame(native, event.target, "未装配时不得改写目标")
            assertFalse(event.isCancelled, "未装配时不得取消连接")
            assertTrue(player.disconnectReasons.isEmpty(), "未装配时不得断开玩家")
        } finally {
            BungeeInitialLobbyListener.stop()
        }
    }

    // ---------- 装配与事件构造 ----------

    private fun startListener(
        router: InitialLobbyRouter,
        lookup: ManagedServerLookup,
    ) {
        BungeeInitialLobbyListener.start(router, lookup)
    }

    /** [serverId] 为 null 表示玩家此刻无归属，即「首次连入代理」。 */
    private fun playerOn(serverId: String?): FakePlayer = FakePlayer(PLAYER_NAME, serverId?.let { FakeServer(FakeServerInfo(it)) })

    /**
     * 放行 DEPRECATION：非废弃的 4 参构造器要先拿到 `ServerConnectRequest`，
     * 而它的 `builder()` 会读 `ProxyServer` 单例（无代理实例时直接 NPE）。3 参构造器只填字段，无平台副作用。
     */
    @Suppress("DEPRECATION")
    private fun connectEvent(
        player: FakePlayer,
        target: FakeServerInfo,
        reason: ServerConnectEvent.Reason,
    ): ServerConnectEvent = ServerConnectEvent(player, target, reason)

    // ---------- 选路快照构造（照 agent-core 的 InitialLobbyRouterTest 口径） ----------

    /** 构造「必定选中 [serverId]」的选路器：候选可调度、池已就绪、在受管目录内且是大厅成员。 */
    private fun routerSelecting(serverId: String): InitialLobbyRouter =
        InitialLobbyRouter(
            snapshot = { directory(setOf(serverId), setOf(serverId)) },
            latestLobbySnapshot = { candidateSnapshot(listOf(candidate(serverId))) },
        )

    /** 构造「必定拒绝」的选路器：大厅池里没有任何候选。 */
    private fun routerRejecting(): InitialLobbyRouter =
        InitialLobbyRouter(
            snapshot = { directory(emptySet(), emptySet()) },
            latestLobbySnapshot = { candidateSnapshot(emptyList()) },
        )

    private fun candidateSnapshot(lobbyCandidates: List<CandidateEntry>): CandidateSnapshot =
        CandidateSnapshot(
            generatedAtMs = 1L,
            savedAtMs = 1L,
            zones = emptyMap(),
            lobby = LobbyCandidates(clusterId = 1L, ready = true, candidates = lobbyCandidates),
        )

    private fun directory(
        managedIds: Set<String>,
        lobbyMembers: Set<String>,
    ): ManagedDirectorySnapshot =
        ManagedDirectorySnapshot(
            version = 1L,
            lastSuccessAtMs = 1L,
            firstSync = false,
            entries = emptyList(),
            byServerId =
                managedIds.associateWith { serverId ->
                    ServiceInstance(serverId, "bukkit", "", "", "", "", "online", 0, 0, 0)
                },
            lobbyMemberIds = lobbyMembers,
            lobby = ManagedLobbySnapshot(clusterId = 1L, ready = true, candidates = emptyList(), reason = null),
        )

    private fun candidate(serverId: String): CandidateEntry =
        CandidateEntry(
            serverId = serverId,
            score = 90,
            level = "healthy",
            schedulable = true,
            onlineCount = 0,
            maxOnline = 100,
        )

    private companion object {
        const val PLAYER_NAME: String = "Newcomer"

        /** 与生产代码保持一致：玩家看到的就是这句。 */
        const val NO_LOBBY_MESSAGE: String = "当前没有可用大厅，请稍后重试"
    }
}

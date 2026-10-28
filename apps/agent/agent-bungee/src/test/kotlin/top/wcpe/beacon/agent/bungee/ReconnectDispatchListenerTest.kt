package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.chat.BaseComponent
import net.md_5.bungee.api.config.ServerInfo
import net.md_5.bungee.api.event.ServerConnectEvent
import net.md_5.bungee.api.event.ServerKickEvent
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.bungee.support.FakePlayer
import top.wcpe.beacon.agent.bungee.support.FakeServer
import top.wcpe.beacon.agent.bungee.support.FakeServerInfo
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.proxy.ManagedDirectorySnapshot
import top.wcpe.beacon.agent.core.proxy.ManagedLobbySnapshot
import top.wcpe.beacon.agent.core.proxy.ReconnectRouter
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertSame
import kotlin.test.assertTrue

/**
 * 事件胶水层单测：[BungeeReconnectListener] 把选路结果写回 BungeeCord 事件的那几行。
 *
 * 决策本身（接管范围、与原生选择是否一致）已由 [ReconnectDispatchTest] 与 agent-core 的
 * `ReconnectRouterTest` 覆盖；这里只盯**写回**这一个动作。之所以必须单独测：
 * 踢人路径的落点与取消标记要成对设置才生效——`cancelServer` 决定去哪，
 * `isCancelled` 才是让 BungeeCord 跳过原生 disconnect 的开关，漏设其中任何一个
 * 都只会在真机上表现为"玩家仍被踢出/仍在原服"，是单测之外极易回归的缺陷。
 *
 * 平台对象（玩家、子服连接、子服信息）全部用 [FakePlayer] / [FakeServer] / [FakeServerInfo]
 * 手写替身构造，事件用 BungeeCord 真实类，不引入任何 mock 框架或反射。
 *
 * 监听器是 Kotlin `object` 单例（内含 `@Volatile` 的 router/lookup），故每个用例都必须在
 * `finally` 里 [BungeeReconnectListener.stop]，避免装配状态串到下一个用例。
 */
class ReconnectDispatchListenerTest {
    // ---------- 踢人路径：ServerKickEvent ----------

    @Test
    fun `踢人路径命中时同时写入落点与取消标记`() {
        val lobby = FakeServerInfo("lobby-1")
        val event = kickEvent(cancelServer = FakeServerInfo("native-fallback"))
        startListener(routerTo("lobby-1"), lobby)
        try {
            BungeeReconnectListener.onServerKick(event)

            // 两处必须同时写：只设 cancelServer 时 BungeeCord 仍会走原生 disconnect，玩家照样被踢。
            assertSame(lobby, event.cancelServer, "落点应写回受管目录返回的 ServerInfo 实例")
            assertTrue(event.isCancelled, "命中回退路由后必须取消原生踢出")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    @Test
    fun `踢人路径选路不可用时保持原值`() {
        val native = FakeServerInfo("native-fallback")
        val event = kickEvent(cancelServer = native)
        startListener(routerUnavailable())
        try {
            BungeeReconnectListener.onServerKick(event)

            // 未命中时两处都不能碰：留下半写状态（例如只置 isCancelled）会把玩家困在原地。
            assertSame(native, event.cancelServer, "未命中时落点必须保持原生值")
            assertFalse(event.isCancelled, "未命中时不得取消原生踢出")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    @Test
    fun `踢人路径目标不在受管目录时保持原值`() {
        val native = FakeServerInfo("native-fallback")
        val event = kickEvent(cancelServer = native)
        // 选路命中 lobby-1，但受管目录还没注入它（discovery 尚未到达）→ 必须整条交还原生逻辑。
        startListener(routerTo("lobby-1"))
        try {
            BungeeReconnectListener.onServerKick(event)

            assertSame(native, event.cancelServer, "取不到 ServerInfo 时落点必须保持原生值")
            assertFalse(event.isCancelled, "取不到 ServerInfo 时不得取消原生踢出")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    @Test
    fun `已被其他插件取消的踢人事件不被覆盖`() {
        val native = FakeServerInfo("native-fallback")
        val event = kickEvent(cancelServer = native, cancelled = true)
        startListener(routerTo("lobby-1"), FakeServerInfo("lobby-1"))
        try {
            BungeeReconnectListener.onServerKick(event)

            // 尊重先到者：先被别的插件接管的事件，落点不能被我们改写（取消标记本就为 true，保持不变）。
            assertSame(native, event.cancelServer, "已取消的事件不得被改写落点")
            assertTrue(event.isCancelled, "已取消的状态必须保持")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    // ---------- 崩溃路径：ServerConnectEvent ----------

    @Test
    fun `崩溃路径命中时改写到选定目标`() {
        val lobby = FakeServerInfo("lobby-1")
        val native = FakeServerInfo("lobby-2")
        val event = connectEvent(native)
        startListener(routerTo("lobby-1"), lobby)
        try {
            BungeeReconnectListener.onServerConnect(event)

            assertSame(lobby, event.target, "原生回退目标应被改写为选路结果")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    @Test
    fun `崩溃路径非原生回退原因时不改写`() {
        val native = FakeServerInfo("lobby-2")
        // 玩家主动 /server 换服不是掉线回退，改写了会抢走玩家自己的选择。
        val event = connectEvent(native, ServerConnectEvent.Reason.COMMAND)
        startListener(routerTo("lobby-1"), FakeServerInfo("lobby-1"))
        try {
            BungeeReconnectListener.onServerConnect(event)

            assertSame(native, event.target, "非 SERVER_DOWN_REDIRECT 不得改写目标")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    @Test
    fun `崩溃路径目标与原生一致时不改写`() {
        val native = FakeServerInfo("lobby-1")
        // 目录里放一个同名但不同实例的 ServerInfo：一旦发生写回，下面的 assertSame 会立刻失败。
        val sameName = FakeServerInfo("lobby-1")
        val event = connectEvent(native)
        startListener(routerTo("lobby-1"), sameName)
        try {
            BungeeReconnectListener.onServerConnect(event)

            assertSame(native, event.target, "与原生选择相同时不应发生写回")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    // ---------- 未装配 ----------

    @Test
    fun `未装配时两个事件都不改写`() {
        // 单例上下文里别的用例可能已装配过，先显式回到未装配态再验短路。
        BungeeReconnectListener.stop()
        val native = FakeServerInfo("native-fallback")
        val kick = kickEvent(cancelServer = native)
        val connect = connectEvent(native)
        try {
            BungeeReconnectListener.onServerKick(kick)
            BungeeReconnectListener.onServerConnect(connect)

            assertSame(native, kick.cancelServer, "未装配时踢人落点不得被改写")
            assertFalse(kick.isCancelled, "未装配时不得取消原生踢出")
            assertSame(native, connect.target, "未装配时崩溃路径目标不得被改写")
        } finally {
            BungeeReconnectListener.stop()
        }
    }

    // ---------- 装配与事件构造 ----------

    /** 装配监听器：选路固定走 [router]，受管目录只认识 [managed] 里的 serverId。 */
    private fun startListener(
        router: ReconnectRouter,
        vararg managed: FakeServerInfo,
    ) {
        val byName = managed.associateBy { it.name }
        BungeeReconnectListener.start(router, ManagedServerLookup { byName[it] })
    }

    /**
     * 构造真实踢人事件；[cancelServer] 是「原生回退落点」初值，用来判断是否被写回。
     *
     * 放行 DEPRECATION：BungeeCord 1.21 把 [ServerKickEvent] 的**全部**公开构造器都标成了
     * `@Deprecated` 且未提供替代构造器，而本测试必须拿到真实事件对象（这是被测代码的入参）。
     */
    @Suppress("DEPRECATION")
    private fun kickEvent(
        cancelServer: ServerInfo,
        cancelled: Boolean = false,
        state: ServerKickEvent.State = ServerKickEvent.State.CONNECTED,
    ): ServerKickEvent {
        val event =
            ServerKickEvent(
                player(FROM_SERVER),
                FakeServerInfo(FROM_SERVER),
                emptyArray<BaseComponent>(),
                cancelServer,
                state,
            )
        event.isCancelled = cancelled
        return event
    }

    /**
     * 构造真实连接事件；玩家此刻仍挂在 [FROM_SERVER] 上，供监听器推断原所在服。
     *
     * 放行 DEPRECATION：非废弃的 4 参构造器要求先拿到 [net.md_5.bungee.api.ServerConnectRequest]，
     * 而它的 `builder()` 会读 `ProxyServer.getInstance()` 单例（无代理实例时直接 NPE），
     * 单测里不该为了造事件去拉起代理。3 参构造器只填字段，不带任何平台副作用。
     */
    @Suppress("DEPRECATION")
    private fun connectEvent(
        target: ServerInfo,
        reason: ServerConnectEvent.Reason = ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT,
    ): ServerConnectEvent = ServerConnectEvent(player(FROM_SERVER), target, reason)

    private fun player(serverId: String?): FakePlayer = FakePlayer("Steve", serverId?.let { FakeServer(FakeServerInfo(it)) })

    // ---------- 选路快照构造（照 agent-core 的 ReconnectRouterTest 口径） ----------

    /** 构造「从 [FROM_SERVER] 掉线必定落到 [targetServerId]」的真实选路器。 */
    private fun routerTo(targetServerId: String): ReconnectRouter {
        val candidates =
            CandidateSnapshot(
                generatedAtMs = 1L,
                savedAtMs = 1L,
                zones = mapOf(ZONE to listOf(candidate(targetServerId))),
                lobby = LobbyCandidates(clusterId = 1L, ready = true, candidates = listOf(candidate(targetServerId))),
            )
        val zones = mapOf(FROM_SERVER to ZONE, targetServerId to ZONE)
        return ReconnectRouter(
            snapshot = { directory(zones, setOf(targetServerId)) },
            latestSnapshot = { candidates },
        )
    }

    /** 选路恒为 Unavailable 的选路器：验证「决策未命中 → 完全不写事件」。 */
    private fun routerUnavailable(): ReconnectRouter =
        ReconnectRouter(snapshot = { directory(emptyMap(), emptySet()) }, latestSnapshot = { null })

    private fun directory(
        zoneByServerId: Map<String, String>,
        lobbyMembers: Set<String>,
    ): ManagedDirectorySnapshot =
        ManagedDirectorySnapshot(
            version = 1L,
            lastSuccessAtMs = 1L,
            firstSync = false,
            entries = emptyList(),
            byServerId =
                zoneByServerId.mapValues { (serverId, zone) ->
                    ServiceInstance(serverId, "bukkit", "", zone, "", "", "online", 0, 0, 0)
                },
            lobbyMemberIds = lobbyMembers,
            lobby = ManagedLobbySnapshot(clusterId = 1L, ready = true, candidates = emptyList(), reason = null),
        )

    private fun candidate(serverId: String): CandidateEntry =
        CandidateEntry(
            serverId = serverId,
            score = 99,
            level = "healthy",
            schedulable = true,
            onlineCount = 0,
            maxOnline = 100,
        )

    private companion object {
        /** 掉线玩家所在的子服 id（选路入口）。 */
        const val FROM_SERVER: String = "game-1"

        /** 快照里的区名：只有区属明确时才走同区大厅这一级。 */
        const val ZONE: String = "一区"
    }
}

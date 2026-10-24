package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

class ReconnectRouterTest {
    @Test
    fun `优先回到原服同区大厅`() {
        // 一区大厅 lobby-1 与二区大厅 lobby-2 都在池里，玩家从一区子服掉线必须回 lobby-1。
        val router =
            routerOf(
                zones =
                    mapOf(
                        "一区" to listOf(candidate("lobby-1", score = 90, online = 5)),
                        "二区" to listOf(candidate("lobby-2", score = 99, online = 1)),
                    ),
                lobby = listOf(candidate("login-1", score = 99, online = 1)),
                zoneByServerId = mapOf("game-1" to "一区", "lobby-1" to "一区", "lobby-2" to "二区"),
                lobbyMembers = setOf("lobby-1", "lobby-2"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        assertEquals("lobby-1", choice.serverId)
        assertEquals(ReconnectRoute.Target.SAME_ZONE_LOBBY, choice.reason)
    }

    @Test
    fun `同区无大厅时落到登录池`() {
        // 一区没有大厅成员 → 退到全局登录大厅池，绝不落到普通子服。
        val router =
            routerOf(
                zones = mapOf("一区" to listOf(candidate("game-2", score = 99, online = 1))),
                lobby = listOf(candidate("login-1", score = 90, online = 5)),
                zoneByServerId = mapOf("game-1" to "一区", "game-2" to "一区"),
                lobbyMembers = setOf("lobby-1"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        assertEquals("login-1", choice.serverId)
        assertEquals(ReconnectRoute.Target.LOGIN_POOL, choice.reason)
    }

    @Test
    fun `同区大厅即掉线服时不再选它`() {
        // 玩家原本就在大厅里，该大厅关闭 → 同区候选只剩它自己，必须排除后走登录池。
        val router =
            routerOf(
                zones = mapOf("一区" to listOf(candidate("lobby-1", score = 99, online = 0))),
                lobby = listOf(candidate("login-2", score = 90, online = 5)),
                zoneByServerId = mapOf("lobby-1" to "一区", "login-2" to "一区"),
                lobbyMembers = setOf("lobby-1"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("lobby-1"))

        assertEquals("login-2", choice.serverId)
        assertEquals(ReconnectRoute.Target.LOGIN_POOL, choice.reason)
    }

    @Test
    fun `登录池同分同负载时交给可注入随机源`() {
        // login-1 / login-2 同分同占用 → 按 serverId 稳定排序后由随机源挑选，实现池内分摊。
        val router =
            routerOf(
                zones = emptyMap(),
                lobby = listOf(candidate("login-2", score = 90, online = 3), candidate("login-1", score = 90, online = 3)),
                zoneByServerId = mapOf("login-1" to "一区", "login-2" to "二区"),
                lobbyMembers = setOf("login-1", "login-2"),
                randomIndex = { size -> size - 1 },
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        // 排序后为 [login-1, login-2]，取最后一个 → login-2。
        assertEquals("login-2", choice.serverId)
    }

    @Test
    fun `登录池不可调度成员被跳过`() {
        val router =
            routerOf(
                zones = emptyMap(),
                lobby =
                    listOf(
                        candidate("login-1", score = 99, online = 1, schedulable = false, reasons = listOf("lost")),
                        candidate("login-2", score = 80, online = 9),
                    ),
                zoneByServerId = mapOf("login-1" to "一区", "login-2" to "二区"),
                lobbyMembers = setOf("login-1", "login-2"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        assertEquals("login-2", choice.serverId)
    }

    @Test
    fun `无可选目标时返回 Unavailable 交还原生回退`() {
        // 登录池未就绪且同区无大厅 → 明确交还原生 priorities 回退，不擅自指定目标。
        val router =
            routerOf(
                zones = emptyMap(),
                lobby = emptyList(),
                zoneByServerId = mapOf("game-1" to "一区"),
                lobbyMembers = emptySet(),
                lobbyReady = false,
            )

        assertEquals(ReconnectRoute.Unavailable, router.route("game-1"))
    }

    @Test
    fun `无候选快照时不阻断回退`() {
        // fail-static：候选快照缺失（控制面不可用且无落盘快照）时不得阻断玩家，直接交还原生回退。
        val router =
            ReconnectRouter(
                snapshot = { directory(mapOf("game-1" to "一区"), emptySet()) },
                latestSnapshot = { null },
            )

        assertEquals(ReconnectRoute.Unavailable, router.route("game-1"))
    }

    @Test
    fun `同区大厅按健康分择优而不是就近取首个`() {
        // 同区两台大厅：分数低的即便排在列表前面也不该被选中，证明选路用的是打分而非顺序。
        val router =
            routerOf(
                zones =
                    mapOf(
                        "一区" to listOf(candidate("lobby-low", score = 40, online = 0), candidate("lobby-high", score = 95, online = 50)),
                    ),
                lobby = listOf(candidate("login-1", score = 99, online = 1)),
                zoneByServerId = mapOf("game-1" to "一区", "lobby-low" to "一区", "lobby-high" to "一区"),
                lobbyMembers = setOf("lobby-low", "lobby-high"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        assertEquals("lobby-high", choice.serverId)
    }

    @Test
    fun `同分时优先选占用更低的同区大厅`() {
        // 分数相同则比较占用率：lobby-busy 已满，应让位给 lobby-idle。
        val router =
            routerOf(
                zones =
                    mapOf(
                        "一区" to listOf(candidate("lobby-busy", score = 90, online = 100), candidate("lobby-idle", score = 90, online = 1)),
                    ),
                lobby = emptyList(),
                zoneByServerId = mapOf("game-1" to "一区", "lobby-busy" to "一区", "lobby-idle" to "一区"),
                lobbyMembers = setOf("lobby-busy", "lobby-idle"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-1"))

        assertEquals("lobby-idle", choice.serverId)
        assertEquals(ReconnectRoute.Target.SAME_ZONE_LOBBY, choice.reason)
    }

    @Test
    fun `不在受管目录的候选被跳过`() {
        // 候选快照里出现但目录里没有的服，本代理转发不到，必须排除，否则玩家会被送到不可达目标。
        val router =
            ReconnectRouter(
                snapshot = { directory(mapOf("game-1" to "一区"), setOf("game-1"), emptySet()) },
                latestSnapshot = {
                    CandidateSnapshot(
                        generatedAtMs = 1L,
                        savedAtMs = 1L,
                        zones = emptyMap(),
                        lobby = LobbyCandidates(clusterId = 1L, ready = true, candidates = listOf(candidate("login-ghost", 99, 0))),
                    )
                },
            )

        assertEquals(ReconnectRoute.Unavailable, router.route("game-1"))
    }

    @Test
    fun `原服无区属时不走高价值同区路径而落登录池`() {
        // 未分配 zone 的服掉线：无从判断"同区"，必须直接降级到登录池。
        val router =
            routerOf(
                zones = mapOf("一区" to listOf(candidate("lobby-1", score = 99, online = 0))),
                lobby = listOf(candidate("login-1", score = 80, online = 5)),
                zoneByServerId = mapOf("game-bare" to "", "lobby-1" to "一区"),
                lobbyMembers = setOf("lobby-1"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route("game-bare"))

        assertEquals("login-1", choice.serverId)
        assertEquals(ReconnectRoute.Target.LOGIN_POOL, choice.reason)
    }

    @Test
    fun `候选池内多个并列时机不恒等于首个`() {
        // 随机性口径（spec §6.2）：只验「目标在候选集内 且 不恒等于排序后首个」，
        // 不做统计学分布断言。这里用两个不同随机源证明结果确实随随机源变化。
        val first =
            routerOf(
                zones = emptyMap(),
                lobby = listOf(candidate("login-1", 90, 3), candidate("login-2", 90, 3), candidate("login-3", 90, 3)),
                zoneByServerId = mapOf("login-1" to "一区", "login-2" to "二区", "login-3" to "三区"),
                lobbyMembers = setOf("login-1", "login-2", "login-3"),
                randomIndex = { 0 },
            )
        val last =
            routerOf(
                zones = emptyMap(),
                lobby = listOf(candidate("login-1", 90, 3), candidate("login-2", 90, 3), candidate("login-3", 90, 3)),
                zoneByServerId = mapOf("login-1" to "一区", "login-2" to "二区", "login-3" to "三区"),
                lobbyMembers = setOf("login-1", "login-2", "login-3"),
                randomIndex = { size -> size - 1 },
            )

        val pickFirst = assertIs<ReconnectRoute.Selected>(first.route("game-1"))
        val pickLast = assertIs<ReconnectRoute.Selected>(last.route("game-1"))

        // 两次都在候选集内
        assertEquals(true, pickFirst.serverId in setOf("login-1", "login-2", "login-3"))
        assertEquals(true, pickLast.serverId in setOf("login-1", "login-2", "login-3"))
        // 且证明存在非「首个」的结果（否则随机形同虚设）
        assertEquals("login-1", pickFirst.serverId)
        assertEquals("login-3", pickLast.serverId)
    }

    @Test
    fun `fromServerId 为空时直接落登录池`() {
        // 拿不到原服（kickedFrom 缺失）时不做区感知，直接走登录池；全空则交还原生。
        val router =
            routerOf(
                zones = emptyMap(),
                lobby = listOf(candidate("login-1", score = 90, online = 3)),
                zoneByServerId = mapOf("login-1" to "一区"),
                lobbyMembers = setOf("login-1"),
            )

        val choice = assertIs<ReconnectRoute.Selected>(router.route(fromServerId = null))

        assertEquals("login-1", choice.serverId)
        assertEquals(ReconnectRoute.Target.LOGIN_POOL, choice.reason)
    }

    private fun routerOf(
        zones: Map<String, List<CandidateEntry>>,
        lobby: List<CandidateEntry>,
        zoneByServerId: Map<String, String>,
        lobbyMembers: Set<String>,
        lobbyReady: Boolean = true,
        randomIndex: (Int) -> Int = { 0 },
    ): ReconnectRouter {
        val snapshot =
            CandidateSnapshot(
                generatedAtMs = 1L,
                savedAtMs = 1L,
                zones = zones,
                lobby = LobbyCandidates(clusterId = 1L, ready = lobbyReady, candidates = lobby),
            )
        // 受管目录需覆盖所有出现过的 serverId，否则候选会被目录成员校验过滤掉。
        val managedIds = zoneByServerId.keys + lobby.map { it.serverId }
        return ReconnectRouter(
            snapshot = { directory(zoneByServerId, managedIds, lobbyMembers) },
            latestSnapshot = { snapshot },
            randomIndex = randomIndex,
        )
    }

    private fun directory(
        zoneByServerId: Map<String, String>,
        managedIds: Set<String>,
        lobbyMembers: Set<String> = emptySet(),
    ): ManagedDirectorySnapshot =
        ManagedDirectorySnapshot(
            version = 1L,
            lastSuccessAtMs = 1L,
            firstSync = false,
            entries = emptyList(),
            byServerId =
                managedIds.associateWith { serverId ->
                    ServiceInstance(serverId, "bukkit", "", zoneByServerId[serverId] ?: "", "", "", "online", 0, 0, 0)
                },
            lobbyMemberIds = lobbyMembers,
            lobby = ManagedLobbySnapshot(clusterId = 1L, ready = true, candidates = emptyList(), reason = null),
        )

    private fun candidate(
        serverId: String,
        score: Int,
        online: Int,
        schedulable: Boolean = true,
        reasons: List<String> = emptyList(),
    ): CandidateEntry =
        CandidateEntry(
            serverId = serverId,
            score = score,
            level = "healthy",
            schedulable = schedulable,
            onlineCount = online,
            maxOnline = 100,
            reasons = reasons,
        )
}

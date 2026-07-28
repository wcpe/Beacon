package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.api.ServiceInstance
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

class InitialLobbyRouterTest {
    @Test
    fun `按最高分再低在线人数选择大厅`() {
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        candidates =
                            listOf(
                                candidate("lobby-b", score = 90, online = 10),
                                candidate("lobby-a", score = 92, online = 50),
                                candidate("lobby-c", score = 92, online = 3),
                            ),
                    )
                },
            )

        val choice = assertIs<InitialLobbyRoute.Selected>(router.route())

        assertEquals("lobby-c", choice.serverId)
    }

    @Test
    fun `同分同在线按稳定排序后交给可注入随机源`() {
        val router =
            InitialLobbyRouter(
                snapshot = { managedSnapshot(listOf(candidate("lobby-z", 90, 1), candidate("lobby-a", 90, 1))) },
                randomIndex = { size -> size - 1 },
            )

        val choice = assertIs<InitialLobbyRoute.Selected>(router.route())

        assertEquals("lobby-z", choice.serverId)
    }

    @Test
    fun `同分时按占用率升序选择大厅`() {
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        listOf(
                            candidate("lobby-full", score = 90, online = 10, maxOnline = 10),
                            candidate("lobby-room", score = 90, online = 20, maxOnline = 100),
                        ),
                    )
                },
            )

        val choice = assertIs<InitialLobbyRoute.Selected>(router.route())

        assertEquals("lobby-room", choice.serverId)
    }

    @Test
    fun `同分时无容量上限视为已满`() {
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        listOf(
                            candidate("lobby-full", score = 90, online = 0, maxOnline = 0),
                            candidate("lobby-room", score = 90, online = 20, maxOnline = 100),
                        ),
                    )
                },
            )

        val choice = assertIs<InitialLobbyRoute.Selected>(router.route())

        assertEquals("lobby-room", choice.serverId)
    }

    @Test
    fun `首连直接读取最新大厅快照而不等待目录重同步发布组合帧`() {
        val latestLobby =
            top.wcpe.beacon.agent.core.client.LobbyCandidates(
                clusterId = 12L,
                ready = true,
                candidates = listOf(candidate("lobby-new", score = 90, online = 1)),
            )
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        candidates = listOf(candidate("lobby-old", score = 90, online = 1)),
                        managedIds = setOf("lobby-old", "lobby-new"),
                        lobbyMembers = setOf("lobby-old", "lobby-new"),
                    )
                },
                latestLobbySnapshot = {
                    top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot(1L, 2L, emptyMap(), latestLobby)
                },
            )

        val choice = assertIs<InitialLobbyRoute.Selected>(router.route())

        assertEquals("lobby-new", choice.serverId)
    }

    @Test
    fun `直接调度读取无大厅快照时不回退到陈旧目录组合帧`() {
        val router =
            InitialLobbyRouter(
                snapshot = { managedSnapshot(listOf(candidate("lobby-old", score = 90, online = 1))) },
                latestLobbySnapshot = { null },
            )

        val rejected = assertIs<InitialLobbyRoute.Rejected>(router.route())

        assertEquals("no_snapshot", rejected.reason)
    }

    @Test
    fun `无候选时明确拒绝不推断普通区服`() {
        val router = InitialLobbyRouter(snapshot = { managedSnapshot(emptyList()) })

        val rejected = assertIs<InitialLobbyRoute.Rejected>(router.route())

        assertEquals("no_candidate", rejected.reason)
    }

    @Test
    fun `未就绪大厅即使带有异常候选也明确拒绝`() {
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        candidates = listOf(candidate("lobby-1", 90, 1)),
                        ready = false,
                    )
                },
            )

        val rejected = assertIs<InitialLobbyRoute.Rejected>(router.route())

        assertEquals("not_ready", rejected.reason)
    }

    @Test
    fun `候选未进入受管目录时明确拒绝`() {
        val router =
            InitialLobbyRouter(
                snapshot = { managedSnapshot(listOf(candidate("lobby-1", 90, 1)), managedIds = emptySet()) },
            )

        val rejected = assertIs<InitialLobbyRoute.Rejected>(router.route())

        assertEquals("managed_directory_mismatch", rejected.reason)
    }

    @Test
    fun `候选虽在受管目录但不是大厅成员时明确拒绝`() {
        val router =
            InitialLobbyRouter(
                snapshot = {
                    managedSnapshot(
                        candidates = listOf(candidate("zone-normal", 90, 1)),
                        managedIds = setOf("zone-normal"),
                        lobbyMembers = emptySet(),
                    )
                },
            )

        val rejected = assertIs<InitialLobbyRoute.Rejected>(router.route())

        assertEquals("lobby_member_mismatch", rejected.reason)
    }

    private fun managedSnapshot(
        candidates: List<CandidateEntry>,
        managedIds: Set<String> = candidates.map { it.serverId }.toSet(),
        lobbyMembers: Set<String> = candidates.map { it.serverId }.toSet(),
        ready: Boolean = candidates.isNotEmpty(),
    ): ManagedDirectorySnapshot =
        ManagedDirectorySnapshot(
            version = 1L,
            lastSuccessAtMs = 1L,
            firstSync = false,
            entries = emptyList(),
            byServerId = managedIds.associateWith { serverId -> ServiceInstance(serverId, "bukkit", "", "", "", "", "online", 0, 0, 0) },
            lobbyMemberIds = lobbyMembers,
            lobby = ManagedLobbySnapshot(clusterId = 12L, ready = ready, candidates = candidates, reason = if (ready) null else "not_ready"),
        )

    private fun candidate(serverId: String, score: Int, online: Int, maxOnline: Int = 300): CandidateEntry =
        CandidateEntry(serverId, score, "healthy", true, online, maxOnline)
}

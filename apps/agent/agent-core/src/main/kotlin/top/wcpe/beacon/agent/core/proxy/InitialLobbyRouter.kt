package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.random.Random

/** 首次大厅落脚的纯路由：只读快照，不做网络、磁盘或平台代理调用。 */
class InitialLobbyRouter(
    private val snapshot: () -> ManagedDirectorySnapshot,
    private val latestLobbySnapshot: (() -> CandidateSnapshot?)? = null,
    private val randomIndex: (Int) -> Int = { size -> Random.nextInt(size) },
) {
    fun route(): InitialLobbyRoute {
        val directory = snapshot()
        val lobby = latestLobbySnapshot?.invoke()?.lobby?.toManagedLobby() ?: latestLobbyUnavailable(directory)
        val candidates = lobby.candidates.filter { it.schedulable }
        return resolveLobbyRoute(lobby, candidates, directory)
    }

    /** 按候选 / 就绪态 / 受管目录 / 大厅成员逐级判定，返回落脚结果。 */
    private fun resolveLobbyRoute(
        lobby: ManagedLobbySnapshot,
        candidates: List<CandidateEntry>,
        directory: ManagedDirectorySnapshot,
    ): InitialLobbyRoute {
        if (candidates.isEmpty()) return InitialLobbyRoute.Rejected(if (lobby.reason == NO_SNAPSHOT) NO_SNAPSHOT else NO_CANDIDATE)
        if (!lobby.ready) return InitialLobbyRoute.Rejected(lobby.reason ?: NOT_READY)
        val managed = candidates.filter { it.serverId in directory.byServerId }
        if (managed.isEmpty()) return InitialLobbyRoute.Rejected(MANAGED_DIRECTORY_MISMATCH)
        return selectLobbyMember(managed, directory)
    }

    /** 从受管候选中筛出大厅成员并选择；无大厅成员时明确拒绝。 */
    private fun selectLobbyMember(
        managed: List<CandidateEntry>,
        directory: ManagedDirectorySnapshot,
    ): InitialLobbyRoute {
        val lobbyMembers = managed.filter { it.serverId in directory.lobbyMemberIds }
        if (lobbyMembers.isEmpty()) return InitialLobbyRoute.Rejected(LOBBY_MEMBER_MISMATCH)
        return InitialLobbyRoute.Selected(select(lobbyMembers).serverId)
    }

    private fun select(candidates: List<CandidateEntry>): CandidateEntry {
        val highestScore = candidates.maxOf { it.score }
        val highestScoreCandidates = candidates.filter { it.score == highestScore }
        val lowestOccupancy = highestScoreCandidates.minOf(::occupancy)
        val tied = highestScoreCandidates.filter { occupancy(it) == lowestOccupancy }.sortedBy { it.serverId }
        return tied[randomIndex(tied.size)]
    }

    private fun occupancy(candidate: CandidateEntry): Double =
        if (candidate.maxOnline <= 0) 1.0 else candidate.onlineCount.toDouble() / candidate.maxOnline

    private fun latestLobbyUnavailable(directory: ManagedDirectorySnapshot): ManagedLobbySnapshot =
        if (latestLobbySnapshot == null) directory.lobby else ManagedLobbySnapshot(null, false, emptyList(), NO_SNAPSHOT)

    private fun top.wcpe.beacon.agent.core.client.LobbyCandidates.toManagedLobby(): ManagedLobbySnapshot {
        val reason =
            when {
                candidates.isNotEmpty() -> null
                !ready -> NOT_READY
                else -> NO_CANDIDATE
            }
        return ManagedLobbySnapshot(clusterId, ready, candidates, reason)
    }

    private companion object {
        const val NO_CANDIDATE = "no_candidate"
        const val NO_SNAPSHOT = "no_snapshot"
        const val NOT_READY = "not_ready"
        const val MANAGED_DIRECTORY_MISMATCH = "managed_directory_mismatch"
        const val LOBBY_MEMBER_MISMATCH = "lobby_member_mismatch"
    }
}

sealed class InitialLobbyRoute {
    data class Selected(val serverId: String) : InitialLobbyRoute()

    data class Rejected(val reason: String) : InitialLobbyRoute()
}

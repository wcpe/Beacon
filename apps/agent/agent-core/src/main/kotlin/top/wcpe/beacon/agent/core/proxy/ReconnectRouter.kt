package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.random.Random

/**
 * 子服关闭后的回退路由：先按玩家原所在服的小区找同区大厅，再落到全局登录大厅池随机。
 *
 * 只读内存快照，不做网络、磁盘或平台代理调用；不修改任何控制面状态，
 * 因此不违反「控制面不写游戏逻辑」的边界（决策发生在 agent 数据面）。
 *
 * 回退优先级：
 * 1. 同区大厅：原所在服所属 zone 内、属于大厅簇且可调度的候选；
 * 2. 全局登录池：快照里的 lobby 池（跨区），同分同负载内随机；
 * 3. 都无法命中 → [ReconnectRoute.Unavailable]，由调用方决定放行 BungeeCord 原生回退。
 */
class ReconnectRouter(
    private val snapshot: () -> ManagedDirectorySnapshot,
    private val latestSnapshot: (() -> CandidateSnapshot?)? = null,
    private val randomIndex: (Int) -> Int = { size -> Random.nextInt(size) },
) {
    /**
     * 为掉线玩家选回退目标。
     *
     * @param fromServerId 玩家掉线前所在的后端服 serverId
     * @param excludeServerIds 需要排除的 serverId（通常是刚关闭的那个服）
     */
    fun route(
        fromServerId: String?,
        excludeServerIds: Set<String> = emptySet(),
    ): ReconnectRoute {
        val directory = snapshot()
        val excluded = excludeServerIds + listOfNotNull(fromServerId)
        val candidates = latestSnapshot?.invoke()
        // 逐级降级：同区大厅 → 全局登录池 → 交还原生回退，避免多出口散落。
        return routeToSameZoneLobby(fromServerId, candidates, directory, excluded)
            ?: routeToLoginPool(candidates, directory, excluded)
            ?: ReconnectRoute.Unavailable
    }

    /** 同区大厅：限定原所在服所属 zone，仅在区属明确且命中大厅簇成员时生效。 */
    private fun routeToSameZoneLobby(
        fromServerId: String?,
        candidates: CandidateSnapshot?,
        directory: ManagedDirectorySnapshot,
        excluded: Set<String>,
    ): ReconnectRoute? {
        if (fromServerId == null) return null
        val zone = directory.byServerId[fromServerId]?.zone()?.takeIf { it.isNotBlank() } ?: return null
        val zoneCandidates = candidates?.zones?.get(zone) ?: return null
        // 只认已发布的受管目录成员，避免把玩家送到本代理转发不到的服。
        val managed = zoneCandidates.filter { it.schedulable && it.serverId in directory.byServerId }
        val lobbyMembers = managed.filter { it.serverId in directory.lobbyMemberIds && it.serverId !in excluded }
        if (lobbyMembers.isEmpty()) return null
        return ReconnectRoute.Selected(select(lobbyMembers).serverId, ReconnectRoute.Target.SAME_ZONE_LOBBY)
    }

    /** 全局登录池：复用大厅选服打分规则，同分同负载内随机分摊。 */
    private fun routeToLoginPool(
        candidates: CandidateSnapshot?,
        directory: ManagedDirectorySnapshot,
        excluded: Set<String>,
    ): ReconnectRoute? {
        val pool = candidates?.lobby ?: return null
        if (!pool.ready) return null
        val available =
            pool.candidates.filter {
                it.schedulable && it.serverId in directory.byServerId && it.serverId !in excluded
            }
        if (available.isEmpty()) return null
        return ReconnectRoute.Selected(select(available).serverId, ReconnectRoute.Target.LOGIN_POOL)
    }

    /** 同分取低负载，再同负载内随机；与首次大厅落脚保持一致的选服语义。 */
    private fun select(candidates: List<CandidateEntry>): CandidateEntry {
        val highestScore = candidates.maxOf { it.score }
        val highestScoreCandidates = candidates.filter { it.score == highestScore }
        val lowestOccupancy = highestScoreCandidates.minOf(::occupancy)
        val tied = highestScoreCandidates.filter { occupancy(it) == lowestOccupancy }.sortedBy { it.serverId }
        return tied[randomIndex(tied.size)]
    }

    private fun occupancy(candidate: CandidateEntry): Double =
        if (candidate.maxOnline <= 0) 1.0 else candidate.onlineCount.toDouble() / candidate.maxOnline
}

/** 回退路由结果；Unavailable 表示本代理无法给出更优目标，交由原生回退处理。 */
sealed class ReconnectRoute {
    /** 命中回退目标，[reason] 标记命中的是哪一级。 */
    data class Selected(
        val serverId: String,
        val reason: Target,
    ) : ReconnectRoute()

    /** 无可用回退目标。 */
    data object Unavailable : ReconnectRoute()

    /** 回退目标来源级别。 */
    enum class Target {
        /** 玩家原所在服的同区大厅。 */
        SAME_ZONE_LOBBY,

        /** 全局登录大厅池。 */
        LOGIN_POOL,
    }
}

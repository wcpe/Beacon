package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.CandidateEntry

/** BC 已发布的 Beacon 受管目录整帧快照；调用方只能读取，不可原地修改。 */
data class ManagedDirectorySnapshot(
    val version: Long,
    val lastSuccessAtMs: Long?,
    val firstSync: Boolean,
    val entries: List<ServiceInstance>,
    val byServerId: Map<String, ServiceInstance>,
    /** 当前成功发现帧中属于 LobbyCluster 的受管服务；不等同于可调度候选。 */
    val lobbyMemberIds: Set<String> = emptySet(),
    /** 每台受管服在本帧配套的调度事实；无候选映射时明确为 [ManagedHealthFactState.NOT_REPORTED]。 */
    val healthByServerId: Map<String, ManagedServerHealth> =
        entries.associate { it.serverId() to ManagedServerHealth.notReported() },
    val lobby: ManagedLobbySnapshot,
) {
    /** 从当前已发布帧读取健康事实；历史构造快照缺索引时仍明确返回“未上报”。 */
    fun healthOf(serverId: String): ManagedServerHealth = healthByServerId[serverId] ?: ManagedServerHealth.notReported()
}

/** 受管目录条目的调度事实来源状态。 */
enum class ManagedHealthFactState {
    /** 当前候选快照中存在该 serverId。 */
    REPORTED,

    /** 当前候选快照无该 serverId；不是命令层的临时查询结果。 */
    NOT_REPORTED,
}

/** 与单台受管服务器同帧发布的健康、容量和不可调度原因。 */
data class ManagedServerHealth(
    val state: ManagedHealthFactState,
    val level: String?,
    val schedulable: Boolean?,
    val score: Int?,
    val onlineCount: Int?,
    val maxOnline: Int?,
    val reasons: List<String>,
) {
    companion object {
        fun reported(candidate: CandidateEntry): ManagedServerHealth =
            ManagedServerHealth(
                state = ManagedHealthFactState.REPORTED,
                level = candidate.level,
                schedulable = candidate.schedulable,
                score = candidate.score,
                onlineCount = candidate.onlineCount,
                maxOnline = candidate.maxOnline,
                reasons = candidate.reasons.toList(),
            )

        fun notReported(): ManagedServerHealth =
            ManagedServerHealth(
                state = ManagedHealthFactState.NOT_REPORTED,
                level = null,
                schedulable = null,
                score = null,
                onlineCount = null,
                maxOnline = null,
                reasons = emptyList(),
            )
    }
}

/** 与受管目录同帧发布的大厅候选摘要；reason 为不可用于首次大厅落脚的机器可读原因。 */
data class ManagedLobbySnapshot(
    val clusterId: Long?,
    val ready: Boolean,
    val candidates: List<CandidateEntry>,
    val reason: String?,
)

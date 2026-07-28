package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.DiscoveryFetchResult
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import top.wcpe.beacon.agent.core.log.LogRedactor
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

/**
 * 同步 namespace 全量 Beacon Bukkit 子服到 Proxy 受管目录，并与大厅候选同帧发布（FR-200）。
 *
 * `homeGroup/homeZone` 仅保留构造兼容；新路径不读取它们、不选小区默认入口、更不写 listener priority。
 */
class ProxyServerDirectorySyncer(
    private val directory: ProxyServerDirectory,
    @Suppress("UNUSED_PARAMETER") homeGroup: String = "",
    @Suppress("UNUSED_PARAMETER") homeZone: String = "",
    private val warn: (String) -> Unit = {},
    private val info: (String) -> Unit = {},
    private val lobbySnapshot: () -> CandidateSnapshot? = { null },
    private val now: () -> Long = { System.currentTimeMillis() },
    private val discover: () -> DiscoveryFetchResult<ServiceInstance>,
) {
    private val seenManaged: MutableSet<String> = linkedSetOf()
    private val syncLock = ReentrantLock()

    @Volatile
    private var published = emptySnapshot()

    /** 读取最近一次成功发布的完整目录快照；失败刷新绝不替换此引用。 */
    fun snapshot(): ManagedDirectorySnapshot = published

    /** 周期同步与命令同步共用的串行入口；仅成功完整发布时返回 true。 */
    fun syncOnce(): Boolean = syncLock.withLock {
        when (val result = discover()) {
            is DiscoveryFetchResult.Success -> {
                syncSuccessfulSnapshot(result.instances)
                true
            }

            is DiscoveryFetchResult.Failed -> {
                warn(LogRedactor.redact("发现 Beacon 子服失败，保留现有代理目录：${result.reason}"))
                false
            }
        }
    }

    private fun syncSuccessfulSnapshot(discovered: List<ServiceInstance>) {
        val instances = discovered.filter { it.role() == ROLE_BUKKIT && it.status() in MANAGED_STATUSES }
        val desired = instances.map { it.serverId() }.toSet()
        for (instance in instances) {
            val existedManual = directory.hasServer(instance.serverId()) && !directory.isManaged(instance.serverId())
            if (directory.upsertManaged(instance)) {
                seenManaged.add(instance.serverId())
                if (existedManual) {
                    info("接管 Proxy 同名手工服务器并由 Beacon 管理：${instance.serverId()} -> ${instance.address()}")
                } else {
                    info("注入 Beacon 子服到代理目录：${instance.serverId()} -> ${instance.address()}")
                }
            } else if (directory.isManaged(instance.serverId())) {
                seenManaged.add(instance.serverId())
            }
        }
        removeStale(desired)
        publish(instances)
    }

    private fun removeStale(desired: Set<String>) {
        val stale = seenManaged.filter { it !in desired }.toList()
        for (serverId in stale) {
            directory.removeManaged(serverId)
            seenManaged.remove(serverId)
        }
    }

    private fun publish(entries: List<ServiceInstance>) {
        val previous = published
        val scheduling = lobbySnapshot()
        published =
            ManagedDirectorySnapshot(
                version = previous.version + 1,
                lastSuccessAtMs = now(),
                firstSync = previous.version == 0L,
                entries = entries.toList(),
                byServerId = entries.associateBy { it.serverId() },
                lobbyMemberIds = entries.filter { it.lobbyClusterMember() }.mapTo(linkedSetOf()) { it.serverId() },
                healthByServerId = healthFacts(entries, scheduling),
                lobby = if (entries.isEmpty()) emptyLobby() else currentLobby(scheduling),
            )
    }

    private fun healthFacts(
        entries: List<ServiceInstance>,
        scheduling: CandidateSnapshot?,
    ): Map<String, ManagedServerHealth> {
        val candidates = LinkedHashMap<String, top.wcpe.beacon.agent.core.client.CandidateEntry>()
        scheduling?.zones?.values?.flatten()?.forEach { candidates[it.serverId] = it }
        scheduling?.lobby?.candidates?.forEach { candidates[it.serverId] = it }
        return entries.associate { entry ->
            entry.serverId() to (candidates[entry.serverId()]?.let(ManagedServerHealth::reported) ?: ManagedServerHealth.notReported())
        }
    }

    private fun currentLobby(scheduling: CandidateSnapshot?): ManagedLobbySnapshot {
        val lobby = scheduling?.lobby ?: return ManagedLobbySnapshot(null, false, emptyList(), NO_SNAPSHOT)
        val candidates = lobby.candidates.toList()
        val reason = when {
            candidates.isNotEmpty() -> null
            !lobby.ready -> NOT_READY
            else -> NO_CANDIDATE
        }
        return ManagedLobbySnapshot(lobby.clusterId, lobby.ready, candidates, reason)
    }

    private fun emptyLobby(): ManagedLobbySnapshot = ManagedLobbySnapshot(null, false, emptyList(), NO_CANDIDATE)

    private fun emptySnapshot(): ManagedDirectorySnapshot =
        ManagedDirectorySnapshot(
            version = 0L,
            lastSuccessAtMs = null,
            firstSync = true,
            entries = emptyList(),
            byServerId = emptyMap(),
            lobby = ManagedLobbySnapshot(null, false, emptyList(), NO_SNAPSHOT),
        )

    private companion object {
        const val ROLE_BUKKIT = "bukkit"
        val MANAGED_STATUSES = setOf("online", "degraded")
        const val NO_SNAPSHOT = "no_snapshot"
        const val NOT_READY = "not_ready"
        const val NO_CANDIDATE = "no_candidate"
    }
}

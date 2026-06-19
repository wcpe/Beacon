package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.Discovery
import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.api.TopologyListener
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.messaging.RosterDirectory

/**
 * Discovery 的 core 实现：背后走 BeaconApiClient.discover（同步 HTTP）。
 * 调用方须在异步线程使用（API 文档已注明）。
 *
 * 拓扑 watch（FR-29）复用 agent 既有 server→agent 推送流：watch 注册到 [topologyWatchHub]，
 * AgentLifecycle 收到 topology-changed 事件后扇出回调。未启用推送流（回退态）时 watch 返回不可用 no-op 句柄。
 *
 * 名册只读查询（FR-31 / ADR-0022）：roster() 全表读 [rosterDirectory]，rosterInZone() 用控制面权威
 * zone→serverId 集 ∩ 名册过滤。rosterDirectory 是可选依赖（messaging 未开 / Redis 未连时返空），
 * 优雅降级返空 Map、绝不抛。
 *
 * @param rosterDirectory 玩家位置名册只读端口（装配期注入持有者；不可用时其 snapshot 返空）
 */
class DiscoveryView(
    private val apiClient: BeaconApiClient,
    private val topologyWatchHub: TopologyWatchHub,
    private val rosterDirectory: RosterDirectory,
) : Discovery {

    override fun query(query: DiscoveryQuery): List<ServiceInstance> {
        return apiClient.discover(
            namespace = query.namespace().orElse(null),
            group = query.group().orElse(null),
            zone = query.zone().orElse(null),
            role = query.role().orElse(null),
            tags = query.tags(),
        ).map { toInstance(it) }
    }

    override fun instancesInZone(group: String, zone: String): List<ServiceInstance> {
        return apiClient.discover(namespace = null, group = group, zone = zone, role = null)
            .map { toInstance(it) }
    }

    override fun instancesInGroup(group: String): List<ServiceInstance> {
        return apiClient.discover(namespace = null, group = group, zone = null, role = null)
            .map { toInstance(it) }
    }

    override fun watch(listener: TopologyListener): ListenerHandle {
        // 回退态：未注入推送流（控制面不可用 / 迁移期）→ 无拓扑直播通道，返回不可用 no-op 句柄、不注册。
        if (!apiClient.streamingEnabled()) {
            return UNAVAILABLE_HANDLE
        }
        return topologyWatchHub.watch(listener)
    }

    override fun roster(): Map<String, String> {
        // 全量名册即名册端口的快照（不可用时端口已降级返空）。
        return rosterDirectory.snapshot()
    }

    override fun rosterInZone(group: String, zone: String): Map<String, String> {
        val full = rosterDirectory.snapshot()
        if (full.isEmpty()) {
            return emptyMap()
        }
        // 控制面权威：解出该 zone 的可用 serverId 集（zone 归属来自控制面 DB，ADR-0004）。
        val zoneServerIds = instancesInZone(group, zone).map { it.serverId() }.toSet()
        if (zoneServerIds.isEmpty()) {
            return emptyMap()
        }
        // 名册不臆造 zone：仅取 value 落在该 zone serverId 集内的条目。
        return full.filterValues { it in zoneServerIds }
    }

    private companion object {
        // 回退态返回的不可用句柄：remove 安全可重复调用、无副作用。
        private val UNAVAILABLE_HANDLE = ListenerHandle { /* 无监听器注册，注销无操作 */ }
    }

    private fun toInstance(obj: Map<String, Any?>): ServiceInstance {
        return ServiceInstance(
            JsonTree.strOr(obj, "serverId", ""),
            JsonTree.strOr(obj, "role", ""),
            JsonTree.strOr(obj, "group", ""),
            JsonTree.strOr(obj, "zone", ""),
            JsonTree.strOr(obj, "address", ""),
            JsonTree.strOr(obj, "version", ""),
            JsonTree.strOr(obj, "status", ""),
            JsonTree.intOr(obj, "playerCount", 0),
            JsonTree.intOr(obj, "capacity", 0),
            JsonTree.intOr(obj, "weight", 0),
        )
    }
}

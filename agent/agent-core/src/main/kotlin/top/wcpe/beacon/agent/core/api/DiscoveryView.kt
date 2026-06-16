package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.Discovery
import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.JsonTree

/**
 * Discovery 的 core 实现：背后走 BeaconApiClient.discover（同步 HTTP）。
 * 调用方须在异步线程使用（API 文档已注明）。
 */
class DiscoveryView(
    private val apiClient: BeaconApiClient,
) : Discovery {

    override fun query(query: DiscoveryQuery): List<ServiceInstance> {
        return apiClient.discover(
            namespace = query.namespace().orElse(null),
            group = query.group().orElse(null),
            zone = query.zone().orElse(null),
            role = query.role().orElse(null),
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

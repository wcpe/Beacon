package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/** 同步 Beacon discovery 的 Bukkit 子服到 Proxy 服务器目录。 */
class ProxyServerDirectorySyncer(
    private val directory: ProxyServerDirectory,
    private val warn: (String) -> Unit = {},
    private val discover: () -> List<ServiceInstance>,
) {
    private val seenManaged: MutableSet<String> = linkedSetOf()

    fun syncOnce() {
        val instances = discover().filter { it.role() == ROLE_BUKKIT && it.status() == STATUS_ONLINE }
        val desired = instances.map { it.serverId() }.toSet()
        for (instance in instances) {
            if (directory.hasServer(instance.serverId()) && !directory.isManaged(instance.serverId())) {
                warn("跳过 Beacon 子服注入：Proxy 已存在同名手工服务器 ${instance.serverId()}")
                continue
            }
            if (directory.upsertManaged(instance)) {
                seenManaged.add(instance.serverId())
            }
        }
        val stale = seenManaged.filter { it !in desired }.toList()
        for (serverId in stale) {
            directory.removeManaged(serverId)
            seenManaged.remove(serverId)
        }
    }

    private companion object {
        const val ROLE_BUKKIT = "bukkit"
        const val STATUS_ONLINE = "online"
    }
}

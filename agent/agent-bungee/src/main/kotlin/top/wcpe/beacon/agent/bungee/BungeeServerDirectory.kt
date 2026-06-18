package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.proxy.ProxyServerDirectory
import java.net.InetSocketAddress

/** BungeeCord ServerInfo 目录实现，只管理 Beacon 创建过的条目。 */
class BungeeServerDirectory : ProxyServerDirectory {
    private val managed: MutableSet<String> = linkedSetOf()

    override fun hasServer(serverId: String): Boolean {
        return ProxyServer.getInstance().servers.containsKey(serverId)
    }

    override fun isManaged(serverId: String): Boolean = managed.contains(serverId)

    override fun upsertManaged(instance: ServiceInstance): Boolean {
        if (hasServer(instance.serverId()) && !isManaged(instance.serverId())) return false
        val address = parseAddress(instance.address()) ?: return false
        val info = ProxyServer.getInstance().constructServerInfo(
            instance.serverId(),
            address,
            "Beacon 管理子服 ${instance.serverId()}",
            false,
        )
        ProxyServer.getInstance().servers[instance.serverId()] = info
        managed.add(instance.serverId())
        return true
    }

    override fun removeManaged(serverId: String) {
        if (!managed.remove(serverId)) return
        ProxyServer.getInstance().servers.remove(serverId)
    }

    private fun parseAddress(raw: String): InetSocketAddress? {
        val idx = raw.lastIndexOf(':')
        if (idx <= 0 || idx == raw.length - 1) return null
        val host = raw.substring(0, idx)
        val port = raw.substring(idx + 1).toIntOrNull() ?: return null
        if (port !in 1..65535) return null
        return InetSocketAddress(host, port)
    }
}

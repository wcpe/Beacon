package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import net.md_5.bungee.api.config.ServerInfo
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.proxy.ProxyServerDirectory
import java.net.InetSocketAddress
import java.util.concurrent.ConcurrentHashMap

/** BungeeCord ServerInfo 目录实现，只管理 Beacon 创建过的条目。 */
class BungeeServerDirectory : ProxyServerDirectory {
    private val managed: MutableSet<String> = ConcurrentHashMap.newKeySet()
    private val replaced: MutableMap<String, ServerInfo> = ConcurrentHashMap()

    override fun hasServer(serverId: String): Boolean {
        return ProxyServer.getInstance().servers.containsKey(serverId)
    }

    override fun isManaged(serverId: String): Boolean = managed.contains(serverId)

    override fun upsertManaged(instance: ServiceInstance): Boolean {
        // 同名手工服也允许接管：覆盖 ServerInfo 地址并记入 managed（启动占位项可被 discovery 接管）
        val address = parseAddress(instance.address()) ?: return false
        val id = instance.serverId()
        // 已受管且地址未变：幂等，不重写、不刷 INFO
        if (isManaged(id)) {
            val existing = ProxyServer.getInstance().servers[id]?.socketAddress
            if (existing is InetSocketAddress &&
                existing.hostString == address.hostString &&
                existing.port == address.port
            ) {
                return false
            }
        }
        val info =
            ProxyServer.getInstance().constructServerInfo(
                id,
                address,
                "Beacon 管理子服 $id",
                false,
            )
        ProxyServer.getInstance().servers[id]?.let { replaced.putIfAbsent(id, it) }
        ProxyServer.getInstance().servers[id] = info
        managed.add(id)
        return true
    }

    override fun removeManaged(serverId: String) {
        if (!managed.remove(serverId)) return
        restoreServer(serverId)
    }

    override fun resetManaged() {
        managed.toList().forEach(::restoreServer)
        managed.clear()
    }

    /** 仅返回本轮 Beacon 接管的后端，避免首次大厅路由落到同名手工配置。 */
    fun managedServerInfo(serverId: String): ServerInfo? {
        if (!managed.contains(serverId)) return null
        return ProxyServer.getInstance().servers[serverId]
    }

    /**
     * 当前代理已知的全部后端子服 serverId 集合（FR-36）：取 BungeeCord 服务器目录 keys，
     * 含 Beacon 注入与手工配置的子服——即本代理「实际能转发到」的后端事实，供拓扑连线消费。
     * 返回防御性副本，避免外部修改代理目录视图。
     */
    override fun backendServerIds(): Set<String> {
        return ProxyServer.getInstance().servers.keys.toSet()
    }

    private fun restoreServer(serverId: String) {
        val original = replaced.remove(serverId)
        if (original == null) {
            ProxyServer.getInstance().servers.remove(serverId)
        } else {
            ProxyServer.getInstance().servers[serverId] = original
        }
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

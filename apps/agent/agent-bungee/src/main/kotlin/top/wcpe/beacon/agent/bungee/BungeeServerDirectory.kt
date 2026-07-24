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
        ProxyServer.getInstance().servers[id] = info
        managed.add(id)
        return true
    }

    override fun removeManaged(serverId: String) {
        if (!managed.remove(serverId)) return
        ProxyServer.getInstance().servers.remove(serverId)
    }

    /**
     * 把 [serverId] 设为 BungeeCord 默认/fallback 服（FR-48）：置于每个监听器 server-priority 列表首位。
     *
     * BungeeCord 的默认/fallback 服由各监听器 `ListenerInfo.getServerPriority()` 列表决定——玩家加入按列表
     * 顺序挑首个可达服。这里用公开 API、不反射、不碰非公开实现 jar：从每个监听器拿到可变 priority 列表，
     * 先移除同名条目再插到首位（幂等去重），不删运维原有的其它 priority 条目。serverId 须已在 servers 中
     * （由 upsertManaged 注入）才会真正可达。
     */
    override fun setDefaultServer(serverId: String) {
        for (listener in ProxyServer.getInstance().config.listeners) {
            val priorities = listener.serverPriority
            // 去重：先移除已有同名条目（避免重复添加），再置首位。
            priorities.remove(serverId)
            priorities.add(0, serverId)
        }
    }

    /**
     * 当前代理已知的全部后端子服 serverId 集合（FR-36）：取 BungeeCord 服务器目录 keys，
     * 含 Beacon 注入与手工配置的子服——即本代理「实际能转发到」的后端事实，供拓扑连线消费。
     * 返回防御性副本，避免外部修改代理目录视图。
     */
    override fun backendServerIds(): Set<String> {
        return ProxyServer.getInstance().servers.keys.toSet()
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

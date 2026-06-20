package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/** Proxy 服务器目录隔离接口，方便测试 ServerInfo 同步逻辑。 */
interface ProxyServerDirectory {
    fun hasServer(serverId: String): Boolean
    fun isManaged(serverId: String): Boolean
    fun upsertManaged(instance: ServiceInstance): Boolean
    fun removeManaged(serverId: String)

    /**
     * 当前代理已知的后端子服 serverId 集合（FR-36 事实）：供 bc 上报「自身后端归属」给控制面、
     * 由拓扑 bc→bukkit 连线消费（FR-37）。返回当前快照，调用方不得修改。
     */
    fun backendServerIds(): Set<String>
}

package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/** Proxy 服务器目录隔离接口，方便测试 ServerInfo 同步逻辑。 */
interface ProxyServerDirectory {
    fun hasServer(serverId: String): Boolean
    fun isManaged(serverId: String): Boolean
    fun upsertManaged(instance: ServiceInstance): Boolean
    fun removeManaged(serverId: String)
}

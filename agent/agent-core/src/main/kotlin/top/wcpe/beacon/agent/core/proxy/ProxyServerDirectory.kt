package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/** Proxy 服务器目录隔离接口，方便测试 ServerInfo 同步逻辑。 */
interface ProxyServerDirectory {
    fun hasServer(serverId: String): Boolean
    fun isManaged(serverId: String): Boolean
    fun upsertManaged(instance: ServiceInstance): Boolean
    fun removeManaged(serverId: String)

    /**
     * 把 [serverId] 设为 BungeeCord 默认/fallback 服（FR-48）：置于每个监听器 server-priority 列表首位，
     * 让玩家加入时优先落到它。serverId 须已在服务器目录中（先 upsert 注入再设默认才有效）。
     * 实现须幂等去重（重复设同一服不重复添加）、不删运维原有 priority 条目。
     * 默认空实现：bukkit 等无代理目录的平台 / 测试桩不动作（设默认服仅对 BC 有意义）。
     */
    fun setDefaultServer(serverId: String) {
        // 默认不动作：仅 BC 代理目录需设默认/fallback 服。
    }

    /**
     * 当前代理已知的后端子服 serverId 集合（FR-36 事实）：供 bc 上报「自身后端归属」给控制面、
     * 由拓扑 bc→bukkit 连线消费（FR-37）。返回当前快照，调用方不得修改。
     */
    fun backendServerIds(): Set<String>
}

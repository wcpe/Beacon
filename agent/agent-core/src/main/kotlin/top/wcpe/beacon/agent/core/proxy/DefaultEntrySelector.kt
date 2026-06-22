package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/**
 * BC 默认服选择纯逻辑（FR-48）：从本轮发现结果里选出该代理应设的默认/fallback 服 serverId。
 *
 * 只认「该 home-zone 在 Beacon 显式配置的默认入口」：必须同时满足
 * 1. 配齐 home-zone（[homeGroup] + [homeZone] 均非空）；
 * 2. 发现结果里有命中该小区、被控制面标为默认入口（`zoneDefaultEntry=true`）、且在线的 bukkit。
 * 任一不满足 → 返回 null（由调用方决定不设默认服 + 告警），**绝不**回退到任意在线 bukkit——
 * 静默把玩家落到非大厅服会跳过大厅、造成风险（用户拍板移除原「取首个在线 bukkit」兜底）。
 *
 * 纯函数、无副作用，单元可穷举。home-zone 是 BC 自身数据面路由配置（不是 zone 归属声明，不违反 ADR-0004）。
 */
object DefaultEntrySelector {

    private const val ROLE_BUKKIT = "bukkit"
    private const val STATUS_ONLINE = "online"

    /**
     * @param instances 本轮发现结果（任意角色 / 状态；本函数内部只看在线 bukkit）
     * @param homeGroup BC 服务的大区（空串=未配）
     * @param homeZone  BC 服务的小区（空串=未配）
     * @return 命中 home-zone 显式配置的在线默认入口 serverId；未配 / 无命中 / 不在线时为 null
     */
    fun select(instances: List<ServiceInstance>, homeGroup: String, homeZone: String): String? {
        if (homeGroup.isBlank() || homeZone.isBlank()) {
            return null
        }
        return instances.firstOrNull {
            it.role() == ROLE_BUKKIT &&
                it.status() == STATUS_ONLINE &&
                it.zoneDefaultEntry() &&
                it.group() == homeGroup &&
                it.zone() == homeZone
        }?.serverId()
    }
}

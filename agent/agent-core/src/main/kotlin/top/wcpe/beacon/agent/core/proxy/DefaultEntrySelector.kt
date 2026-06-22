package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/**
 * BC 默认服选择纯逻辑（FR-48）：从本轮发现的在线 bukkit 子服里选出该代理应设的默认/fallback 服 serverId。
 *
 * 选择优先级（fail-static、修住 P0「玩家进不去」）：
 * 1. 配了 home-zone（[homeGroup] + [homeZone] 均非空）且发现结果里有命中该小区、被控制面标为默认入口
 *    （`zoneDefaultEntry=true`）的在线 bukkit → 选它（运维指定的稳定落点）；
 * 2. 否则取本轮在线 bukkit 中的第一个（按入参顺序，确定性兜底——至少能进）；
 * 3. 一个在线 bukkit 都没有 → 返回 null（不设默认服，等下一轮）。
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
     * @return 应设为默认/fallback 服的 serverId；无可用在线 bukkit 时为 null
     */
    fun select(instances: List<ServiceInstance>, homeGroup: String, homeZone: String): String? {
        val onlineBukkit = instances.filter { it.role() == ROLE_BUKKIT && it.status() == STATUS_ONLINE }
        if (onlineBukkit.isEmpty()) {
            return null
        }
        // 1. home-zone 命中的默认入口优先（仅当 home-zone 配齐时才匹配）。
        if (homeGroup.isNotBlank() && homeZone.isNotBlank()) {
            val preferred = onlineBukkit.firstOrNull {
                it.zoneDefaultEntry() && it.group() == homeGroup && it.zone() == homeZone
            }
            if (preferred != null) {
                return preferred.serverId()
            }
        }
        // 2. 兜底：第一个在线 bukkit（确定性顺序）。
        return onlineBukkit.first().serverId()
    }
}

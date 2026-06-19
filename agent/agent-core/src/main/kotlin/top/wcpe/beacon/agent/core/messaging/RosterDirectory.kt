package top.wcpe.beacon.agent.core.messaging

/**
 * 玩家位置名册只读端口（FR-31 / ADR-0022：把已躺在 agent 侧 Redis 的名册作为「事实」只读暴露）。
 *
 * 名册由 BC 上的 beacon-proxy 维护并写入 Redis（玩家名→所在子服 serverId，见 [RedisPlayerRoster]）；
 * 本端口供 [top.wcpe.beacon.agent.core.api.DiscoveryView] 全表读，组合控制面权威 zone 集做过滤。
 * core 只依赖本抽象，Redis 读取（HGETALL）在适配器，core 不 import Jedis（守 ADR-0005）。
 *
 * 与既有 [PlayerLocator]（单个解析 resolveServerId）分立不合并：职责不同（全表读 vs 单个寻址）。
 *
 * 一致性取舍（简单优先）：名册最终一致，换服瞬间快照可能短暂错位（沿用 ADR-0016 决策 5），
 * 业务插件须容忍瞬时偏差。
 */
interface RosterDirectory {

    /**
     * 读取全量名册快照（玩家名 → 所在子服 serverId）。
     *
     * @return 当前名册全表；名册不可用 / 为空时返回空 Map（绝不返 null、绝不抛）
     */
    fun snapshot(): Map<String, String>
}

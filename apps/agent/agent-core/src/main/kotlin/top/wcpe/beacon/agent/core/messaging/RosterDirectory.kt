package top.wcpe.beacon.agent.core.messaging

/**
 * 玩家位置名册只读端口（FR-31）。
 *
 * **名册权威在控制面**（ADR-0063 决策 4，取代 ADR-0016 的 agent 侧 Redis 名册）：控制面依连接明细
 * 在内存维护玩家位置快照，agent 侧不再持有 Redis 名册，故本端口在 v2 装配下无实现注入（见
 * [RosterDirectoryHolder]，`snapshot()` 恒返空）。保留接口签名以守向后兼容。
 *
 * 本端口供 [top.wcpe.beacon.agent.core.api.DiscoveryView] 全表读，组合控制面权威 zone 集做过滤；
 * 若后续按 ADR-0063 接回控制面名册，读取实现应在适配器、core 只依赖本抽象（守 ADR-0005）。
 *
 * 与既有 [PlayerLocator]（单个解析 resolveServerId）分立不合并：职责不同（全表读 vs 单个寻址）。
 *
 * 一致性取舍（简单优先）：名册最终一致，换服瞬间快照可能短暂错位，业务插件须容忍瞬时偏差。
 */
interface RosterDirectory {
    /**
     * 读取全量名册快照（玩家名 → 所在子服 serverId）。
     *
     * @return 当前名册全表；名册不可用 / 为空时返回空 Map（绝不返 null、绝不抛）
     */
    fun snapshot(): Map<String, String>
}

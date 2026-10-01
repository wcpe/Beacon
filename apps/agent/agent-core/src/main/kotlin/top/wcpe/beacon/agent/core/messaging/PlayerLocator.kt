package top.wcpe.beacon.agent.core.messaging

/**
 * 玩家位置解析端口（ADR-0016 决策 5：按玩家寻址依赖「玩家→所在子服」名册）。
 *
 * **本端口属 Legacy Redis 通道**：名册原由 BC 上的 beacon-proxy 维护并写入 Redis，供
 * [MessageBus.sendToPlayer] 解析目标服。ADR-0063 决策 4 已把「玩家→所在服」的解析权威迁至控制面
 * （依连接明细维护内存快照），HTTP 中转路径下按玩家寻址**不经本端口**（[MessageBus] 的 playerLocator
 * 为 null，交控制面解析）。保留本抽象与适配器实现，只为 Legacy Redis 通道兼容。
 *
 * core 只依赖本抽象，具体读取在适配器。
 *
 * 一致性取舍（简单优先）：接受换服瞬间短暂错位；解析落空（玩家已不在）返回 null，
 * 由 [MessageBus] 走「找不到目标」兜底，不上强一致。
 */
interface PlayerLocator {
    /**
     * 解析玩家当前所在子服 serverId。
     *
     * @param playerName 玩家名
     * @return 所在子服 serverId；名册无此玩家返回 null
     */
    fun resolveServerId(playerName: String): String?
}

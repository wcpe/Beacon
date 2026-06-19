package top.wcpe.beacon.agent.core.messaging

/**
 * 玩家位置解析端口（ADR-0016 决策 5：按玩家寻址依赖「玩家→所在子服」名册）。
 *
 * 名册由 BC 上的 beacon-proxy 维护并写入 Redis；本端口供 [MessageBus.sendToPlayer] 解析目标服。
 * core 只依赖本抽象，Redis 读取在适配器。
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

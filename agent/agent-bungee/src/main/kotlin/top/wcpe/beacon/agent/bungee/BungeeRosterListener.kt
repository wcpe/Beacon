package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.PlayerDisconnectEvent
import net.md_5.bungee.api.event.ServerConnectedEvent
import taboolib.common.platform.event.SubscribeEvent

/**
 * Bungee 玩家位置事件监听（FR-26）：把进服/换服/退出映射为名册更新。
 *
 * - [ServerConnectedEvent]：玩家连接到某子服（首次进服与换服都触发）→ 登记当前所在服。
 * - [PlayerDisconnectEvent]：玩家断开 → 删除名册（仅当当前所在服与上次已知一致，换服误删保护在 roster 内）。
 *
 * 事件回调可能在 Bungee 事件线程；名册写入是轻量 Redis 短操作，且 roster 内部容错（异常仅告警），
 * 不阻断玩家连接（fail-static）。
 *
 * 注意：本监听需真机（BungeeCord + Redis）验证；本地无法跑事件链路。
 */
object BungeeRosterListener {

    /** 名册引导引用；由主类在 ENABLE 时注入，未注入时事件为空操作。 */
    @Volatile
    var bootstrap: BungeePlayerRosterBootstrap? = null

    @SubscribeEvent
    fun onServerConnected(event: ServerConnectedEvent) {
        val playerName = event.player?.name ?: return
        val serverName = event.server?.info?.name ?: return
        bootstrap?.onPlayerLocated(playerName, serverName)
    }

    @SubscribeEvent
    fun onPlayerDisconnect(event: PlayerDisconnectEvent) {
        val player = event.player ?: return
        val playerName = player.name ?: return
        // 退出时玩家当前所在服（若仍可读）即其最后所在服；整体断开时读不到则为空串，
        // roster 对空来源服无条件删（已离线、无换服误删风险），非空才按名册当前值比对判定。
        val serverName = player.server?.info?.name ?: ""
        bootstrap?.onPlayerQuit(playerName, serverName)
    }
}

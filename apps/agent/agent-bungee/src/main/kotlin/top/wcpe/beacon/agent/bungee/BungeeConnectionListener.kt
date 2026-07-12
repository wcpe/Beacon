package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.PlayerDisconnectEvent
import net.md_5.bungee.api.event.PostLoginEvent
import net.md_5.bungee.api.event.ServerConnectedEvent
import taboolib.common.platform.event.SubscribeEvent
import top.wcpe.beacon.agent.core.connection.ProxyConnectionTracker
import java.net.InetSocketAddress

/**
 * Bungee 连接事件监听（FR-145）：把玩家登入 / 后端切换 / 登出映射为连接会话追踪器调用，产出 open/close 事件入缓冲。
 *
 * - [PostLoginEvent]：玩家登入代理 → onConnect（生成 connId、发 open 事件）。
 * - [ServerConnectedEvent]：连接到某后端子服（首次进服与换服都触发）→ onBackend（累加会话首末后端与切换数摘要）。
 * - [PlayerDisconnectEvent]：断开 → onDisconnect（用同 connId 发 close 事件）。
 *
 * 埋点零成本（tracker 内仅 map + UUID 生成，无阻塞 IO），可在 BC 事件线程直接调用，绝不阻塞 MC/BC 主线程。
 * 与 [BungeeRosterListener]（FR-31 名册）事件源同类但职责不同（连接明细采集 vs 玩家寻址名册），各自独立。
 *
 * 注意：本监听需真机（BungeeCord）验证；本地无法跑事件链路。
 */
object BungeeConnectionListener {
    /** 会话追踪器引用；由主类在 ENABLE 时注入，未注入时事件为空操作。 */
    @Volatile
    var tracker: ProxyConnectionTracker? = null

    @SubscribeEvent
    fun onPostLogin(event: PostLoginEvent) {
        val player = event.player ?: return
        val uuid = player.uniqueId?.toString() ?: return
        val clientIp = (player.socketAddress as? InetSocketAddress)?.address?.hostAddress
        val protocol = player.pendingConnection?.version
        tracker?.onConnect(uuid, player.name ?: uuid, clientIp, protocol)
    }

    @SubscribeEvent
    fun onServerConnected(event: ServerConnectedEvent) {
        val player = event.player ?: return
        val uuid = player.uniqueId?.toString() ?: return
        val serverName = event.server?.info?.name ?: return
        tracker?.onBackend(uuid, serverName)
    }

    @SubscribeEvent
    fun onPlayerDisconnect(event: PlayerDisconnectEvent) {
        val player = event.player ?: return
        val uuid = player.uniqueId?.toString() ?: return
        // BC 断开事件不区分正常退出 / 被踢，统一按 quit（closeKind 精细化交控制面 / 后续演进）；proxy_shutdown 由控制面据 bootId 变更补 close。
        tracker?.onDisconnect(uuid, "quit", null)
    }
}

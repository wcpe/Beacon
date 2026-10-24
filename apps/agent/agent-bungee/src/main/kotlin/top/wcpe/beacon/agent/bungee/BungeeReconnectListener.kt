package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.config.ServerInfo
import net.md_5.bungee.api.event.ServerConnectEvent
import net.md_5.bungee.api.event.ServerKickEvent
import taboolib.common.platform.event.SubscribeEvent
import taboolib.common.platform.function.info
import top.wcpe.beacon.agent.core.proxy.ReconnectRoute
import top.wcpe.beacon.agent.core.proxy.ReconnectRouter

/**
 * 承接子服关闭后的玩家回退：先把玩家送回原所在服的同区大厅，再落到全局登录大厅池（随机）。
 *
 * ## 为什么接两个事件
 *
 * BungeeCord 对「玩家离开子服」有两条互斥的源码路径（`DownstreamBridge`）：
 *
 * 1. **子服主动踢人** → `handle(Kick)` → 触发 [ServerKickEvent]（`State.CONNECTED`）。
 *    子服正常 `stop` 时多半走这条。
 * 2. **子服进程消失 / 连接断** → `disconnected(ChannelWrapper)` → 不产生 kick 事件，
 *    而是直接走原生回退：`connectNow(def, SERVER_DOWN_REDIRECT)`。
 *    崩溃、被 kill、网络断都走这条。**只接事件 1 会漏掉玩家最常遇到的「整台子服挂掉」。**
 *
 * ## 为什么选 `ServerConnectEvent.setTarget` 而不是改内部状态
 *
 * 两条路径最终都会收敛到同一个公开事件 [ServerConnectEvent]：
 *
 * | 来源 | Reason |
 * |---|---|
 * | 子服崩溃后的原生回退 | `SERVER_DOWN_REDIRECT` |
 * | 我们在 [ServerKickEvent] 里 `cancelServer` 之后的跳转 | `KICK_REDIRECT` |
 *
 * 该事件实现 `Cancellable` 且 `target` 可写，替换目标即可，**无需触碰任何内部类**
 * （如 `ServerConnection.setObsolete`），也不依赖反射。用 Reason 过滤还天然排除了
 * 玩家主动 `/server`（`COMMAND`）、插件跳转（`PLUGIN`）等无关场景。
 *
 * ## 已知边界
 *
 * 原生回退只在「listener 的 `priorities` 里还能找到第二个候选」时才会发起跳转。
 * 若 `priorities` 只配了玩家当前所在的那一台，`updateAndGetNextServer` 返回 null，
 * BungeeCord 直接断开连接、不产生任何事件，本能力无从接管。
 * 故该场景要求 `priorities` 覆盖全部大厅服（与多大厅做容灾的运维惯例一致）。
 */
object BungeeReconnectListener {
    @Volatile
    private var router: ReconnectRouter? = null

    @Volatile
    private var directory: BungeeServerDirectory? = null

    fun start(
        router: ReconnectRouter,
        directory: BungeeServerDirectory,
    ) {
        this.router = router
        this.directory = directory
        info("子服回退路由已启用（接管 ServerKickEvent + ServerConnectEvent）")
    }

    fun stop() {
        router = null
        directory = null
    }

    /** 路径一：子服主动 kick（含正常 stop 时的 "Server closed"）。 */
    @SubscribeEvent
    fun onServerKick(event: ServerKickEvent) {
        val player = event.player ?: return
        val selected =
            ReconnectDispatch.forKick(event.state, event.isCancelled, event.kickedFrom?.name, ::routeOf) ?: return
        val target = managedTarget(selected) ?: return

        info("回退路由（kick）：玩家=${player.name} 从 ${event.kickedFrom?.name} 断开 → ${target.name}")
        // 两处都要设：cancelServer 指定落点，isCancelled 才是让 BungeeCord 跳过原 disconnect 的开关。
        event.cancelServer = target
        event.isCancelled = true
    }

    /**
     * 路径二：子服进程消失后，BungeeCord 发起的原生回退跳转。
     *
     * 接管范围（只认 `SERVER_DOWN_REDIRECT`）与差异判断都在 [ReconnectDispatch.forConnect] 里，
     * 这里只负责把平台对象翻译进去、把结果写回事件。
     */
    @SubscribeEvent
    fun onServerConnect(event: ServerConnectEvent) {
        val player = event.player ?: return
        // 此刻玩家仍挂在掉线的那台服上，用它作为「原所在服」做区感知；
        // 取不到时退化为按全局登录池选路（ReconnectRouter 对 null 的处理）。
        val from = runCatching { player.server?.info?.name }.getOrNull()
        val selected = ReconnectDispatch.forConnect(event.reason, from, event.target?.name, ::routeOf) ?: return
        val target = managedTarget(selected) ?: return

        info("回退路由（server-down）：玩家=${player.name} 原服=${from ?: "未知"} 重定向 ${event.target?.name} → ${target.name}")
        event.target = target
    }

    /** 向 router 求选路结果；未装配时退化为 `Unavailable`，等价于「不干预」。 */
    private fun routeOf(fromServerId: String?): ReconnectRoute =
        router?.route(fromServerId, setOfNotNull(fromServerId)) ?: ReconnectRoute.Unavailable

    /** 把选中的 serverId 翻译成可用的 [ServerInfo]；不在受管目录内时返回 null（交还原生回退）。 */
    private fun managedTarget(selected: ReconnectRoute.Selected): ServerInfo? {
        info("回退路由命中层级=${selected.reason}，目标=${selected.serverId}")
        return directory?.managedServerInfo(selected.serverId)
    }
}

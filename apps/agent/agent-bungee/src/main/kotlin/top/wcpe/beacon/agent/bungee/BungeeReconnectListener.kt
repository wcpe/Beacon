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
        // 仅接管「玩家已在服内、该服断开」；连接阶段失败交给原生重试链。
        if (event.state != ServerKickEvent.State.CONNECTED || event.isCancelled) return

        val player = event.player ?: return
        val from = event.kickedFrom?.name ?: return
        val target = resolveTarget(from) ?: return

        info("回退路由（kick）：玩家=${player.name} 从 $from 断开 → ${target.name}")
        event.cancelServer = target
        event.isCancelled = true
    }

    /**
     * 路径二：子服进程消失后，BungeeCord 发起的原生回退跳转。
     *
     * 只处理 `SERVER_DOWN_REDIRECT`——这正是「子服掉了、把人送去 fallback」的语义，
     * 与玩家主动换服（`COMMAND`）和插件跳转（`PLUGIN`）天然区分开。
     */
    @SubscribeEvent
    fun onServerConnect(event: ServerConnectEvent) {
        if (event.reason != ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT) return

        val player = event.player ?: return
        // 此刻玩家仍挂在掉线的那台服上，用它作为「原所在服」做区感知；
        // 取不到时退化为按全局登录池选路（ReconnectRouter 对 null 的处理）。
        val from = runCatching { player.server?.info?.name }.getOrNull()
        val target = resolveTarget(from) ?: return
        if (target.name == event.target?.name) return

        info("回退路由（server-down）：玩家=${player.name} 原服=${from ?: "未知"} 重定向 ${event.target?.name} → ${target.name}")
        event.target = target
    }

    /** 按原所在服求回退目标；未装配、无候选或目标不在受管目录时返回 null（交还原生回退）。 */
    private fun resolveTarget(fromServerId: String?): ServerInfo? {
        val route = router?.route(fromServerId, setOfNotNull(fromServerId)) as? ReconnectRoute.Selected ?: return null
        val target = directory?.managedServerInfo(route.serverId) ?: return null
        info("回退路由命中层级=${route.reason}，目标=${route.serverId}")
        return target
    }
}

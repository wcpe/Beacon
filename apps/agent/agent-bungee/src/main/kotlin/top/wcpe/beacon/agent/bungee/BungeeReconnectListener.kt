package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.ServerKickEvent
import taboolib.common.platform.event.SubscribeEvent
import top.wcpe.beacon.agent.core.proxy.ReconnectRoute
import top.wcpe.beacon.agent.core.proxy.ReconnectRouter

/**
 * 承接子服关闭后的玩家回退：先把玩家送回原所在服的同区大厅，再落到全局登录大厅池（随机）。
 *
 * 只处理 [ServerKickEvent.State.CONNECTED]（玩家已在子服内、该服异常断开）：
 * 连接阶段的失败由 BungeeCord 自身的重试链负责，这里不接管，避免与原生逻辑重复。
 * 选不到目标时保持事件原样放行，让原生 `priorities` 回退继续生效（fail-static）。
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
    }

    fun stop() {
        router = null
        directory = null
    }

    @SubscribeEvent
    fun onServerKick(event: ServerKickEvent) {
        // 仅接管「玩家已在服内、该服异常断开」；连接阶段失败交给原生重试链。
        if (event.state != ServerKickEvent.State.CONNECTED) return
        if (event.isCancelled) return
        val kickedFrom = event.kickedFrom?.name ?: return
        val currentRouter = router ?: return
        val currentDirectory = directory ?: return
        val route = currentRouter.route(kickedFrom, setOf(kickedFrom))
        if (route !is ReconnectRoute.Selected) return
        val target = currentDirectory.managedServerInfo(route.serverId) ?: return
        event.cancelServer = target
        event.isCancelled = true
    }
}

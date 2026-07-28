package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.ServerConnectEvent
import taboolib.common.platform.event.SubscribeEvent
import top.wcpe.beacon.agent.core.proxy.InitialLobbyRoute
import top.wcpe.beacon.agent.core.proxy.InitialLobbyRouter

/** 仅接管玩家首次连入代理时的大厅落脚，不干预任何后续业务转服。 */
object BungeeInitialLobbyListener {
    @Volatile
    private var router: InitialLobbyRouter? = null

    @Volatile
    private var directory: BungeeServerDirectory? = null

    fun start(
        router: InitialLobbyRouter,
        directory: BungeeServerDirectory,
    ) {
        this.directory = directory
        this.router = router
    }

    fun stop() {
        router = null
        directory = null
    }

    @SubscribeEvent
    fun onServerConnect(event: ServerConnectEvent) {
        if (event.reason != ServerConnectEvent.Reason.JOIN_PROXY || event.player.server != null) return
        when (val route = router?.route() ?: return) {
            is InitialLobbyRoute.Selected -> routeToManagedLobby(event, route.serverId)
            is InitialLobbyRoute.Rejected -> rejectFirstEntry(event)
        }
    }

    private fun routeToManagedLobby(
        event: ServerConnectEvent,
        serverId: String,
    ) {
        val target = directory?.managedServerInfo(serverId)
        if (target == null) {
            rejectFirstEntry(event)
            return
        }
        event.target = target
    }

    private fun rejectFirstEntry(event: ServerConnectEvent) {
        event.isCancelled = true
        event.player.disconnect("当前没有可用大厅，请稍后重试")
    }
}

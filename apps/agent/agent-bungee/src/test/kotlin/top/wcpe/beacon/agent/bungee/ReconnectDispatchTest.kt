package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.ServerConnectEvent
import net.md_5.bungee.api.event.ServerKickEvent
import top.wcpe.beacon.agent.core.proxy.ReconnectRoute
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * 回退决策层单测。
 *
 * 关注点不是「选路算法选得对不对」（那由 agent-core 的 `ReconnectRouterTest` 覆盖），
 * 而是**接管范围**：哪些事件该管、哪些必须放过——这是本监听器最容易改错、
 * 又只有真机才能观察到的部分。
 */
class ReconnectDispatchTest {
    private fun selected(serverId: String) = ReconnectRoute.Selected(serverId, ReconnectRoute.Target.LOGIN_POOL)

    // ---------- 踢人路径 ----------

    @Test
    fun `已连接态的踢人事件被接管`() {
        val got =
            ReconnectDispatch.forKick(ServerKickEvent.State.CONNECTED, false, "game-2") {
                selected("lobby-1")
            }
        assertEquals("lobby-1", got?.serverId)
    }

    @Test
    fun `连接阶段的踢人事件交还原生重试链`() {
        // 连接阶段失败本该由 BungeeCord 自己重试，抢过来会把玩家从一次还能继续的登录流程里拽走。
        val got =
            ReconnectDispatch.forKick(ServerKickEvent.State.CONNECTING, false, "game-2") {
                selected("lobby-1")
            }
        assertNull(got)
    }

    @Test
    fun `已被取消的踢人事件不再改写`() {
        // 尊重先到者，避免两个插件互相覆盖落点形成拉锯。
        val got =
            ReconnectDispatch.forKick(ServerKickEvent.State.CONNECTED, true, "game-2") {
                selected("lobby-1")
            }
        assertNull(got)
    }

    @Test
    fun `踢人事件缺少原服时不干预`() {
        val got =
            ReconnectDispatch.forKick(ServerKickEvent.State.CONNECTED, false, null) {
                selected("lobby-1")
            }
        assertNull(got)
    }

    @Test
    fun `踢人路径无可用目标时不干预`() {
        val got =
            ReconnectDispatch.forKick(ServerKickEvent.State.CONNECTED, false, "game-2") {
                ReconnectRoute.Unavailable
            }
        assertNull(got)
    }

    // ---------- 崩溃路径 ----------

    @Test
    fun `崩溃路径的原生回退被接管`() {
        val got =
            ReconnectDispatch.forConnect(
                ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT,
                "game-2",
                "lobby-2",
            ) { selected("lobby-1") }
        assertEquals("lobby-1", got?.serverId)
    }

    @Test
    fun `玩家主动换服与插件跳转都不接管`() {
        val reasons =
            listOf(
                ServerConnectEvent.Reason.COMMAND,
                ServerConnectEvent.Reason.PLUGIN,
                ServerConnectEvent.Reason.PLUGIN_MESSAGE,
                ServerConnectEvent.Reason.JOIN_PROXY,
                ServerConnectEvent.Reason.UNKNOWN,
            )
        for (reason in reasons) {
            val got = ReconnectDispatch.forConnect(reason, "game-2", "lobby-2") { selected("lobby-1") }
            assertNull(got, "reason=$reason 不应被接管")
        }
    }

    @Test
    fun `原生大厅兜底重试不接管`() {
        // LOBBY_FALLBACK 是 BungeeCord 自己的大厅重试，语义与「子服掉线」不同，交给它自己跑。
        val got =
            ReconnectDispatch.forConnect(
                ServerConnectEvent.Reason.LOBBY_FALLBACK,
                "game-2",
                "lobby-2",
            ) { selected("lobby-1") }
        assertNull(got)
    }

    @Test
    fun `目标与原生选择相同时不写回`() {
        // 选择一致时改写是纯噪音，还会让「是否真的介入」变得不可观测。
        val got =
            ReconnectDispatch.forConnect(
                ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT,
                "game-2",
                "lobby-1",
            ) { selected("lobby-1") }
        assertNull(got)
    }

    @Test
    fun `原服未知时把 null 交给选路以退化到登录池`() {
        var seen: String? = "未被调用"
        val got =
            ReconnectDispatch.forConnect(
                ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT,
                null,
                "lobby-2",
            ) { from ->
                seen = from
                selected("lobby-9")
            }
        // router 必须收到 null（而非空串或占位值），才可能走全局登录池这一级。
        assertNull(seen)
        assertEquals("lobby-9", got?.serverId)
    }

    @Test
    fun `崩溃路径无可用目标时不干预`() {
        val got =
            ReconnectDispatch.forConnect(
                ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT,
                "game-2",
                "lobby-2",
            ) { ReconnectRoute.Unavailable }
        assertNull(got)
    }
}

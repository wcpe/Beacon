package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.event.ServerConnectEvent
import net.md_5.bungee.api.event.ServerKickEvent
import top.wcpe.beacon.agent.core.proxy.ReconnectRoute

/**
 * 回退路由的**决策层**：把「这个事件该不该接管、接管到哪台服」从事件处理里剥离出来。
 *
 * 抽出来的目的有两个：
 *
 * 1. 决策只吃枚举与字符串，不碰 `ProxiedPlayer` / `ServerInfo` 这类必须持有代理实例才能构造的平台对象，
 *    于是「只接管哪些事件」「目标与原生选择相同时不写回」这些最容易改错的约束能被普通单测覆盖住；
 * 2. 让 [BungeeReconnectListener] 退化成「读事件 → 问决策 → 写事件」，平台耦合面降到最低。
 *
 * 本文件**不得引入 TabooLib 依赖**，否则测试就得拉起 TabooLib 运行期。
 */
internal object ReconnectDispatch {
    /**
     * 踢人路径（[ServerKickEvent]）的决策。
     *
     * 只接管 `CONNECTED` 态：连接阶段的 kick（`CONNECTING` 等）属于原生重试链的职责，
     * 抢过来会把玩家从一次本来还能继续的登录流程里拽走。
     *
     * @param fromServerId 玩家被踢前所在的服；取不到时不干预（无法判断区属，宁可不插手）。
     * @return 命中则为选中的目标（带层级用于日志），`null` 表示不干预、交还原生行为。
     */
    fun forKick(
        state: ServerKickEvent.State,
        cancelled: Boolean,
        fromServerId: String?,
        route: (String?) -> ReconnectRoute,
    ): ReconnectRoute.Selected? {
        if (cancelled) return null
        if (state != ServerKickEvent.State.CONNECTED) return null
        val from = fromServerId ?: return null
        return route(from) as? ReconnectRoute.Selected
    }

    /**
     * 崩溃路径（原生 `SERVER_DOWN_REDIRECT`）的决策。
     *
     * 用 `Reason` 过滤，而不是靠事件来源去猜：这样玩家主动 `/server`（`COMMAND`）、
     * 插件跳转（`PLUGIN` / `PLUGIN_MESSAGE`）、登录入服（`JOIN_PROXY`）、
     * 以及 BungeeCord 自己的大厅兜底重试（`LOBBY_FALLBACK`）天然被排除在外。
     *
     * @param fromServerId 玩家掉线前所在服；`null` 时交给 router 走全局登录池。
     * @param nativeTargetId BungeeCord 自己算出的回退目标；与我们的选择相同时不写回。
     * @return 命中则为选中的目标，`null` 表示不干预。
     */
    fun forConnect(
        reason: ServerConnectEvent.Reason,
        fromServerId: String?,
        nativeTargetId: String?,
        route: (String?) -> ReconnectRoute,
    ): ReconnectRoute.Selected? {
        if (reason != ServerConnectEvent.Reason.SERVER_DOWN_REDIRECT) return null
        val selected = route(fromServerId) as? ReconnectRoute.Selected ?: return null
        // 与原生选择一致时不做无意义写回，避免多一层可观测噪音与无谓的目标替换。
        // 此处拿 serverId 直接比目标名，依赖 BungeeCord 的不变量：constructServerInfo(id, …) 以 id 作为 name，
        // 故 ServerInfo.name 恒等于受管目录里的 serverId。若将来引入别名映射，这条比较需改为比对解析后的 name。
        if (selected.serverId == nativeTargetId) return null
        return selected
    }
}

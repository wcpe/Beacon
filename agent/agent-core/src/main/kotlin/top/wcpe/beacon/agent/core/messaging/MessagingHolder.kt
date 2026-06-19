package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.api.Messaging

/**
 * 当前生效的 [Messaging] 门面持有者（volatile，跨线程可见）。
 *
 * 默认 [DisabledMessaging]；消息模块启动成功后由壳层 [set] 为活跃的 [MessagingView]，
 * 关闭 / 重连失败时可 [reset] 回 Disabled。BeaconAgentImpl.messaging() 始终返回当前值（非 null）。
 */
class MessagingHolder {

    @Volatile
    private var current: Messaging = DisabledMessaging

    /** 当前门面（非 null）。 */
    fun get(): Messaging = current

    /** 切换为活跃门面。 */
    fun set(messaging: Messaging) {
        current = messaging
    }

    /** 复位为禁用门面。 */
    fun reset() {
        current = DisabledMessaging
    }
}

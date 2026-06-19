package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.MessageHandler
import top.wcpe.beacon.agent.api.Messaging
import top.wcpe.beacon.agent.api.TopicHandler
import java.util.concurrent.CompletableFuture

/**
 * 消息模块关闭（messaging.enabled=false 或冷启动未取得 Redis 配置）时的门面：
 * isAvailable=false，发送类方法抛 IllegalStateException，订阅/注册返回空操作句柄。
 *
 * 让业务插件统一以 isAvailable() 判断后再用，模块关时优雅降级而非 NPE。
 */
object DisabledMessaging : Messaging {

    override fun isAvailable(): Boolean = false

    override fun send(targetServerId: String, type: String, payload: Any?) = unavailable()

    override fun call(targetServerId: String, type: String, payload: Any?): CompletableFuture<Any?> {
        val future = CompletableFuture<Any?>()
        future.completeExceptionally(IllegalStateException(REASON))
        return future
    }

    override fun publish(topic: String, payload: Any?) = unavailable()

    override fun subscribe(topic: String, handler: TopicHandler): ListenerHandle = NOOP_HANDLE

    override fun sendToPlayer(playerName: String, type: String, payload: Any?): Boolean = false

    override fun on(type: String, handler: MessageHandler): ListenerHandle = NOOP_HANDLE

    private fun unavailable(): Nothing = throw IllegalStateException(REASON)

    private const val REASON = "跨服消息模块未启用（messaging.enabled=false 或 Redis 配置未下发）"

    private val NOOP_HANDLE = ListenerHandle { }
}

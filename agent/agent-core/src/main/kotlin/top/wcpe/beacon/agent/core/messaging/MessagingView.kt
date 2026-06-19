package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.api.IncomingMessage
import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.MessageHandler
import top.wcpe.beacon.agent.api.Messaging
import top.wcpe.beacon.agent.api.TopicHandler
import java.util.concurrent.CompletableFuture

/**
 * [Messaging] Java 门面的 core 实现：把对外调用委托给 [MessageBus]。
 *
 * 把 Kotlin 侧的 MessageContext / Message 适配为 Java API 的 IncomingMessage，
 * 业务插件无需感知 core 内部类型。
 */
class MessagingView(private val bus: MessageBus) : Messaging {

    override fun isAvailable(): Boolean = bus.isAvailable()

    override fun send(targetServerId: String, type: String, payload: Any?) {
        bus.send(targetServerId, type, payload)
    }

    override fun call(targetServerId: String, type: String, payload: Any?): CompletableFuture<Any?> {
        return bus.call(targetServerId, type, payload)
    }

    override fun publish(topic: String, payload: Any?) {
        bus.publish(topic, payload)
    }

    override fun subscribe(topic: String, handler: TopicHandler): ListenerHandle {
        bus.subscribe(topic) { message -> handler.handle(topic, message.payload) }
        return ListenerHandle { bus.unsubscribe(topic) }
    }

    override fun sendToPlayer(playerName: String, type: String, payload: Any?): Boolean {
        return bus.sendToPlayer(playerName, type, payload)
    }

    override fun on(type: String, handler: MessageHandler): ListenerHandle {
        bus.on(type) { context -> handler.handle(ContextIncomingMessage(context)) }
        // 注销 = 注册一个忽略一切的空处理器（覆盖前者）。core 不暴露移除单 type 的 API，保持简单。
        return ListenerHandle { bus.on(type) { } }
    }

    /** 把 MessageContext 适配为 Java API 的 IncomingMessage。 */
    private class ContextIncomingMessage(private val context: MessageContext) : IncomingMessage {
        override fun type(): String = context.message.type
        override fun payload(): Any? = context.payload()
        override fun source(): String? = context.message.source
        override fun isRequest(): Boolean = context.isRequest()
        override fun reply(payload: Any?) = context.reply(payload)
    }
}

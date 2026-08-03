package top.wcpe.beacon.agent.core.messaging

// 消息总线入站分发扩展：收件流 / 回信通道 / 主题订阅的原始回调解码与路由。
// 从 MessageBus 拆出以控制类内函数数量（TooManyFunctions）。

/** 收件流入站（Redis 通道回调）：解码后走统一分发。 */
internal fun MessageBus.onInboundRaw(raw: String) {
    val message = decode(raw) ?: return
    deliverInbound(message)
}

/** 回信入站（Redis 回信通道回调）：按 correlationId 唤醒等待的 Future。HTTP 中转不用此路（回信走收件流）。 */
internal fun MessageBus.onReplyRaw(raw: String) {
    val message = decode(raw) ?: return
    val correlationId = message.correlationId
    if (correlationId == null) {
        warn("回信缺 correlationId，丢弃 source=${message.source}")
        return
    }
    pending.remove(correlationId)?.complete(message.payload)
}

/** RPC 响应：唤醒挂起 Future；无主（请求已超时清理）则静默受理，绝不 type 路由。 */
internal fun MessageBus.completeResponse(
    correlationId: String,
    message: Message,
): InboundOutcome {
    pending.remove(correlationId)?.complete(message.payload)
    return InboundOutcome.delivered(null)
}

/** 路由到按 type 注册的处理器；无处理器告警并回 failed。 */
internal fun MessageBus.routeToHandler(message: Message): InboundOutcome {
    val handler = typeHandlers[message.type]
    if (handler == null) {
        warn("无处理器的消息类型：type=${message.type} source=${message.source}，丢弃")
        return InboundOutcome.failed("no_handler_for_type")
    }
    val context = MessageContext(message, this)
    val startNanos = System.nanoTime()
    return try {
        handler(context)
        InboundOutcome.delivered((System.nanoTime() - startNanos) / 1_000_000L)
    } catch (t: Throwable) {
        warn("消息处理器抛异常：type=${message.type}，已隔离，错误=${t.message}")
        InboundOutcome.failed(t.message ?: "handler_error")
    }
}

/**
 * 广播入站（FR-180）：按 topic（落信封 type）路由本地订阅分发表，与定向 on(type) 分发表隔离。
 * 无订阅者回 delivered——广播 fan-out 及本 namespace 全部在线服，订阅与否是各服本地状态，
 * 不订阅不构成投递失败（pub/sub 可丢语义）；订阅 handler 抛异常回 failed（计入广播聚合 failed_count）。
 */
internal fun MessageBus.routeToTopicHandler(message: Message): InboundOutcome {
    val handler = topicHandlers[message.type] ?: return InboundOutcome.delivered(null)
    val startNanos = System.nanoTime()
    return try {
        handler(message)
        InboundOutcome.delivered((System.nanoTime() - startNanos) / 1_000_000L)
    } catch (t: Throwable) {
        warn("主题处理器抛异常：topic=${message.type}，已隔离，错误=${t.message}")
        InboundOutcome.failed(t.message ?: "handler_error")
    }
}

/** 主题入站（Redis 通道订阅回调）：解码 → 回调该 topic 处理器。 */
internal fun MessageBus.onTopicRaw(
    topic: String,
    raw: String,
) {
    val message = decode(raw) ?: return
    val handler = topicHandlers[topic] ?: return
    try {
        handler(message)
    } catch (t: Throwable) {
        warn("主题处理器抛异常：topic=$topic，已隔离，错误=${t.message}")
    }
}

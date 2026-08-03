package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.id.Uuid7

// 消息总线出站编排扩展：定向 / RPC / 主题 / 回信的出站发送与异步执行。
// 从 MessageBus 拆出以控制类内函数数量（TooManyFunctions）

/** 由 [MessageContext.reply] 调用：把响应发回请求方。Redis 走回信通道，HTTP 中转按 source 定向发一条带 correlationId 的消息。
 *  上行经 [outboundExecutor] 异步执行，绝不阻塞调用者（含 MC 主线程）。 */
internal fun MessageBus.reply(
    request: Message,
    payload: Any?,
) {
    val response =
        Message(
            type = request.type,
            payload = payload,
            correlationId = request.correlationId,
            source = selfServerId,
            messageId = Uuid7.generate(),
            sentAt = System.currentTimeMillis(),
        )
    val replyTo = request.replyTo
    val encoded = encode(response)
    if (replyTo != null) {
        this.submitOutbound { transport.sendReply(replyTo, encoded) }
        return
    }
    val target = request.source ?: return
    val outbound = response.copy(targetKind = Message.TARGET_SERVER, targetId = target)
    val outboundEncoded = encode(outbound)
    this.submitOutbound { transport.sendToServer(target, outboundEncoded) }
}

internal fun MessageBus.dispatchOutbound(
    targetKind: String,
    targetId: String,
    type: String,
    payload: Any?,
) {
    val message =
        Message(
            type = type,
            payload = payload,
            source = selfServerId,
            messageId = Uuid7.generate(),
            sentAt = System.currentTimeMillis(),
            targetKind = targetKind,
            targetId = targetId,
        )
    // targetId 兼作 transport 的目标参数：Redis 用它选收件流；HTTP 适配器改读信封 targetKind/targetId 建 wire 目标。
    transport.sendToServer(targetId, encode(message))
}

/**
 * 把出站阻塞调用（transport.send/publish/reply）丢到 [outboundExecutor] 异步线程，
 * 绝不阻塞调用者（含 MC 主线程）；fire-and-forget 语义下发送失败仅 warn 日志。
 */
internal fun MessageBus.submitOutbound(task: () -> Unit) {
    outboundExecutor {
        try {
            task()
        } catch (t: Throwable) {
            warn("跨服消息出站发送失败：${t.message ?: "无错误信息"}")
        }
    }
}

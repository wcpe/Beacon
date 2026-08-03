package top.wcpe.beacon.agent.core.messaging

// 消息总线编解码与工具扩展：信封编解码、可用性校验、payload 大小检查、挂起 Future 清理。
// 从 MessageBus 拆出以控制类内函数数量（TooManyFunctions）

/** 注册按类型分发的处理器。重复注册同 type 覆盖前者。 */
fun MessageBus.on(
    type: String,
    handler: (MessageContext) -> Unit,
) {
    typeHandlers[type] = handler
}

internal fun MessageBus.requireAvailable() {
    check(isAvailable()) { "跨服消息模块不可用（未启用或控制面消息通道未就绪）" }
}

/** payload 上限前置校验：超限本地直接失败，不发无谓请求（spec §5.1 / §8-6）。null payload 视作 0 字节直接放行。 */
internal fun MessageBus.checkPayloadSize(payload: Any?) {
    if (payload == null) return
    val bytes = codec.encode(payload).toByteArray(Charsets.UTF_8).size
    require(bytes <= MessageBus.MAX_PAYLOAD_BYTES) {
        "跨服消息 payload 超过 ${MessageBus.MAX_PAYLOAD_BYTES} 字节上限（实际 $bytes 字节），本地拒绝发送"
    }
}

internal fun MessageBus.failAllPending(error: Throwable) {
    val ids = pending.keys.toList()
    for (id in ids) {
        pending.remove(id)?.completeExceptionally(error)
    }
}

internal fun MessageBus.encode(message: Message): String = codec.encode(message.toMap())

/** 解码原始 json 为信封；非法消息（缺 type / 解析失败）告警并返回 null。 */
internal fun MessageBus.decode(raw: String): Message? {
    val message =
        try {
            Message.fromMap(codec.decode(raw))
        } catch (t: Throwable) {
            warn("消息解码失败，丢弃，错误=${t.message}")
            return null
        }
    if (message == null) {
        warn("非法消息（缺 type 或非对象），丢弃")
    }
    return message
}

package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.id.Uuid7
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.CompletableFuture
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeoutException

/**
 * 跨服消息总线（core 引擎）：信封编解码、按 type 路由分发、RPC 关联 ID 配对 + 超时、
 * 定向 / RPC / 主题 / 按玩家寻址的编排。
 *
 * P5 起底层传输由 Redis 换为控制面 HTTP 单跳中转（ADR-0063）：上行经 [MessageTransport] 送控制面、
 * 下行由 [MessagePollCoordinator][top.wcpe.beacon.agent.core.messaging.MessagePollCoordinator] 长轮询取回后逐条调 [deliverInbound]。
 * 因 HTTP 单通道无独立回信信道，RPC 请求以「correlationId 自引用其 messageId」为标记、响应回填该 messageId，
 * 分发时按 correlationId 前置区分响应与请求（[deliverInbound] 三路）。旧 Redis 双通道路径保留兼容（onInboundRaw/onReplyRaw）。
 *
 * 只依赖抽象：[MessageTransport]（搬运原始 json）、[JsonCodec]（编解码）、[PlayerLocator]（本地名册寻址，仅 Redis 通道用）。
 * 不 import 任何具体库（Redis/okhttp/kotlinx），守 ADR-0005/0016 边界。
 *
 * 线程：发送方法可在任意线程调用（内部仅编码 + 委托 transport）。入站消息由长轮询 / 订阅后台线程回调，
 * handler 在该后台线程同步执行（绝不上 MC 主线程；handler 自行切回平台线程）。
 *
 * @param transport     传输端口（HTTP 适配器注入；测试注入假实现）
 * @param codec         信封 json 编解码
 * @param selfServerId  本服 serverId（回信定向、source 标识用）
 * @param settings      运行参数（RPC 超时等）
 * @param playerLocator 玩家位置解析（仅 Redis 通道注入；HTTP 中转下为 null，按玩家寻址交控制面解析）
 * @param scheduleTimeout 延迟调度（RPC 超时清理用）：默认 daemon 线程，壳层可注入平台 runAsyncDelayed
 * @param warn          告警日志（无法配对的回信、非法消息等）
 */
class MessageBus(
    private val transport: MessageTransport,
    private val codec: JsonCodec,
    private val selfServerId: String,
    private val settings: MessagingSettings,
    private val playerLocator: PlayerLocator? = null,
    private val scheduleTimeout: (delayMs: Long, task: () -> Unit) -> Unit = DEFAULT_SCHEDULER,
    private val warn: (String) -> Unit = {},
) {
    /** 按消息类型注册的处理器：type → handler。非 RPC 收消息后回调，返回值忽略。 */
    private val typeHandlers = ConcurrentHashMap<String, (MessageContext) -> Unit>()

    /** 主题处理器：topic → handler。 */
    private val topicHandlers = ConcurrentHashMap<String, (Message) -> Unit>()

    /** 等待回信的 RPC 请求：请求 messageId → Future。 */
    private val pending = ConcurrentHashMap<String, CompletableFuture<Any?>>()

    /** 本服专属回信通道名（Redis 通道用；HTTP 中转不投递此，回信按 source 定向）。 */
    private val replyChannel: String = "$REPLY_PREFIX$selfServerId"

    @Volatile
    private var started = false

    /**
     * 启动：连 transport、订阅本服收件流与回信通道。失败抛异常由上层降级（isAvailable 仍为 false）。
     */
    fun start() {
        transport.start()
        transport.subscribeServerInbox { raw -> onInboundRaw(raw) }
        transport.subscribeReplyInbox(replyChannel) { raw -> onReplyRaw(raw) }
        started = true
    }

    /** 关闭：失败所有挂起 Future、关 transport。 */
    fun close() {
        started = false
        failAllPending(IllegalStateException("消息总线已关闭"))
        transport.close()
    }

    /** 模块是否可用（已启动且 transport 已连上）。业务侧据此优雅降级。 */
    fun isAvailable(): Boolean = started && transport.isConnected()

    /** 注册按类型分发的处理器。重复注册同 type 覆盖前者。 */
    fun on(
        type: String,
        handler: (MessageContext) -> Unit,
    ) {
        typeHandlers[type] = handler
    }

    /**
     * 定向发送（fire-and-forget）：向目标子服投递一条单向消息。
     *
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限（本地前置拒绝，不发无谓请求）
     */
    fun send(
        targetServerId: String,
        type: String,
        payload: Any?,
    ) {
        requireAvailable()
        checkPayloadSize(payload)
        dispatchOutbound(Message.TARGET_SERVER, targetServerId, type, payload)
    }

    /**
     * 请求-响应（RPC）：发请求并返回 Future，目标回信后完成；超时则 Future 异常完成。
     *
     * @return 完成值为目标返回的 payload（泛型树）；超时抛 [TimeoutException]
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限
     */
    fun call(
        targetServerId: String,
        type: String,
        payload: Any?,
    ): CompletableFuture<Any?> {
        requireAvailable()
        checkPayloadSize(payload)
        // correlationId 自引用 messageId：作为 RPC 请求标记与关联键，响应回填此值（spec §4.2 / §3.3）。
        val messageId = Uuid7.generate()
        val future = CompletableFuture<Any?>()
        pending[messageId] = future

        val request =
            Message(
                type = type,
                payload = payload,
                correlationId = messageId,
                replyTo = replyChannel,
                source = selfServerId,
                messageId = messageId,
                sentAt = System.currentTimeMillis(),
                targetKind = Message.TARGET_SERVER,
                targetId = targetServerId,
            )
        try {
            transport.sendToServer(targetServerId, encode(request))
        } catch (t: Throwable) {
            // 发送失败立刻清理，不留悬挂 Future。
            pending.remove(messageId)
            future.completeExceptionally(t)
            return future
        }

        // 超时兜底：到点仍未完成则异常完成并清理（响应过期即弃）。
        scheduleTimeout(settings.rpcTimeoutMs) {
            val removed = pending.remove(messageId)
            if (removed != null && !removed.isDone) {
                removed.completeExceptionally(
                    TimeoutException("RPC 超时：target=$targetServerId type=$type 超过 ${settings.rpcTimeoutMs}ms 未收回信"),
                )
            }
        }
        return future
    }

    /**
     * 主题发布（可丢广播，FR-180 / ADR-0065 复活 ADR-0063 §7 的 no-op 条款）：topic 落 msg_type，
     * HTTP 中转经控制面按当前在线服集合 fan-out（含发送者自身；离线不补投）；Redis 通道仍走原 pub/sub。
     *
     * @param zone 可选 zone 级定向：非空只投该 zone 当前在线服（仅 HTTP 中转生效，Redis 通道无 zone 概念）
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限（本地前置拒绝，不发无谓请求）
     */
    fun publish(
        topic: String,
        payload: Any?,
        zone: String? = null,
    ) {
        requireAvailable()
        checkPayloadSize(payload)
        val message =
            Message(
                type = topic,
                payload = payload,
                source = selfServerId,
                messageId = Uuid7.generate(),
                sentAt = System.currentTimeMillis(),
                targetKind = Message.TARGET_BROADCAST,
                targetId = zone,
                broadcast = true,
            )
        transport.publishTopic(topic, encode(message))
    }

    /**
     * 主题订阅：登记本地 topic 分发表（与定向 on(type) 分发表隔离）并向 transport 订阅。
     * HTTP 中转下广播经长轮询取回、按信封 broadcast 标记路由到本表（[deliverInbound]），
     * transport 侧订阅为 no-op；Redis 通道仍走真订阅回调。
     *
     * @throws IllegalStateException 模块不可用
     */
    fun subscribe(
        topic: String,
        handler: (Message) -> Unit,
    ) {
        requireAvailable()
        topicHandlers[topic] = handler
        transport.subscribeTopic(topic) { raw -> onTopicRaw(topic, raw) }
    }

    /** 取消主题订阅。 */
    fun unsubscribe(topic: String) {
        topicHandlers.remove(topic)
        transport.unsubscribeTopic(topic)
    }

    /**
     * 按玩家寻址：
     * - 注入了 [PlayerLocator]（Redis 通道）：本地名册解析所在服后定向发送。
     * - 未注入（HTTP 中转，ADR-0063 §4 名册权威在控制面）：发一条按玩家寻址消息，由控制面据名册快照解析目标服。
     *
     * @return Redis 通道：true=已解析投递，false=名册无此玩家；HTTP 中转：恒 true（是否在线由控制面回执/状态判定）
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限
     */
    fun sendToPlayer(
        playerName: String,
        type: String,
        payload: Any?,
    ): Boolean {
        requireAvailable()
        checkPayloadSize(payload)
        val locator = playerLocator
        // HTTP 中转：不注入本地名册，发按玩家寻址消息，交控制面按连接明细名册快照解析（玩家不在线 → 控制面记 failed）。
        if (locator == null) {
            dispatchOutbound(Message.TARGET_PLAYER, playerName, type, payload)
            return true
        }
        // Redis 通道：本地名册解析所在服后定向。
        val serverId = locator.resolveServerId(playerName)
        if (serverId != null) {
            dispatchOutbound(Message.TARGET_SERVER, serverId, type, payload)
        } else {
            warn("按玩家寻址落空：玩家 $playerName 不在名册（可能已换服/离线），丢弃 type=$type")
        }
        return serverId != null
    }

    // ---- 入站分发 ----

    /**
     * 分发一条入站消息（HTTP 长轮询协调器逐条调用；Redis 收件流回调经 [onInboundRaw] 亦走此）。
     *
     * 广播（信封 broadcast 标记，FR-180）前置分流到 topic 订阅分发表，与定向三路隔离。定向三路
     * （HTTP 单通道下响应与请求同路，故按 correlationId 前置区分）：
     * - RPC 响应（correlationId 非空且不等于自身 messageId）→ 唤醒挂起 Future，绝不再当请求路由（杜绝响应回环）。
     * - RPC 请求（correlationId 自引用其 messageId）→ 路由 type 处理器，handler 可回信。
     * - 单向 send（correlationId 为 null）→ 路由 type 处理器。
     *
     * @return 回执结果（供 HTTP 协调器 ack）：delivered / failed + 失败原因 + handler 耗时
     */
    fun deliverInbound(message: Message): InboundOutcome {
        if (message.broadcast) {
            return routeToTopicHandler(message)
        }
        val correlationId = message.correlationId
        if (correlationId != null && correlationId != message.messageId) {
            return completeResponse(correlationId, message)
        }
        return routeToHandler(message)
    }

    /** 收件流入站（Redis 通道回调）：解码后走统一分发。 */
    private fun onInboundRaw(raw: String) {
        val message = decode(raw) ?: return
        deliverInbound(message)
    }

    /** 回信入站（Redis 回信通道回调）：按 correlationId 唤醒等待的 Future。HTTP 中转不用此路（回信走收件流）。 */
    private fun onReplyRaw(raw: String) {
        val message = decode(raw) ?: return
        val correlationId = message.correlationId
        if (correlationId == null) {
            warn("回信缺 correlationId，丢弃 source=${message.source}")
            return
        }
        pending.remove(correlationId)?.complete(message.payload)
    }

    /** RPC 响应：唤醒挂起 Future；无主（请求已超时清理）则静默受理，绝不 type 路由。 */
    private fun completeResponse(
        correlationId: String,
        message: Message,
    ): InboundOutcome {
        pending.remove(correlationId)?.complete(message.payload)
        return InboundOutcome.delivered(null)
    }

    /** 路由到按 type 注册的处理器；无处理器告警并回 failed。 */
    private fun routeToHandler(message: Message): InboundOutcome {
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
    private fun routeToTopicHandler(message: Message): InboundOutcome {
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
    private fun onTopicRaw(
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

    /** 由 [MessageContext.reply] 调用：把响应发回请求方。Redis 走回信通道，HTTP 中转按 source 定向发一条带 correlationId 的消息。 */
    internal fun reply(
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
        if (replyTo != null) {
            transport.sendReply(replyTo, encode(response))
            return
        }
        val target = request.source ?: return
        val outbound = response.copy(targetKind = Message.TARGET_SERVER, targetId = target)
        transport.sendToServer(target, encode(outbound))
    }

    private fun dispatchOutbound(
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

    private fun requireAvailable() {
        check(isAvailable()) { "跨服消息模块不可用（未启用或控制面消息通道未就绪）" }
    }

    /** payload 上限前置校验：超限本地直接失败，不发无谓请求（spec §5.1 / §8-6）。null payload 视作 0 字节直接放行。 */
    private fun checkPayloadSize(payload: Any?) {
        if (payload == null) return
        val bytes = codec.encode(payload).toByteArray(Charsets.UTF_8).size
        require(bytes <= MAX_PAYLOAD_BYTES) {
            "跨服消息 payload 超过 $MAX_PAYLOAD_BYTES 字节上限（实际 $bytes 字节），本地拒绝发送"
        }
    }

    private fun failAllPending(error: Throwable) {
        val ids = pending.keys.toList()
        for (id in ids) {
            pending.remove(id)?.completeExceptionally(error)
        }
    }

    private fun encode(message: Message): String = codec.encode(message.toMap())

    /** 解码原始 json 为信封；非法消息（缺 type / 解析失败）告警并返回 null。 */
    private fun decode(raw: String): Message? {
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

    companion object {
        /** 回信通道前缀（Redis 通道用）：本服回信通道 = reply:<serverId>。 */
        private const val REPLY_PREFIX: String = "reply:"

        /** payload 字节上限（默认 64KB，spec §3.4）：超限发送请求本地直接拒绝。 */
        const val MAX_PAYLOAD_BYTES: Int = 64 * 1024

        /** 默认超时调度用的 daemon 定时器（单例，全 bus 共享）。 */
        private val TIMEOUT_TIMER = java.util.Timer("beacon-rpc-timeout", true)

        /**
         * 默认超时调度器：daemon 单线程定时器。壳层应注入平台 runAsyncDelayed 以复用平台调度，
         * 测试可注入受控调度器以确定性触发超时。
         */
        val DEFAULT_SCHEDULER: (Long, () -> Unit) -> Unit = { delayMs, task ->
            TIMEOUT_TIMER.schedule(
                object : java.util.TimerTask() {
                    override fun run() = task()
                },
                delayMs,
            )
        }
    }
}

/**
 * 入站消息分发结果（供 HTTP 长轮询协调器回执 ack）。
 *
 * @property status delivered（handler 处理完 / 响应已唤醒）或 failed（无处理器 / handler 抛异常）
 * @property reason 失败原因（脱敏文案）；delivered 为 null
 * @property handlerCostMs handler 处理耗时毫秒；响应唤醒 / 无耗时为 null
 */
data class InboundOutcome(
    val status: String,
    val reason: String?,
    val handlerCostMs: Long?,
) {
    companion object {
        const val STATUS_DELIVERED: String = "delivered"
        const val STATUS_FAILED: String = "failed"

        fun delivered(handlerCostMs: Long?): InboundOutcome = InboundOutcome(STATUS_DELIVERED, null, handlerCostMs)

        fun failed(reason: String): InboundOutcome = InboundOutcome(STATUS_FAILED, reason, null)
    }
}

/**
 * 入站消息上下文：把信封交给 handler，并在 RPC 请求时提供 [reply] 回信能力。
 *
 * @property message 收到的信封
 */
class MessageContext internal constructor(
    val message: Message,
    private val bus: MessageBus,
) {
    /** 本消息是否为 RPC 请求（带 correlationId，期待回信）。 */
    fun isRequest(): Boolean = message.isRequest()

    /** 业务负载（泛型树）。 */
    fun payload(): Any? = message.payload

    /**
     * 回信（仅对 RPC 请求有效；非请求调用无副作用）。
     *
     * @param payload 响应负载（泛型树）
     */
    fun reply(payload: Any?) {
        if (!message.isRequest()) return
        bus.reply(message, payload)
    }
}

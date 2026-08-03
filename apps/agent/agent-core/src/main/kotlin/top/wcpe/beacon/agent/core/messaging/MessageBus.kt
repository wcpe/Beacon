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
 * 线程：发送方法可在任意线程调用，**上行 transport 调用经 [outboundExecutor] 丢到异步线程，绝不阻塞调用者
 * （含 MC 主线程）**。入站消息由长轮询 / 订阅后台线程回调，handler 在该后台线程同步执行
 * （绝不上 MC 主线程；handler 自行切回平台线程）。
 *
 * @param transport     传输端口（HTTP 适配器注入；测试注入假实现）
 * @param codec         信封 json 编解码
 * @param selfServerId  本服 serverId（回信定向、source 标识用）
 * @param settings      运行参数（RPC 超时等）
 * @param playerLocator 玩家位置解析（仅 Redis 通道注入；HTTP 中转下为 null，按玩家寻址交控制面解析）
 * @param scheduleTimeout 延迟调度（RPC 超时清理用）：默认 daemon 线程，壳层可注入平台 runAsyncDelayed
 * @param outboundExecutor 出站执行器：把 transport 阻塞调用（send/publish/reply）丢到异步线程，
 *   绝不阻塞调用者（含 MC 主线程）；默认同步执行器（测试向后兼容），生产由壳层注入 adapter::runAsync
 * @param warn          告警日志（无法配对的回信、非法消息等）
 */
class MessageBus(
    internal val transport: MessageTransport,
    internal val codec: JsonCodec,
    internal val selfServerId: String,
    internal val settings: MessagingSettings,
    internal val playerLocator: PlayerLocator? = null,
    internal val scheduleTimeout: (delayMs: Long, task: () -> Unit) -> Unit = DEFAULT_SCHEDULER,
    internal val outboundExecutor: (task: () -> Unit) -> Unit = { it() },
    internal val warn: (String) -> Unit = {},
) {
    /** 按消息类型注册的处理器：type → handler。非 RPC 收消息后回调，返回值忽略。 */
    internal val typeHandlers = ConcurrentHashMap<String, (MessageContext) -> Unit>()

    /** 主题处理器：topic → handler。 */
    internal val topicHandlers = ConcurrentHashMap<String, (Message) -> Unit>()

    /** 等待回信的 RPC 请求：请求 messageId → Future。 */
    internal val pending = ConcurrentHashMap<String, CompletableFuture<Any?>>()

    /** 本服专属回信通道名（Redis 通道用；HTTP 中转不投递此，回信按 source 定向）。 */
    internal val replyChannel: String = "$REPLY_PREFIX$selfServerId"

    @Volatile
    internal var started = false

    /**
     * 启动：连 transport、订阅本服收件流与回信通道。失败抛异常由上层降级（isAvailable 仍为 false）。
     */
    fun start() {
        transport.start()
        transport.subscribeServerInbox { raw -> this.onInboundRaw(raw) }
        transport.subscribeReplyInbox(replyChannel) { raw -> this.onReplyRaw(raw) }
        started = true
    }

    /** 关闭：失败所有挂起 Future、关 transport。 */
    fun close() {
        started = false
        this.failAllPending(IllegalStateException("消息总线已关闭"))
        transport.close()
    }

    /** 模块是否可用（已启动且 transport 已连上）。业务侧据此优雅降级。 */
    fun isAvailable(): Boolean = started && transport.isConnected()

    /**
     * 定向发送（fire-and-forget）：向目标子服投递一条单向消息。
     *
     * 上行经 [outboundExecutor] 异步执行，绝不阻塞调用者（含 MC 主线程）；发送失败仅 warn 日志。
     *
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限（本地前置拒绝，不发无谓请求）
     */
    fun send(
        targetServerId: String,
        type: String,
        payload: Any?,
    ) {
        this.requireAvailable()
        this.checkPayloadSize(payload)
        this.submitOutbound { this.dispatchOutbound(Message.TARGET_SERVER, targetServerId, type, payload) }
    }

    /**
     * 请求-响应（RPC）：发请求并立即返回 Future，目标回信后完成；超时则 Future 异常完成
     * （{@link java.util.concurrent.TimeoutException}）。
     *
     * 上行经 [outboundExecutor] 异步执行，绝不阻塞调用者（含 MC 主线程）；发送失败异步 completeExceptionally。
     *
     * @return 完成值为目标返回的 payload（泛型树）
     * @throws IllegalStateException 模块不可用
     * @throws IllegalArgumentException payload 超过上限
     */
    fun call(
        targetServerId: String,
        type: String,
        payload: Any?,
    ): CompletableFuture<Any?> {
        this.requireAvailable()
        this.checkPayloadSize(payload)
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
        // 上行发送丢异步线程，绝不阻塞调用者；发送失败异步 completeExceptionally 并清理 pending。
        outboundExecutor {
            try {
                transport.sendToServer(targetServerId, this.encode(request))
            } catch (t: Throwable) {
                pending.remove(messageId)
                future.completeExceptionally(t)
            }
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
     * 上行经 [outboundExecutor] 异步执行，绝不阻塞调用者（含 MC 主线程）；发送失败仅 warn 日志。
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
        this.requireAvailable()
        this.checkPayloadSize(payload)
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
        val encoded = this.encode(message)
        this.submitOutbound { transport.publishTopic(topic, encoded) }
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
        this.requireAvailable()
        topicHandlers[topic] = handler
        transport.subscribeTopic(topic) { raw -> this.onTopicRaw(topic, raw) }
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
     * 上行经 [outboundExecutor] 异步执行，绝不阻塞调用者（含 MC 主线程）。
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
        this.requireAvailable()
        this.checkPayloadSize(payload)
        val locator = playerLocator
        // HTTP 中转：不注入本地名册，发按玩家寻址消息，交控制面按连接明细名册快照解析（玩家不在线 → 控制面记 failed）。
        if (locator == null) {
            this.submitOutbound { this.dispatchOutbound(Message.TARGET_PLAYER, playerName, type, payload) }
            return true
        }
        // Redis 通道：本地名册解析所在服（内存操作，不阻塞）后定向。
        val serverId = locator.resolveServerId(playerName)
        if (serverId != null) {
            this.submitOutbound { this.dispatchOutbound(Message.TARGET_SERVER, serverId, type, payload) }
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
            return this.routeToTopicHandler(message)
        }
        val correlationId = message.correlationId
        if (correlationId != null && correlationId != message.messageId) {
            return this.completeResponse(correlationId, message)
        }
        return this.routeToHandler(message)
    }

    companion object {
        /** 回信通道前缀（Redis 通道用）：本服回信通道 = reply:<serverId>。 */
        private const val REPLY_PREFIX: String = "reply:"

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

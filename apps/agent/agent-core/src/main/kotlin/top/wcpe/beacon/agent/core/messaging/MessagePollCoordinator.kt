package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.MessageAck
import top.wcpe.beacon.agent.core.client.MessageAckOutcome
import top.wcpe.beacon.agent.core.client.MessagePollOutcome
import top.wcpe.beacon.agent.core.client.PolledMessage
import top.wcpe.beacon.agent.core.client.ackMessages
import top.wcpe.beacon.agent.core.client.pollMessages
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * 跨服消息下行长轮询协调器（FR-149 §4.2 / ADR-0063）：单条自续杯「代」循环，持续 POST /messages/poll 取本服待投消息，
 * 逐条交 [MessageBus.deliverInbound] 分发本机订阅者 / 唤醒挂起 RPC future，再 POST /messages/ack 回执。
 *
 * 「代」模式与 [SchedulingRefresher][top.wcpe.beacon.agent.core.scheduling.SchedulingRefresher] 同构：[start] 幂等（重注册不重启）、
 * [stop] 令循环退出。全程 TabooLib async，绝不上 MC 主线程（业务 handler 若碰平台 API 自行切回）。
 * 长轮询已在服务端挂起 waitSec，故成功（含 204 无消息）立即续杯、连接失败退避后重连。warn-once-on-transition 防刷屏。
 * 降级：控制面不可用时循环退避重连、[MessageBus.isAvailable] 由 transport 连接态判定，消息面不本地缓冲重发（ADR-0063 §8）。
 *
 * @param now 时钟（deliveredAt 用；默认系统时钟，测试可注入）
 */
class MessagePollCoordinator(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val adapter: PlatformAdapter,
    private val bus: MessageBus,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    private val active = AtomicBoolean(false)
    private val pollGen = AtomicReference(0)
    private val lifecycleLock = Any()

    /** 长轮询挂起上限（秒，≤25）、单次取回上限（≤50）、连接失败重连退避（毫秒）：默认生产值，[configure] 供测试覆盖。 */
    @Volatile
    private var waitSec: Int = POLL_WAIT_SEC

    @Volatile
    private var maxMessages: Int = POLL_MAX

    @Volatile
    private var retryDelayMs: Long = RETRY_DELAY_MS

    /** 长轮询是否健康：首轮先做零等待探测，连续失败只在转入失败 / 恢复各告警一次。 */
    @Volatile
    private var healthy: Boolean = false

    @Volatile
    private var healthObserved: Boolean = false

    /** 由 [MessagingRuntime] 在启动前注入；协调器独立测试可保留空监听器。 */
    @Volatile
    internal var connectionStateListener: (Boolean) -> Unit = {}

    /** 配置长轮询参数（须在 [start] 前调用，无并发）。仅测试为加速覆盖。 */
    fun configure(
        waitSec: Int,
        maxMessages: Int,
        retryDelayMs: Long,
    ) {
        this.waitSec = waitSec
        this.maxMessages = maxMessages
        this.retryDelayMs = retryDelayMs
    }

    fun start() {
        val gen =
            synchronized(lifecycleLock) {
                if (!active.compareAndSet(false, true)) return
                (pollGen.get() + 1).also(pollGen::set)
            }
        schedulePoll(gen, 0)
    }

    fun stop() {
        synchronized(lifecycleLock) {
            active.set(false)
            pollGen.set(pollGen.get() + 1)
        }
    }

    private fun schedulePoll(
        gen: Int,
        delayMs: Long,
    ) {
        if (!isCurrent(gen)) return
        if (delayMs <= 0) {
            adapter.runAsync { runPollTask(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { runPollTask(gen) }
        }
    }

    private fun runPollTask(gen: Int) {
        try {
            if (!isCurrent(gen)) return
            val currentWaitSec = if (healthy) waitSec else 0
            val nextDelay = pollOnce(gen, currentWaitSec) ?: return
            schedulePoll(gen, nextDelay)
        } catch (t: Throwable) {
            val reason = "${t.javaClass.simpleName}: ${t.message ?: "无错误信息"}"
            try {
                onFailure(gen, reason)
            } finally {
                synchronized(lifecycleLock) {
                    if (isCurrent(gen)) {
                        active.set(false)
                        pollGen.set(pollGen.get() + 1)
                    }
                }
            }
            adapter.error("跨服消息长轮询任务异常，当前轮询代已停用", t)
        }
    }

    /**
     * 长轮询一次并分发回执。
     *
     * @return 下一轮续杯延迟：成功（含 204）立即续杯（0）；连接失败退避 [retryDelayMs]；代失效返回 null。
     */
    private fun pollOnce(
        gen: Int,
        currentWaitSec: Int,
    ): Long? {
        val outcome = apiClient.pollMessages(identity, currentWaitSec, maxMessages)
        if (!isCurrent(gen)) return null
        return when (outcome) {
            is MessagePollOutcome.Messages ->
                if (onRecover(gen)) dispatchAndAck(outcome.messages, gen) else retryDelayMs

            is MessagePollOutcome.Empty -> if (onRecover(gen)) 0L else retryDelayMs
            is MessagePollOutcome.Failed -> {
                onFailure(gen, outcome.reason)
                retryDelayMs
            }
        }
    }

    /** 逐条分发入站消息并批量回执；代失效时不再执行 handler 或 ack。 */
    private fun dispatchAndAck(
        messages: List<PolledMessage>,
        gen: Int,
    ): Long? {
        if (messages.isEmpty()) return 0L
        val acks = ArrayList<MessageAck>(messages.size)
        for (polled in messages) {
            if (!isCurrent(gen)) break
            val message =
                Message(
                    type = polled.msgType,
                    payload = polled.payload,
                    correlationId = polled.correlationId,
                    source = polled.sourceServerId,
                    messageId = polled.messageId,
                    broadcast = polled.broadcast,
                )
            val outcome = bus.deliverInbound(message)
            acks.add(MessageAck(polled.messageId, outcome.status, outcome.reason, now(), outcome.handlerCostMs))
        }
        val outcome = if (isCurrent(gen)) apiClient.ackMessages(identity, acks) else null
        return when (outcome) {
            null -> null
            is MessageAckOutcome.Applied -> 0L
            is MessageAckOutcome.Failed -> {
                onFailure(gen, outcome.reason)
                retryDelayMs
            }
        }
    }

    private fun onRecover(gen: Int): Boolean =
        synchronized(lifecycleLock) {
            if (!isCurrent(gen)) return@synchronized false
            try {
                connectionStateListener(true)
            } catch (t: Throwable) {
                onFailure(gen, "恢复消息通道失败：${t.message ?: "无错误信息"}")
                return@synchronized false
            }
            if (healthObserved && !healthy) adapter.info("跨服消息长轮询已恢复")
            healthy = true
            healthObserved = true
            true
        }

    private fun onFailure(
        gen: Int,
        reason: String,
    ) {
        synchronized(lifecycleLock) {
            if (!isCurrent(gen) || healthObserved && !healthy) return@synchronized
            healthy = false
            healthObserved = true
            try {
                connectionStateListener(false)
            } catch (t: Throwable) {
                adapter.error("跨服消息失联清理失败", t)
            }
            adapter.warn("跨服消息长轮询失败（$reason），退避后重连；后续同类失败不再刷屏")
        }
    }

    private fun isCurrent(gen: Int): Boolean = active.get() && gen == pollGen.get()

    companion object {
        /** 长轮询挂起上限（秒，spec §5.1 waitSec ≤25）。 */
        const val POLL_WAIT_SEC: Int = 20

        /** 单次取回上限（spec §5.1 max ≤50）。 */
        const val POLL_MAX: Int = 50

        /** 连接失败重连退避（毫秒）。 */
        const val RETRY_DELAY_MS: Long = 5_000L
    }
}

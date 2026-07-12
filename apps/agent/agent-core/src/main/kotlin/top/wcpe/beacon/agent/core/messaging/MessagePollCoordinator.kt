package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.MessageAck
import top.wcpe.beacon.agent.core.client.MessageAckOutcome
import top.wcpe.beacon.agent.core.client.MessagePollOutcome
import top.wcpe.beacon.agent.core.client.PolledMessage
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

    /** 长轮询挂起上限（秒，≤25）、单次取回上限（≤50）、连接失败重连退避（毫秒）：默认生产值，[configure] 供测试覆盖。 */
    @Volatile
    private var waitSec: Int = POLL_WAIT_SEC

    @Volatile
    private var maxMessages: Int = POLL_MAX

    @Volatile
    private var retryDelayMs: Long = RETRY_DELAY_MS

    /** 长轮询是否健康：连续失败只在转入失败 / 恢复各告警一次。 */
    @Volatile
    private var healthy: Boolean = true

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
        if (!active.compareAndSet(false, true)) return
        val gen = pollGen.get() + 1
        pollGen.set(gen)
        schedulePoll(gen, 0)
    }

    fun stop() {
        active.set(false)
    }

    private fun schedulePoll(
        gen: Int,
        delayMs: Long,
    ) {
        if (!active.get()) return
        if (delayMs <= 0) {
            adapter.runAsync { pollTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { pollTick(gen) }
        }
    }

    private fun pollTick(gen: Int) {
        if (!active.get() || gen != pollGen.get()) return
        val nextDelay = pollOnce()
        schedulePoll(gen, nextDelay)
    }

    /**
     * 长轮询一次并分发回执。
     *
     * @return 下一轮续杯延迟：成功（含 204）立即续杯（0）；连接失败退避 [retryDelayMs]。
     */
    private fun pollOnce(): Long =
        when (val outcome = apiClient.pollMessages(identity, waitSec, maxMessages)) {
            is MessagePollOutcome.Messages -> {
                onRecover()
                dispatchAndAck(outcome.messages)
                0L
            }

            is MessagePollOutcome.Empty -> {
                onRecover()
                0L
            }

            is MessagePollOutcome.Failed -> {
                onFailure(outcome.reason)
                retryDelayMs
            }
        }

    /** 逐条分发入站消息并批量回执（best-effort：ack 失败则控制面超时重投、下轮再 ack）。 */
    private fun dispatchAndAck(messages: List<PolledMessage>) {
        if (messages.isEmpty()) return
        val acks =
            messages.map { pm ->
                val message =
                    Message(
                        type = pm.msgType,
                        payload = pm.payload,
                        correlationId = pm.correlationId,
                        source = pm.sourceServerId,
                        messageId = pm.messageId,
                    )
                val outcome = bus.deliverInbound(message)
                MessageAck(
                    messageId = pm.messageId,
                    status = outcome.status,
                    reason = outcome.reason,
                    deliveredAtMs = now(),
                    handlerCostMs = outcome.handlerCostMs,
                )
            }
        if (apiClient.ackMessages(identity, acks) is MessageAckOutcome.Failed) {
            adapter.warn("跨服消息回执失败，控制面将超时重投、下轮取回后再 ack（best-effort）")
        }
    }

    private fun onRecover() {
        if (!healthy) {
            healthy = true
            adapter.info("跨服消息长轮询已恢复")
        }
    }

    private fun onFailure(reason: String) {
        if (!healthy) return
        healthy = false
        adapter.warn("跨服消息长轮询失败（$reason），退避后重连；后续同类失败不再刷屏")
    }

    companion object {
        /** 长轮询挂起上限（秒，spec §5.1 waitSec ≤25）。 */
        const val POLL_WAIT_SEC: Int = 20

        /** 单次取回上限（spec §5.1 max ≤50）。 */
        const val POLL_MAX: Int = 50

        /** 连接失败重连退避（毫秒）。 */
        const val RETRY_DELAY_MS: Long = 5_000L
    }
}

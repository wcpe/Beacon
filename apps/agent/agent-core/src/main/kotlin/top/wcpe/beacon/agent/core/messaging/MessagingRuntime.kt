package top.wcpe.beacon.agent.core.messaging

/**
 * 跨服消息模块运行时（HTTP 中转，ADR-0063）：随注册成功启动、停机停止，把活跃门面注入 [MessagingHolder]。
 *
 * 与配置同步 / 心跳故障域隔离：启动失败仅降级（holder 保持 Disabled），不抛到接入流程。未启用
 * （messaging.enabled=false）时保持降级——由 [top.wcpe.beacon.agent.core.settings.AgentSettings] 门控，
 * 真机由运维在 config.yml 打开。取代 Legacy 的 [MessagingModule] + Redis 引导（后者在 v2 无 Redis 配置下发时本就 inert）。
 *
 * @param settings 消息运行参数（含 enabled 开关与 RPC 超时）
 * @param holder   对外门面持有者（启动成功后置活跃 [MessagingView]）
 * @param bus      消息总线（HTTP transport 注入）
 * @param poll     下行长轮询协调器
 * @param info     INFO 日志
 * @param error    ERROR 日志
 */
class MessagingRuntime(
    private val settings: MessagingSettings,
    private val holder: MessagingHolder,
    private val bus: MessageBus,
    private val poll: MessagePollCoordinator,
    private val info: (String) -> Unit = {},
    private val error: (String, Throwable?) -> Unit = { _, _ -> },
) {
    private val view = MessagingView(bus)

    init {
        poll.connectionStateListener = ::onConnectionStateChanged
    }

    /**
     * 启动下行轮询；首轮零等待探测成功后才启动总线并置活跃门面。
     * 幂等（poll.start / bus.start 各自可重复调用），未启用则保持降级；失败仅降级不抛。
     */
    fun start() {
        if (!settings.enabled) {
            info("跨服消息模块未启用（messaging.enabled=false），保持降级")
            return
        }
        try {
            holder.set(view)
            poll.start()
            info("跨服消息模块轮询已启动（HTTP 中转，首轮探测通过后开放门面）")
        } catch (t: Throwable) {
            error("跨服消息模块启动失败，降级（不影响配置同步与玩家游玩）", t)
            rollbackStart()
        }
    }

    /** 停止：停长轮询、复位门面、关总线（挂起 RPC 异常完成）。幂等。 */
    fun stop() {
        poll.stop()
        deactivate("停止跨服消息模块异常")
    }

    private fun onConnectionStateChanged(connected: Boolean) {
        if (!connected) {
            deactivate("跨服消息通道失联清理异常")
            return
        }
        try {
            if (!bus.isAvailable()) bus.start()
            holder.set(view)
        } catch (t: Throwable) {
            deactivate("跨服消息通道恢复回滚异常")
            throw t
        }
    }

    private fun rollbackStart() {
        poll.stop()
        deactivate("跨服消息模块启动回滚异常")
    }

    private fun deactivate(errorMessage: String) {
        holder.reset()
        try {
            bus.close()
        } catch (t: Throwable) {
            error(errorMessage, t)
        }
    }
}

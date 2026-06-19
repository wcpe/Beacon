package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.transport.JsonCodec

/**
 * 跨服消息模块编排（core）：装配 [MessageBus] + 启停 + 把活跃门面注入 [MessagingHolder]。
 *
 * 与配置同步/心跳故障域隔离（ADR-0016 决策 6）：本模块独立持有 transport 与 bus，
 * 启停不影响配置命脉。启动失败仅降级（holder 保持 Disabled），不抛到壳层中断接入。
 *
 * 生命周期：
 * - [start]：建 bus、连 transport、订阅收件流/回信通道，成功则 holder.set 活跃门面。
 * - [stop]：关 bus（失败挂起 RPC、关连接），holder 复位 Disabled。
 *
 * Redis 连接配置由壳层从 Beacon 下发的有效配置解析后，构造 transport 注入本模块（决策 15）。
 *
 * @param transport     已按下发配置构造的传输（Redis 适配器实例）
 * @param codec         信封编解码
 * @param selfServerId  本服 serverId
 * @param settings      消息运行参数
 * @param holder        对外门面持有者
 * @param playerLocator 玩家位置解析（可空 = 不支持按玩家寻址）
 * @param scheduleTimeout RPC 超时调度（壳层注入平台 runAsyncDelayed）
 * @param info          INFO 日志
 * @param warn          WARN 日志
 * @param error         ERROR 日志
 */
class MessagingModule(
    private val transport: MessageTransport,
    private val codec: JsonCodec,
    private val selfServerId: String,
    private val settings: MessagingSettings,
    private val holder: MessagingHolder,
    private val playerLocator: PlayerLocator? = null,
    private val scheduleTimeout: ((Long, () -> Unit) -> Unit)? = null,
    private val info: (String) -> Unit = {},
    private val warn: (String) -> Unit = {},
    private val error: (String, Throwable?) -> Unit = { _, _ -> },
) {

    @Volatile
    private var bus: MessageBus? = null

    /**
     * 启动消息模块。失败仅降级（holder 保持 Disabled），不抛出（不连累 agent 接入）。
     *
     * @return true=启动成功并已就绪；false=启动失败已降级
     */
    fun start(): Boolean {
        if (!settings.enabled) {
            info("跨服消息模块未启用（messaging.enabled=false），保持降级")
            return false
        }
        val newBus = MessageBus(
            transport = transport,
            codec = codec,
            selfServerId = selfServerId,
            settings = settings,
            playerLocator = playerLocator,
            scheduleTimeout = scheduleTimeout ?: MessageBus.DEFAULT_SCHEDULER,
            warn = warn,
        )
        return try {
            newBus.start()
            bus = newBus
            holder.set(MessagingView(newBus))
            info("跨服消息模块已启动：serverId=$selfServerId")
            true
        } catch (t: Throwable) {
            error("跨服消息模块启动失败，降级（不影响配置同步与玩家游玩）", t)
            try {
                newBus.close()
            } catch (ignored: Throwable) {
                // 启动失败后的清理异常忽略。
            }
            holder.reset()
            false
        }
    }

    /** 停止消息模块并复位为降级门面。幂等。 */
    fun stop() {
        holder.reset()
        val current = bus ?: return
        bus = null
        try {
            current.close()
        } catch (t: Throwable) {
            warn("停止跨服消息模块异常：${t.message}")
        }
    }
}

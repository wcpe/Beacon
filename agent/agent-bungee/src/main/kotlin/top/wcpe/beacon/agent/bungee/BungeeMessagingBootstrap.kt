package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.adapters.messaging.RedisConnection
import top.wcpe.beacon.agent.adapters.messaging.RedisMessageTransport
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.messaging.MessagingHolder
import top.wcpe.beacon.agent.core.messaging.MessagingModule
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.transport.JsonCodec

/**
 * Bungee（proxy）侧跨服消息模块引导（FR-26 / ADR-0016）：从 Beacon 下发的有效配置读取 Redis 连接，
 * 在代理上启动**完整消息模块**（消费本服收件流 + 按 type 分发 `on` 处理器 + RPC 回信 + 主题 publish/subscribe），
 * 使代理成为消息收发的对等参与方——与子服一致经 [MessagingHolder] 对外暴露 `BeaconAgent.messaging()`。
 *
 * 与 [BungeePlayerRosterBootstrap] 的分工：后者只维护「玩家→所在子服」名册（写 `beacon:player-loc`，供子服按玩家
 * 寻址）；本引导负责代理自身的消息收发能力（代理作为跨服编排控制层需接收业务消息并发布广播等）。二者各持独立
 * Redis 连接、互不影响，与配置同步 / 心跳故障域隔离——启动失败仅降级（holder=Disabled），不连累代理与配置命脉。
 *
 * Redis 连接配置以约定 dataId（[REDIS_CONFIG_DATA_ID]）随有效配置下发（决策 15）；冷启动未取得时保持降级，
 * 待配置变更后再 [sync]。本引导在 ENABLE 后调用一次，并在每次配置变更后再调，据下发配置启停 / 重连。
 *
 * @param identity 本代理身份（serverId 决定收件流 / 消费组 / 回信通道）
 * @param settings 本地运行参数（含消息开关与超时、请求超时供连接超时复用）
 * @param store    有效配置存储（读 Redis 下发配置）
 * @param codec    信封编解码
 * @param holder   对外消息门面持有者（模块启动成功后置为活跃门面）
 * @param adapter  平台适配（日志、延迟调度供 RPC 超时）
 */
class BungeeMessagingBootstrap(
    private val identity: AgentIdentity,
    private val settings: AgentSettings,
    private val store: EffectiveConfigStore,
    private val codec: JsonCodec,
    private val holder: MessagingHolder,
    private val adapter: PlatformAdapter,
) {

    /** 当前消息模块；null 表示未启动（未启用 / 配置缺失 / 已停止）。 */
    @Volatile
    private var module: MessagingModule? = null

    /** 上次用于启动的连接快照，避免配置无关变更时无谓重连。 */
    @Volatile
    private var lastConnection: RedisConnection? = null

    /**
     * 据当前下发配置同步消息模块状态（ENABLE 后与每次配置变更后调用）。
     *
     * - 未启用：保持降级。
     * - 已启用 + 拿到 Redis 配置 + 与上次不同：重建模块并启动。
     * - 已启用 + 配置缺失：保持降级（冷启动未取得配置先关，决策 15）。
     */
    fun sync() {
        if (!settings.messaging.enabled) {
            return
        }
        val connection = readRedisConnection() ?: return
        if (connection == lastConnection && module != null) {
            return
        }
        // 连接变化（或首次）：停旧起新。
        stop()
        val transport = RedisMessageTransport(
            connection = connection,
            serverId = identity.serverId,
            settings = settings.messaging,
            info = adapter::info,
            warn = adapter::warn,
            error = adapter::error,
        )
        val newModule = MessagingModule(
            transport = transport,
            codec = codec,
            selfServerId = identity.serverId,
            settings = settings.messaging,
            holder = holder,
            // 代理侧亦可按玩家寻址（读自身维护的名册）。
            playerLocator = transport.playerLocator(),
            scheduleTimeout = adapter::runAsyncDelayed,
            info = adapter::info,
            warn = adapter::warn,
            error = adapter::error,
        )
        // 连接与重订阅是阻塞操作，放异步线程，绝不阻塞主线程。
        adapter.runAsync {
            if (newModule.start()) {
                module = newModule
                lastConnection = connection
            }
        }
    }

    /** 停止消息模块（DISABLE 调用）。 */
    fun stop() {
        module?.stop()
        module = null
        lastConnection = null
    }

    /** 读取下发的 Redis 连接配置；缺失返回 null。 */
    private fun readRedisConnection(): RedisConnection? {
        val item = store.item(REDIS_CONFIG_DATA_ID) ?: return null
        val tree = try {
            codec.decode(item.content)
        } catch (t: Throwable) {
            adapter.warn("解析 Redis 下发配置失败：${t.message}")
            return null
        }
        return RedisConnection.fromTree(tree, connectTimeoutMs = settings.requestTimeoutMs.toInt())
    }

    private companion object {
        /** Redis 连接配置的约定 dataId（随有效配置下发，与子服侧一致，决策 15）。 */
        const val REDIS_CONFIG_DATA_ID: String = "beacon-messaging-redis"
    }
}

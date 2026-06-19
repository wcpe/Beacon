package top.wcpe.beacon.agent.bukkit

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
 * Bukkit 侧跨服消息模块引导（FR-26 / ADR-0016）：从 Beacon 下发的有效配置读取 Redis 连接，
 * 启动消息模块；配置变更时重连。与配置同步/心跳故障域隔离——本引导独立，启动失败仅降级。
 *
 * Redis 连接配置以约定 dataId（[REDIS_CONFIG_DATA_ID]）随有效配置下发（决策 15）；冷启动未取得时
 * 消息模块保持降级（holder=Disabled）。本引导在 ENABLE 后调用一次，并在每次配置变更后再调，
 * 据下发配置启停 / 重连。
 *
 * 注意：真正的 Redis 连接 / Streams / pub/sub 行为需联网真机验证（本地无 Redis）。
 *
 * @param identity 本服身份（serverId 决定收件流 / 消费组 / 回信通道）
 * @param settings 本地运行参数（含消息开关与超时、请求超时供连接超时复用）
 * @param store    有效配置存储（读 Redis 下发配置）
 * @param codec    信封编解码
 * @param holder   对外门面持有者
 * @param adapter  平台适配（日志、延迟调度供 RPC 超时）
 */
class BukkitMessagingBootstrap(
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
        val connection = readRedisConnection()
        if (connection == null) {
            // 配置未下发：保持降级，等待后续配置变更再尝试。
            return
        }
        if (connection == lastConnection && module != null) {
            // 连接未变且已在运行：无需重连。
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
            // 子服侧按玩家寻址读名册（名册由 proxy 维护）。
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
        /** Redis 连接配置的约定 dataId（随有效配置下发，决策 15）。 */
        const val REDIS_CONFIG_DATA_ID: String = "beacon-messaging-redis"
    }
}

package top.wcpe.beacon.agent.bungee

import redis.clients.jedis.JedisPool
import top.wcpe.beacon.agent.adapters.messaging.JedisPoolFactory
import top.wcpe.beacon.agent.adapters.messaging.RedisConnection
import top.wcpe.beacon.agent.adapters.messaging.RedisPlayerRoster
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.transport.JsonCodec
import net.md_5.bungee.api.ProxyServer

/**
 * Bungee（proxy）侧玩家位置名册引导（FR-26 / ADR-0016 决策 5）：
 * 据下发的 Redis 配置建连接池与 [RedisPlayerRoster]，随玩家进服/换服/退出更新名册，重启时全量重建。
 *
 * 名册供子服 agent 的「按玩家寻址」解析（[top.wcpe.beacon.agent.adapters.messaging.RedisMessageTransport.playerLocator]）。
 * Redis 配置缺失（冷启动未下发）时保持空闲，待配置变更后再 [sync]。
 *
 * 注意：真正的 Redis 读写与 Bungee 事件联动需真机验证（本地无 Redis / 无代理）。
 *
 * @param settings 本地运行参数（messaging.enabled 与请求超时）
 * @param store    有效配置存储（读 Redis 下发配置）
 * @param codec    json 解码（解析下发配置树）
 * @param adapter  平台适配（日志、异步调度）
 */
class BungeePlayerRosterBootstrap(
    private val settings: AgentSettings,
    private val store: EffectiveConfigStore,
    private val codec: JsonCodec,
    private val adapter: PlatformAdapter,
) {

    @Volatile
    private var pool: JedisPool? = null

    @Volatile
    private var roster: RedisPlayerRoster? = null

    @Volatile
    private var lastConnection: RedisConnection? = null

    /** 据当前下发配置同步名册引导（ENABLE 后与每次配置变更后调用）。连接变化则重建并全量重建名册。 */
    fun sync() {
        if (!settings.messaging.enabled) return
        val connection = readRedisConnection() ?: return
        if (connection == lastConnection && roster != null) return
        // 连接变化（或首次）：重建池与名册，并全量重建（重启不信旧名册）。
        adapter.runAsync {
            closePool()
            try {
                val newPool = JedisPoolFactory.create(connection)
                val newRoster = RedisPlayerRoster(newPool, warn = adapter::warn)
                newRoster.rebuild(currentOnlinePlayers())
                pool = newPool
                roster = newRoster
                lastConnection = connection
                adapter.info("玩家位置名册已就绪并按当前在线玩家重建")
            } catch (t: Throwable) {
                adapter.error("玩家位置名册初始化失败，proxy 仍正常运行（按玩家寻址不可用）", t)
            }
        }
    }

    /** 玩家进服/换服：登记当前所在服。 */
    fun onPlayerLocated(playerName: String, serverId: String) {
        roster?.onPlayerLocated(playerName, serverId)
    }

    /** 玩家退出：仅当名册当前所在服与退出服一致才删（换服误删保护）。 */
    fun onPlayerQuit(playerName: String, fromServerId: String) {
        roster?.onPlayerQuit(playerName, fromServerId)
    }

    /** 停止：关连接池。 */
    fun stop() {
        closePool()
        roster = null
        lastConnection = null
    }

    private fun closePool() {
        try {
            pool?.close()
        } catch (t: Throwable) {
            adapter.warn("关闭名册连接池异常：${t.message}")
        }
        pool = null
    }

    /** 当前在线玩家 → 所在子服（重启重建用）。 */
    private fun currentOnlinePlayers(): Map<String, String> {
        val result = LinkedHashMap<String, String>()
        for (player in ProxyServer.getInstance().players) {
            val serverName = player.server?.info?.name ?: continue
            result[player.name] = serverName
        }
        return result
    }

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
        /** Redis 连接配置的约定 dataId（与子服侧一致）。 */
        const val REDIS_CONFIG_DATA_ID: String = "beacon-messaging-redis"
    }
}

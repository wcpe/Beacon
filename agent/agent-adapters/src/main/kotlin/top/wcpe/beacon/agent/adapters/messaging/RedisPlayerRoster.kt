package top.wcpe.beacon.agent.adapters.messaging

import redis.clients.jedis.JedisPool

/**
 * 玩家位置名册维护（ADR-0016 决策 5：由 BC 上的 beacon-proxy 维护「玩家→所在子服」）。
 *
 * 写入 Redis hash `beacon:player-loc`（field=玩家名，value=所在 serverId），随进服/换服/退出更新。
 * **proxy 重启重建**（决策 5）：不信旧名册，重启后用当前在线玩家全量重建（[rebuild]）。
 *
 * 一致性取舍（简单优先）：接受换服瞬间短暂错位；退出删除按「仅当当前所在服与退出服一致才删」，
 * 避免「换服后旧服的退出事件误删新位置」（换服时序常为：新服 join 先到、旧服 quit 后到）。
 *
 * @param pool Redis 连接池
 * @param warn 告警日志
 */
class RedisPlayerRoster(
    private val pool: JedisPool,
    private val warn: (String) -> Unit = {},
) {

    /** 玩家进服/换服：登记其当前所在服。 */
    fun onPlayerLocated(playerName: String, serverId: String) {
        try {
            pool.resource.use { jedis ->
                jedis.hset(RedisChannels.PLAYER_LOCATION_HASH, playerName, serverId)
            }
        } catch (t: Throwable) {
            warn("更新玩家名册异常：player=$playerName server=$serverId ${t.message}")
        }
    }

    /**
     * 玩家退出：仅当名册中当前所在服与本次退出的服一致才删除。
     *
     * 防换服误删：换服时旧服的退出事件可能晚于新服的进服事件到达，此时名册已是新服，
     * 旧服退出不应删除新位置。
     */
    fun onPlayerQuit(playerName: String, fromServerId: String) {
        try {
            pool.resource.use { jedis ->
                val current = jedis.hget(RedisChannels.PLAYER_LOCATION_HASH, playerName)
                if (shouldDeleteOnQuit(current, fromServerId)) {
                    jedis.hdel(RedisChannels.PLAYER_LOCATION_HASH, playerName)
                }
            }
        } catch (t: Throwable) {
            warn("删除玩家名册异常：player=$playerName from=$fromServerId ${t.message}")
        }
    }

    /**
     * 全量重建名册（proxy 重启后调用）：清空旧名册，按当前在线玩家整表写入。
     *
     * @param onlinePlayers 玩家名 → 所在子服 serverId
     */
    fun rebuild(onlinePlayers: Map<String, String>) {
        try {
            pool.resource.use { jedis ->
                // 先删旧名册（不信任重启前残留），再整表写入当前在线玩家。
                jedis.del(RedisChannels.PLAYER_LOCATION_HASH)
                if (onlinePlayers.isNotEmpty()) {
                    jedis.hset(RedisChannels.PLAYER_LOCATION_HASH, onlinePlayers)
                }
            }
        } catch (t: Throwable) {
            warn("重建玩家名册异常：count=${onlinePlayers.size} ${t.message}")
        }
    }

    companion object {

        /**
         * 退出是否应删除名册项（纯逻辑，便于单测）：仅当名册当前所在服与退出服一致才删。
         *
         * @param currentServerId 名册中该玩家当前所在服（可空 = 名册无此玩家）
         * @param fromServerId    本次退出事件来源服
         * @return true=应删除；false=换服误删保护 / 名册无项，跳过
         */
        fun shouldDeleteOnQuit(currentServerId: String?, fromServerId: String): Boolean {
            return currentServerId != null && currentServerId == fromServerId
        }
    }
}

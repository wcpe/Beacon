package top.wcpe.beacon.agent.adapters.messaging

/**
 * Redis 信道命名约定（纯逻辑，无 Jedis 依赖，便于单测）。
 *
 * 命名按单 BC 入口设计（ADR-0016 决策 11）：收件流 / 主题 / 回信通道 / 玩家名册各有固定前缀，
 * 全集群一致，便于运维排查与避免键冲突。
 */
object RedisChannels {

    /** 每服收件流键：beacon:msg:{serverId}。消费组名 = serverId。 */
    fun serverInbox(serverId: String): String = "beacon:msg:$serverId"

    /** 主题信道键（pub/sub）：beacon:topic:{topic}。 */
    fun topic(topic: String): String = "beacon:topic:$topic"

    /** 回信信道键（pub/sub）：beacon:reply:{serverId}。与 MessageBus 的 replyTo 值一一对应。 */
    fun replyChannel(serverId: String): String = "beacon:reply:$serverId"

    /** 玩家位置名册键（hash）：beacon:player-loc，field=玩家名，value=所在 serverId。 */
    const val PLAYER_LOCATION_HASH: String = "beacon:player-loc"

    /** 收件流消费组名（按单 proxy 设计：组 = serverId）。 */
    fun consumerGroup(serverId: String): String = serverId

    /** 信封 json 在 Stream entry 里的字段名（Streams 是 field-value，单字段承载整条 json）。 */
    const val ENVELOPE_FIELD: String = "envelope"
}

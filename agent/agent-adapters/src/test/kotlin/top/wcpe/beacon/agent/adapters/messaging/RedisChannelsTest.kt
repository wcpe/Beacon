package top.wcpe.beacon.agent.adapters.messaging

import kotlin.test.Test
import kotlin.test.assertEquals

/** Redis 信道命名约定的纯逻辑单测（无需 Redis 连接）。 */
class RedisChannelsTest {

    @Test
    fun `收件流键含 serverId`() {
        assertEquals("beacon:msg:lobby-1", RedisChannels.serverInbox("lobby-1"))
    }

    @Test
    fun `主题键含 topic`() {
        assertEquals("beacon:topic:news", RedisChannels.topic("news"))
    }

    @Test
    fun `回信信道键含 serverId`() {
        assertEquals("beacon:reply:lobby-1", RedisChannels.replyChannel("lobby-1"))
    }

    @Test
    fun `消费组名等于 serverId`() {
        assertEquals("lobby-1", RedisChannels.consumerGroup("lobby-1"))
    }

    @Test
    fun `玩家名册键与信封字段名固定`() {
        assertEquals("beacon:player-loc", RedisChannels.PLAYER_LOCATION_HASH)
        assertEquals("envelope", RedisChannels.ENVELOPE_FIELD)
    }
}

package top.wcpe.beacon.agent.adapters.messaging

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 玩家名册退出删除决策的纯逻辑单测（换服误删保护）。 */
class RedisPlayerRosterTest {

    @Test
    fun `当前所在服与退出服一致时删除`() {
        assertTrue(RedisPlayerRoster.shouldDeleteOnQuit(currentServerId = "lobby-1", fromServerId = "lobby-1"))
    }

    @Test
    fun `换服后旧服退出不删新位置`() {
        // 名册已是新服 game-1，旧服 lobby-1 的退出事件晚到，不应误删。
        assertFalse(RedisPlayerRoster.shouldDeleteOnQuit(currentServerId = "game-1", fromServerId = "lobby-1"))
    }

    @Test
    fun `名册无此玩家时不删`() {
        assertFalse(RedisPlayerRoster.shouldDeleteOnQuit(currentServerId = null, fromServerId = "lobby-1"))
    }

    @Test
    fun `空来源服退出应删名册项`() {
        // 玩家整体断开时 player.server 为空 → fromServerId=""，应无条件删，否则名册项永不删、泄漏。
        assertTrue(RedisPlayerRoster.shouldDeleteOnQuit(currentServerId = "lobby-1", fromServerId = ""))
    }

    @Test
    fun `空来源服且名册无项时删（无害空删）`() {
        // 无对应名册项时返回 true 仅触发对缺失 field 的 hdel（Redis 无操作），语义不冲突。
        assertTrue(RedisPlayerRoster.shouldDeleteOnQuit(currentServerId = null, fromServerId = ""))
    }
}

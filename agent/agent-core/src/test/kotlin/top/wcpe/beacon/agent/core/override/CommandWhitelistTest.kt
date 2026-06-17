package top.wcpe.beacon.agent.core.override

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * agent 本地命令白名单单测（ADR-0011 决策 3）：
 * 默认空白名单全拒；首 token 命中才放行；元字符 / 多条 / 控制字符一律拒绝（防注入）。
 */
class CommandWhitelistTest {

    @Test
    fun `默认空白名单 任何命令都拒绝`() {
        val wl = CommandWhitelist(emptySet())
        assertFalse(wl.isAllowed("allin reload"))
        assertFalse(wl.isAllowed("reload"))
    }

    @Test
    fun `首 token 命中白名单才放行`() {
        val wl = CommandWhitelist(setOf("allin", "papi"))
        assertTrue(wl.isAllowed("allin reload"))
        assertTrue(wl.isAllowed("papi reload"))
        assertTrue(wl.isAllowed("allin"))
        // 首 token 不在白名单：拒。
        assertFalse(wl.isAllowed("lp sync"))
        // 整条命令在白名单文本里出现但首 token 不符：拒（按首 token 比对，不做子串匹配）。
        assertFalse(wl.isAllowed("evil allin"))
    }

    @Test
    fun `白名单首 token 不区分大小写`() {
        val wl = CommandWhitelist(setOf("Allin"))
        assertTrue(wl.isAllowed("allin reload"))
        assertTrue(wl.isAllowed("ALLIN reload"))
    }

    @Test
    fun `注入元字符一律拒绝`() {
        val wl = CommandWhitelist(setOf("allin"))
        assertFalse(wl.isAllowed("allin reload; rm -rf /"))
        assertFalse(wl.isAllowed("allin && evil"))
        assertFalse(wl.isAllowed("allin | cat"))
        assertFalse(wl.isAllowed("allin > out"))
        assertFalse(wl.isAllowed("allin < in"))
        assertFalse(wl.isAllowed("allin \$HOME"))
        assertFalse(wl.isAllowed("allin `whoami`"))
        assertFalse(wl.isAllowed("allin &"))
    }

    @Test
    fun `换行回车控制字符一律拒绝`() {
        val wl = CommandWhitelist(setOf("allin"))
        assertFalse(wl.isAllowed("allin reload\nplay evil"))
        assertFalse(wl.isAllowed("allin reload\rplay evil"))
        assertFalse(wl.isAllowed("allin\treload"))
        assertFalse(wl.isAllowed("allin\u0007reload"))
    }

    @Test
    fun `空命令拒绝`() {
        val wl = CommandWhitelist(setOf("allin"))
        assertFalse(wl.isAllowed(""))
        assertFalse(wl.isAllowed("   "))
    }
}

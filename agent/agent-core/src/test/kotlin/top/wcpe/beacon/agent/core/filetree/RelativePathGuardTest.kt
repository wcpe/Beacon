package top.wcpe.beacon.agent.core.filetree

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 落盘相对路径安全校验单测：拒绝绝对 / 穿越 / 反斜杠 / 盘符。 */
class RelativePathGuardTest {

    @Test
    fun `合法相对路径放行`() {
        assertTrue(RelativePathGuard.isSafe("a.yml"))
        assertTrue(RelativePathGuard.isSafe("ui-components/main.allin"))
        assertTrue(RelativePathGuard.isSafe("scripts/sub/hello.js"))
    }

    @Test
    fun `空路径拒绝`() {
        assertFalse(RelativePathGuard.isSafe(""))
    }

    @Test
    fun `绝对路径拒绝`() {
        assertFalse(RelativePathGuard.isSafe("/etc/passwd"))
        assertFalse(RelativePathGuard.isSafe("C:/Windows/system32"))
        assertFalse(RelativePathGuard.isSafe("C:foo"))
    }

    @Test
    fun `点点穿越拒绝`() {
        assertFalse(RelativePathGuard.isSafe(".."))
        assertFalse(RelativePathGuard.isSafe("../escape"))
        assertFalse(RelativePathGuard.isSafe("a/../../b"))
        assertFalse(RelativePathGuard.isSafe("dir/../../../etc"))
    }

    @Test
    fun `反斜杠拒绝`() {
        assertFalse(RelativePathGuard.isSafe("a\\b"))
        assertFalse(RelativePathGuard.isSafe("dir\\..\\escape"))
    }

    @Test
    fun `受保护的自身 dataFolder 命中返回 true`() {
        // 顶段命中受保护集合（agent 自身插件名）即视为受保护，applier 据此跳过落盘。
        val reserved = setOf("BeaconAgent", "BeaconAgentProxy")
        assertTrue(RelativePathGuard.isReservedSelfPath("BeaconAgent/config.yml", reserved))
        assertTrue(RelativePathGuard.isReservedSelfPath("BeaconAgentProxy/effective-config.snapshot.json", reserved))
        // 多层深路径同样命中（顶段相同即可）
        assertTrue(RelativePathGuard.isReservedSelfPath("BeaconAgent/sub/dir/file.json", reserved))
    }

    @Test
    fun `未命中保护集合不视为受保护`() {
        val reserved = setOf("BeaconAgent")
        assertFalse(RelativePathGuard.isReservedSelfPath("LuckPerms/config.yml", reserved))
        assertFalse(RelativePathGuard.isReservedSelfPath("config.yml", reserved))
        // 顶段不严格相等不命中：BeaconAgentX 不属于 BeaconAgent 子树
        assertFalse(RelativePathGuard.isReservedSelfPath("BeaconAgentX/config.yml", reserved))
    }

    @Test
    fun `空保护集合永远返回 false`() {
        // 壳层未传入自身名（如纯 core 单元测试）时不应拦截任何路径。
        assertFalse(RelativePathGuard.isReservedSelfPath("BeaconAgent/config.yml", emptySet()))
        assertFalse(RelativePathGuard.isReservedSelfPath("anything", emptySet()))
    }
}

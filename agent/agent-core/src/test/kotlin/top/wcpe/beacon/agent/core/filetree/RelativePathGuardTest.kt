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
}

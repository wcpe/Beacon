package top.wcpe.beacon.agent.core.command

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 反向抓取相对路径字符串级安全校验 [PluginsPathGuard] 单测（FR-39，见 ADR-0027）：
 * 拒 `..` / 绝对 / 反斜杠 / 冒号 / UNC / Windows 保留设备名 / 段尾点空格 / 空段；合法相对路径放行。
 */
class PluginsPathGuardTest {

    @Test
    fun `合法相对路径放行`() {
        assertTrue(PluginsPathGuard.isSafe("config.yml"))
        assertTrue(PluginsPathGuard.isSafe("AllinCore/config.yml"))
        assertTrue(PluginsPathGuard.isSafe("lang/zh_CN.yml"))
        assertTrue(PluginsPathGuard.isSafe("a/b/c/deep.json"))
        // 段内含点（非段尾）合法。
        assertTrue(PluginsPathGuard.isSafe("file.name.with.dots.yml"))
    }

    @Test
    fun `穿越一律拒绝`() {
        assertFalse(PluginsPathGuard.isSafe(".."))
        assertFalse(PluginsPathGuard.isSafe("../escape.yml"))
        assertFalse(PluginsPathGuard.isSafe("a/../../escape.yml"))
        assertFalse(PluginsPathGuard.isSafe("../../etc/passwd"))
    }

    @Test
    fun `绝对路径与盘符与UNC拒绝`() {
        assertFalse(PluginsPathGuard.isSafe("/etc/passwd"))
        assertFalse(PluginsPathGuard.isSafe("C:/Windows/system32"))
        assertFalse(PluginsPathGuard.isSafe("C:foo"))
        assertFalse(PluginsPathGuard.isSafe("\\\\host\\share\\x"))
    }

    @Test
    fun `反斜杠与冒号拒绝`() {
        assertFalse(PluginsPathGuard.isSafe("a\\b.yml"))
        assertFalse(PluginsPathGuard.isSafe("a:b.yml"))
        assertFalse(PluginsPathGuard.isSafe("config.yml:hidden")) // Windows ADS
    }

    @Test
    fun `Windows保留设备名不区分大小写拒绝`() {
        assertFalse(PluginsPathGuard.isSafe("CON"))
        assertFalse(PluginsPathGuard.isSafe("con.yml"))
        assertFalse(PluginsPathGuard.isSafe("sub/AUX"))
        assertFalse(PluginsPathGuard.isSafe("LPT1.txt"))
        assertFalse(PluginsPathGuard.isSafe("nul"))
        assertFalse(PluginsPathGuard.isSafe("COM9.dat"))
    }

    @Test
    fun `段尾点或空格拒绝`() {
        // Windows 落盘剥离「段尾」的点 / 空格，借此绕过判定——故只拒段尾、不拒段内
        //（"my config.yml" 等含内部空格属合法文件名，不应拒）。下列四例覆盖 末段/非末段 × 尾点/尾空格。
        assertFalse(PluginsPathGuard.isSafe("config.yml.")) // 末段尾点
        assertFalse(PluginsPathGuard.isSafe("sub /x.yml")) // 非末段尾空格
        assertFalse(PluginsPathGuard.isSafe("name.yml ")) // 末段尾空格
        assertFalse(PluginsPathGuard.isSafe("nested/x.jar.")) // 非末段尾点
    }

    @Test
    fun `段内空格属合法文件名放行`() {
        // 段「内部」空格（非段尾）不被任何平台剥离，属合法文件名，不应误拒。
        assertTrue(PluginsPathGuard.isSafe("my config.yml"))
        assertTrue(PluginsPathGuard.isSafe("name .yml"))
        assertTrue(PluginsPathGuard.isSafe("plugin/data file.json"))
    }

    @Test
    fun `空路径与空段拒绝`() {
        assertFalse(PluginsPathGuard.isSafe(""))
        assertFalse(PluginsPathGuard.isSafe("a//b.yml")) // 连续斜杠产生空段
        assertFalse(PluginsPathGuard.isSafe("a/b/")) // 末尾斜杠产生空段
    }
}

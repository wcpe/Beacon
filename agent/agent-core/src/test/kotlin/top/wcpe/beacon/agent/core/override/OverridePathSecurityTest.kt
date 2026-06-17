package top.wcpe.beacon.agent.core.override

import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 覆盖落盘路径 Path 级安全校验单测（ADR-0011 决策 4）：
 * 用 normalize().startsWith(Path)（非字符串前缀）限定在目标根内；
 * 拒 `..` / 绝对 / 盘符 / UNC / 反斜杠 / 冒号 / Windows 保留设备名 / .jar / server 关键文件；
 * 规范化后不区分大小写比较。agent 为最终权威。
 */
class OverridePathSecurityTest {

    private val root: File = Files.createTempDirectory("beacon-override").toFile()
    private val guard = OverridePathSecurity(root)

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
    }

    @Test
    fun `目标根内的相对路径放行`() {
        assertTrue(guard.isSafe("config.yml"))
        assertTrue(guard.isSafe("scripts/hello.js"))
        assertTrue(guard.isSafe("ui-components/main.allin"))
        assertTrue(guard.isSafe("lang/zh_CN.yml"))
    }

    @Test
    fun `穿越逃出目标根一律拒绝`() {
        assertFalse(guard.isSafe("../escape.yml"))
        assertFalse(guard.isSafe("a/../../escape.yml"))
        assertFalse(guard.isSafe("../../etc/passwd"))
        assertFalse(guard.isSafe(".."))
    }

    @Test
    fun `绝对路径与盘符与UNC拒绝`() {
        assertFalse(guard.isSafe("/etc/passwd"))
        assertFalse(guard.isSafe("C:/Windows/system32"))
        assertFalse(guard.isSafe("C:foo"))
        assertFalse(guard.isSafe("\\\\host\\share\\x"))
    }

    @Test
    fun `反斜杠与冒号拒绝`() {
        assertFalse(guard.isSafe("a\\b.yml"))
        assertFalse(guard.isSafe("a:b.yml"))
        // 冒号常见于 Windows ADS（备用数据流）与盘符，规范化前即拒。
        assertFalse(guard.isSafe("config.yml:hidden"))
    }

    @Test
    fun `Windows保留设备名不区分大小写拒绝`() {
        assertFalse(guard.isSafe("CON"))
        assertFalse(guard.isSafe("con.yml"))
        assertFalse(guard.isSafe("sub/AUX"))
        assertFalse(guard.isSafe("LPT1.txt"))
        assertFalse(guard.isSafe("nul"))
    }

    @Test
    fun `禁覆盖 jar`() {
        assertFalse(guard.isSafe("AllinCore.jar"))
        assertFalse(guard.isSafe("libs/dep.JAR"))
        assertFalse(guard.isSafe("nested/x.Jar"))
    }

    @Test
    fun `禁覆盖 server 关键文件`() {
        assertFalse(guard.isSafe("server.properties"))
        assertFalse(guard.isSafe("bukkit.yml"))
        assertFalse(guard.isSafe("spigot.yml"))
        assertFalse(guard.isSafe("paper.yml"))
        assertFalse(guard.isSafe("eula.txt"))
    }

    @Test
    fun `段尾点或空格不可绕过禁覆盖`() {
        // Windows 落盘剥离段尾的点 / 空格，"x.jar."→"x.jar"、"bukkit.yml."→"bukkit.yml"，必须拒。
        assertFalse(guard.isSafe("AllinCore.jar."))
        assertFalse(guard.isSafe("bukkit.yml."))
        assertFalse(guard.isSafe("server.properties."))
        // 段尾空格（整串 trim 去不掉非末段、点也去不掉）。
        assertFalse(guard.isSafe("sub /x.yml"))
        assertFalse(guard.isSafe("con /x.yml"))
        assertFalse(guard.isSafe("nested/x.jar."))
    }

    @Test
    fun `空路径拒绝`() {
        assertFalse(guard.isSafe(""))
        assertFalse(guard.isSafe("   "))
    }
}

package top.wcpe.beacon.agent.core.override

import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 覆盖集目标根 agent 侧最终校验单测（ADR-0011 决策 4，控制面被攻破兜底）。
 * 限定 plugins/<plugin>/ 内；拒穿越 / 绝对 / 盘符 / 反斜杠 / 保留名 / 段尾点空格 / 仅 plugins 根。
 */
class TargetRootSecurityTest {

    private val serverRoot: File = Files.createTempDirectory("beacon-srv-tr").toFile()
    private val pluginsBase: File = File(serverRoot, "plugins").apply { mkdirs() }
    private val sec = TargetRootSecurity(pluginsBase)

    @AfterTest
    fun cleanup() {
        serverRoot.deleteRecursively()
    }

    @Test
    fun `合法插件目录放行`() {
        assertTrue(sec.isSafe("plugins/AllinCore"))
        assertTrue(sec.isSafe("plugins/AllinCore/"))
        assertTrue(sec.isSafe("plugins/Some-Plugin_1"))
    }

    @Test
    fun `穿越逃逸 plugins 拒绝`() {
        assertFalse(sec.isSafe("plugins/../../etc"))
        assertFalse(sec.isSafe("plugins/../secret"))
        assertFalse(sec.isSafe("../plugins/AllinCore"))
    }

    @Test
    fun `仅 plugins 根或非 plugins 前缀 拒绝`() {
        assertFalse(sec.isSafe("plugins"))
        assertFalse(sec.isSafe("plugins/"))
        assertFalse(sec.isSafe("config/AllinCore"))
        assertFalse(sec.isSafe("world/AllinCore"))
    }

    @Test
    fun `绝对路径 盘符 反斜杠 拒绝`() {
        assertFalse(sec.isSafe("/plugins/AllinCore"))
        assertFalse(sec.isSafe("C:/plugins/AllinCore"))
        assertFalse(sec.isSafe("plugins\\AllinCore"))
        assertFalse(sec.isSafe("plugins/Allin:Core"))
    }

    @Test
    fun `Windows 保留名与段尾点空格 拒绝`() {
        assertFalse(sec.isSafe("plugins/con"))
        assertFalse(sec.isSafe("plugins/AllinCore."))
        assertFalse(sec.isSafe("plugins/AllinCore "))
    }

    @Test
    fun `空串拒绝`() {
        assertFalse(sec.isSafe(""))
        assertFalse(sec.isSafe("   "))
    }
}

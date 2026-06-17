package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.security.MessageDigest
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 覆盖应用编排单测（ADR-0011 决策 2/4/5/7）：备份→Path 级安全覆盖→受管标记→（命中白名单才）派发命令；
 * 检测外部改动告警而非盲盖；非法路径跳过不逃逸；回滚只还原文件不重放命令。
 */
class OverrideApplierTest {

    private val targetRoot: File = Files.createTempDirectory("beacon-ov-target").toFile()
    private val backupDir: File = Files.createTempDirectory("beacon-ov-backup").toFile()

    private class RecordingAdapter(private val folder: File) : PlatformAdapter {
        val dispatched = mutableListOf<String>()
        val warnings = mutableListOf<String>()
        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = folder
        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun dispatchConsoleCommand(command: String) { dispatched.add(command) }
        override fun info(msg: String) {}
        override fun warn(msg: String) { warnings.add(msg) }
        override fun error(msg: String, t: Throwable?) {}
    }

    private lateinit var adapter: RecordingAdapter

    private fun md5(s: String): String {
        val d = MessageDigest.getInstance("MD5").digest(s.toByteArray(StandardCharsets.UTF_8))
        return d.joinToString("") { "%02x".format(it) }
    }

    private fun newApplier(whitelist: Set<String>): OverrideApplier {
        adapter = RecordingAdapter(backupDir)
        return OverrideApplier(
            targetRoot = targetRoot,
            backupManager = BackupManager(backupDir),
            tracker = ManagedFileTracker(),
            pathSecurity = OverridePathSecurity(targetRoot),
            reloadExecutor = ReloadCommandExecutor(CommandWhitelist(whitelist), adapter),
            adapter = adapter,
        )
    }

    @AfterTest
    fun cleanup() {
        targetRoot.deleteRecursively()
        backupDir.deleteRecursively()
    }

    @Test
    fun `覆盖新文件 备份记不存在 并按白名单派发命令`() {
        val applier = newApplier(setOf("allin"))
        val files = listOf(OverrideFile("config.yml", "new", md5("new")))
        val ok = applier.apply("set1", files, reloadCommand = "allin reload")
        assertTrue(ok)
        assertEquals("new", File(targetRoot, "config.yml").readText(StandardCharsets.UTF_8))
        assertEquals(listOf("allin reload"), adapter.dispatched)
    }

    @Test
    fun `覆盖已存在文件 先备份原内容`() {
        val applier = newApplier(setOf("allin"))
        File(targetRoot, "config.yml").writeText("old", StandardCharsets.UTF_8)
        val records = applier.applyAndReturnBackups("set1", listOf(OverrideFile("config.yml", "new", md5("new"))), null)
        assertEquals("new", File(targetRoot, "config.yml").readText(StandardCharsets.UTF_8))
        // 备份保留了旧内容、记原本存在。
        val rec = records.single()
        assertTrue(rec.existedBefore)
        assertEquals("old", rec.readBackupContent())
    }

    @Test
    fun `检测到外部改动 告警而非盲盖`() {
        val applier = newApplier(setOf("allin"))
        // agent 先落一版，记基准。
        applier.apply("set1", listOf(OverrideFile("config.yml", "v1", md5("v1"))), null)
        // 插件自己把 config 改了。
        File(targetRoot, "config.yml").writeText("plugin-rewrote", StandardCharsets.UTF_8)
        // 下一轮 agent 想盖成 v2：应检测到外部改动，告警且不盖。
        val ok = applier.apply("set1", listOf(OverrideFile("config.yml", "v2", md5("v2"))), null)
        assertFalse(ok, "检测到外部改动应放弃覆盖")
        assertEquals("plugin-rewrote", File(targetRoot, "config.yml").readText(StandardCharsets.UTF_8))
        assertTrue(adapter.warnings.any { it.contains("外部") }, "应有外部改动告警")
    }

    @Test
    fun `非法路径跳过 不逃逸目标根 不阻断其余`() {
        val applier = newApplier(setOf("allin"))
        val files = listOf(
            OverrideFile("../escape.yml", "evil", md5("evil")),
            OverrideFile("good.yml", "ok", md5("ok")),
        )
        applier.apply("set1", files, null)
        assertFalse(File(targetRoot.parentFile, "escape.yml").exists(), "逃逸文件不应被创建")
        assertEquals("ok", File(targetRoot, "good.yml").readText(StandardCharsets.UTF_8))
    }

    @Test
    fun `成员路径是目录占位 读盘失败跳过告警 不停摆 其余照写`() {
        val applier = newApplier(setOf("allin"))
        // 目标位置已存在同名目录（占位 / 不可读），反馈环读盘会抛异常。
        File(targetRoot, "blocked.yml").mkdirs()
        val files = listOf(
            OverrideFile("blocked.yml", "x", md5("x")),
            OverrideFile("good.yml", "ok", md5("ok")),
        )
        // 关键：不抛异常（否则整个 override 异步循环会静默停摆）。
        val ok = applier.apply("set1", files, null)
        assertFalse(ok, "有文件读盘失败应记为未全量成功")
        assertEquals("ok", File(targetRoot, "good.yml").readText(StandardCharsets.UTF_8), "其余文件照常覆盖")
        assertTrue(adapter.warnings.any { it.contains("读盘失败") }, "应有读盘失败告警")
    }

    @Test
    fun `越白名单命令不派发`() {
        val applier = newApplier(setOf("allin"))
        applier.apply("set1", listOf(OverrideFile("config.yml", "x", md5("x"))), reloadCommand = "lp sync")
        assertTrue(adapter.dispatched.isEmpty(), "越白名单命令不应派发")
    }

    @Test
    fun `回滚只还原文件 不重放命令`() {
        val applier = newApplier(setOf("allin"))
        File(targetRoot, "config.yml").writeText("old", StandardCharsets.UTF_8)
        val records = applier.applyAndReturnBackups("set1", listOf(OverrideFile("config.yml", "new", md5("new"))), "allin reload")
        adapter.dispatched.clear()
        // 回滚。
        applier.rollback(records)
        assertEquals("old", File(targetRoot, "config.yml").readText(StandardCharsets.UTF_8))
        assertTrue(adapter.dispatched.isEmpty(), "回滚绝不重放重载命令")
    }
}

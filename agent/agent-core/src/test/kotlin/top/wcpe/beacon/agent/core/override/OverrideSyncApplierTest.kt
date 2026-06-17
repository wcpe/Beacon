package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.filetree.FileContent
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
 * 三方覆盖集同步编排接线单测（ADR-0011 决策 2/3/4/5/6）：
 * 取齐成员落 targetRoot → 命中白名单才派发命令；白名单空不派发并告警；取内容失败整集 fail-static 不落盘不派发；
 * 恶意 targetRoot（控制面被攻破）整集拒绝；同 overrideMd5 幂等跳过；fail-static 不臆测删盘。
 */
class OverrideSyncApplierTest {

    // 模拟服务器根，其下建 plugins 基目录；targetRoot 相对服务器根（plugins/<plugin>）。
    private val serverRoot: File = Files.createTempDirectory("beacon-srv").toFile()
    private val pluginsBase: File = File(serverRoot, "plugins").apply { mkdirs() }
    private val backupDir: File = Files.createTempDirectory("beacon-ov-backup2").toFile()

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

    private fun member(path: String, content: String) = FileContent(path, md5(content), content)

    private fun newApplier(
        whitelist: Set<String>,
        fetch: (String, String) -> FileContent?,
    ): OverrideSyncApplier {
        adapter = RecordingAdapter(backupDir)
        return OverrideSyncApplier(
            pluginsBaseFolder = pluginsBase,
            backupRoot = backupDir,
            whitelist = CommandWhitelist(whitelist),
            adapter = adapter,
            fetchMember = fetch,
        )
    }

    private fun manifest(md5: String, vararg sets: OverrideSetEntry) =
        OverrideManifest("prod", "lobby-1", md5, sets.toList())

    @AfterTest
    fun cleanup() {
        serverRoot.deleteRecursively()
        backupDir.deleteRecursively()
    }

    @Test
    fun `落 targetRoot 并按白名单派发命令`() {
        val applier = newApplier(setOf("allin")) { _, path -> member(path, "content-$path") }
        val ok = applier.apply(
            manifest("md5-1", OverrideSetEntry("AllinCore", "plugins/AllinCore", "allin reload", listOf("config.yml"))),
        )
        assertTrue(ok)
        val written = File(pluginsBase, "AllinCore/config.yml")
        assertEquals("content-config.yml", written.readText(StandardCharsets.UTF_8))
        assertEquals(listOf("allin reload"), adapter.dispatched)
    }

    @Test
    fun `白名单空 不派发命令并告警`() {
        val applier = newApplier(emptySet()) { _, path -> member(path, "x") }
        applier.apply(
            manifest("md5-1", OverrideSetEntry("AllinCore", "plugins/AllinCore", "allin reload", listOf("config.yml"))),
        )
        assertTrue(adapter.dispatched.isEmpty(), "白名单空不应派发任何命令")
        assertTrue(adapter.warnings.any { it.contains("白名单为空") }, "应告警白名单为空")
    }

    @Test
    fun `取成员内容失败 整集 fail-static 不落盘不派发`() {
        val applier = newApplier(setOf("allin")) { _, _ -> null } // 取内容恒失败
        val ok = applier.apply(
            manifest("md5-1", OverrideSetEntry("AllinCore", "plugins/AllinCore", "allin reload", listOf("config.yml"))),
        )
        assertFalse(ok, "取内容失败应返回未收敛")
        assertFalse(File(pluginsBase, "AllinCore/config.yml").exists(), "fail-static 不应落盘")
        assertTrue(adapter.dispatched.isEmpty(), "fail-static 不应派发命令")
    }

    @Test
    fun `恶意 targetRoot 逃逸 plugins 整集拒绝`() {
        val applier = newApplier(setOf("allin")) { _, path -> member(path, "evil") }
        val ok = applier.apply(
            manifest("md5-1", OverrideSetEntry("Evil", "plugins/../../etc", "allin reload", listOf("passwd"))),
        )
        assertFalse(ok, "恶意目标根应整集拒绝")
        assertFalse(File(serverRoot.parentFile, "etc/passwd").exists(), "逃逸文件不应被创建")
        assertTrue(adapter.dispatched.isEmpty(), "拒绝集不应派发命令")
        assertTrue(adapter.warnings.any { it.contains("目标根") }, "应有非法目标根告警")
    }

    @Test
    fun `同 overrideMd5 幂等跳过 不重复派发命令`() {
        var fetchCount = 0
        val applier = newApplier(setOf("allin")) { _, path -> fetchCount++; member(path, "x") }
        val m = manifest("md5-same", OverrideSetEntry("AllinCore", "plugins/AllinCore", "allin reload", listOf("config.yml")))
        applier.apply(m)
        val afterFirst = fetchCount
        adapter.dispatched.clear()
        applier.apply(m) // 同 md5 第二次
        assertEquals(afterFirst, fetchCount, "同 md5 不应重复取成员")
        assertTrue(adapter.dispatched.isEmpty(), "同 md5 不应重复派发命令")
    }

    @Test
    fun `取内容失败一轮后 下一轮恢复可落盘`() {
        var failFirst = true
        val applier = newApplier(setOf("allin")) { _, path ->
            if (failFirst) null else member(path, "recovered")
        }
        val m = manifest("md5-1", OverrideSetEntry("AllinCore", "plugins/AllinCore", "allin reload", listOf("config.yml")))
        assertFalse(applier.apply(m), "首轮取失败应未收敛")
        failFirst = false
        assertTrue(applier.apply(m), "恢复后应收敛")
        assertEquals("recovered", File(pluginsBase, "AllinCore/config.yml").readText(StandardCharsets.UTF_8))
    }
}

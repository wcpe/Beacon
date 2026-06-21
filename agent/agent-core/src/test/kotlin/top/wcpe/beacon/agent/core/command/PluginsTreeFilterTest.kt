package top.wcpe.beacon.agent.core.command

import java.nio.charset.StandardCharsets
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.test.fail

/**
 * 反向抓取过滤 + 上限纯函数 [PluginsTreeFilter] 穷举单测（FR-39，见 ADR-0027）：
 * - 排除项（不安全路径 / jar / 二进制）静默剔除、不致整体失败、不计入配额；
 * - 上限（单文件 / 总字节 / 文件数）任一超标 → 整体失败、不部分上传；
 * - 文本配置正常保留（按路径稳定排序回传）。
 */
class PluginsTreeFilterTest {

    /** 文本转字节（UTF-8）。 */
    private fun b(s: String): ByteArray = s.toByteArray(StandardCharsets.UTF_8)

    /** 断言通过并取文件集；拒绝则 fail。 */
    private fun accepted(outcome: FilterOutcome): List<IngestFile> = when (outcome) {
        is FilterOutcome.Accepted -> outcome.files
        is FilterOutcome.Rejected -> fail("期望通过，实际被拒：${outcome.reason}")
    }

    @Test
    fun `纯文本配置全部保留并按路径排序`() {
        val tree = mapOf(
            "config.yml" to b("k: v"),
            "lang/zh_CN.yml" to b("hello: 你好"),
            "data/notes.txt" to b("note"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("config.yml", "data/notes.txt", "lang/zh_CN.yml"), files.map { it.path })
        assertEquals("hello: 你好", files.first { it.path == "lang/zh_CN.yml" }.content)
    }

    @Test
    fun `排除 jar 后缀不区分大小写`() {
        val tree = mapOf(
            "AllinCore.jar" to b("MZ-binary"),
            "libs/dep.JAR" to b("x"),
            "nested/x.Jar" to b("y"),
            "keep.yml" to b("k: v"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("keep.yml"), files.map { it.path }, "jar 应被剔除，仅留文本")
    }

    @Test
    fun `排除含 NUL 字节的二进制`() {
        val tree = mapOf(
            "image.png" to byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47, 0x00, 0x01),
            "keep.yml" to b("k: v"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("keep.yml"), files.map { it.path }, "含 NUL 的二进制应剔除")
    }

    @Test
    fun `排除非法 UTF-8 字节序列`() {
        // 0xFF 0xFE 不是合法 UTF-8 起始字节 → 判二进制剔除。
        val tree = mapOf(
            "bad.dat" to byteArrayOf(0xFF.toByte(), 0xFE.toByte(), 0x41),
            "keep.yml" to b("k: v"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("keep.yml"), files.map { it.path }, "非法 UTF-8 应剔除")
    }

    @Test
    fun `保留合法多字节 UTF-8 文本`() {
        // 合法 UTF-8（中文 / emoji）不应被误判二进制。
        val tree = mapOf("i18n.yml" to b("title: 测试🚀"))
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(1, files.size)
        assertEquals("title: 测试🚀", files[0].content)
    }

    @Test
    fun `空文件视作文本保留`() {
        val tree = mapOf("empty.yml" to ByteArray(0))
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("empty.yml"), files.map { it.path })
        assertEquals("", files[0].content)
    }

    @Test
    fun `剔除穿越与绝对与反斜杠等不安全路径`() {
        val tree = mapOf(
            "../escape.yml" to b("a: 1"),
            "a/../../escape.yml" to b("a: 1"),
            "/etc/passwd" to b("root"),
            "a\\b.yml" to b("a: 1"),
            "c:foo.yml" to b("a: 1"),
            "CON" to b("a: 1"),
            "con.yml" to b("a: 1"),
            "trail.yml " to b("a: 1"), // 段尾空格（Windows 落盘剥离）→ 不安全，剔除
            "ok.yml" to b("a: 1"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(listOf("ok.yml"), files.map { it.path }, "仅合法路径保留")
    }

    @Test
    fun `单文件超 1MB 整体失败`() {
        val big = ByteArray((PluginIngestLimits.MAX_FILE_BYTES + 1).toInt()) { 'a'.code.toByte() }
        val tree = mapOf("huge.yml" to big, "small.yml" to b("k: v"))
        val outcome = PluginsTreeFilter.filter(tree)
        assertTrue(outcome is FilterOutcome.Rejected, "单文件超限应整体失败")
        assertTrue((outcome as FilterOutcome.Rejected).reason.contains("单文件"), "原因应指明单文件超限：${outcome.reason}")
    }

    @Test
    fun `恰好 1MB 单文件放行`() {
        val exact = ByteArray(PluginIngestLimits.MAX_FILE_BYTES.toInt()) { 'a'.code.toByte() }
        val files = accepted(PluginsTreeFilter.filter(mapOf("exact.bin.yml" to exact)))
        assertEquals(1, files.size, "恰好等于上限不算超")
    }

    @Test
    fun `文件数超上限整体失败`() {
        val tree = HashMap<String, ByteArray>()
        for (i in 0..PluginIngestLimits.MAX_FILES) { // MAX_FILES + 1 个文本文件
            tree["f$i.yml"] = b("k: v")
        }
        val outcome = PluginsTreeFilter.filter(tree)
        assertTrue(outcome is FilterOutcome.Rejected, "文件数超限应整体失败")
        assertTrue((outcome as FilterOutcome.Rejected).reason.contains("文件数"), "原因应指明文件数超限：${outcome.reason}")
    }

    @Test
    fun `jar 与二进制不计入文件数配额`() {
        // 文本文件恰好等于上限，再混入若干 jar / 二进制（应被剔除、不挤爆配额）→ 通过。
        val tree = HashMap<String, ByteArray>()
        for (i in 0 until PluginIngestLimits.MAX_FILES) {
            tree["f$i.yml"] = b("k: v")
        }
        tree["extra.jar"] = b("MZ")
        tree["bin.dat"] = byteArrayOf(0x00, 0x01)
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertEquals(PluginIngestLimits.MAX_FILES, files.size, "jar / 二进制不应计入文件数")
    }

    @Test
    fun `总字节超上限整体失败`() {
        // 用多个接近单文件上限的文本文件把总量推过 64MB（单个不超 1MB）。
        val perFile = ByteArray((PluginIngestLimits.MAX_FILE_BYTES).toInt()) { 'a'.code.toByte() } // 1MB
        val count = (PluginIngestLimits.MAX_TOTAL_BYTES / PluginIngestLimits.MAX_FILE_BYTES).toInt() + 1 // 65 个 → 65MB
        val tree = HashMap<String, ByteArray>()
        for (i in 0 until count) {
            tree["big$i.yml"] = perFile
        }
        val outcome = PluginsTreeFilter.filter(tree)
        assertTrue(outcome is FilterOutcome.Rejected, "总字节超限应整体失败")
        assertTrue((outcome as FilterOutcome.Rejected).reason.contains("聚合字节"), "原因应指明总字节超限：${outcome.reason}")
    }

    @Test
    fun `空树通过且回传空集`() {
        val files = accepted(PluginsTreeFilter.filter(emptyMap()))
        assertTrue(files.isEmpty(), "空树应通过且无文件")
    }

    @Test
    fun `全为排除项时通过且回传空集`() {
        val tree = mapOf(
            "plugin.jar" to b("MZ"),
            "world.dat" to byteArrayOf(0x00),
            "../escape.yml" to b("a: 1"),
        )
        val files = accepted(PluginsTreeFilter.filter(tree))
        assertTrue(files.isEmpty(), "全为剔除项 → 通过但空集（不整体失败）")
    }
}

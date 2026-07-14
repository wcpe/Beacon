package top.wcpe.beacon.agent.core.command

import top.wcpe.beacon.agent.core.browse.FsBrowseReader
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.security.MessageDigest
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 文件资产扫描 FS 边界 [AssetIndexReader] 单测（FR-163，见 ADR asset-manifest-sync-protocol）：
 * - 扫 `plugins/` 子树 + 根白名单配置文件，相对路径正斜杠、相对服务器工作目录；
 * - sha256 / size / mtime 与磁盘一致；jar 收入清单（isText=false）；
 * - 默认排除（.log / logs 目录等）剔除；
 * - (size,mtime) 命中缓存复用 sha256、force 重哈希；
 * - 符号链接逃逸 serverRoot 外被剔除（Windows 不支持符号链接时跳过）。
 */
class AssetIndexReaderTest {
    private val serverRoot: File = Files.createTempDirectory("beacon-asset-root").toFile()
    private val outside: File = Files.createTempDirectory("beacon-asset-outside").toFile()

    @AfterTest
    fun cleanup() {
        serverRoot.deleteRecursively()
        outside.deleteRecursively()
    }

    private fun write(
        rel: String,
        content: String,
    ): File {
        val f = File(serverRoot, rel)
        f.parentFile.mkdirs()
        f.writeText(content, StandardCharsets.UTF_8)
        return f
    }

    private fun sha256Hex(content: String): String =
        MessageDigest.getInstance("SHA-256")
            .digest(content.toByteArray(StandardCharsets.UTF_8))
            .joinToString("") { "%02x".format(it) }

    @Test
    fun `扫 plugins 子树与根白名单相对路径正斜杠`() {
        write("plugins/Foo/a.yml", "k: v")
        write("server.properties", "motd=hi")
        write("plugins/Foo/x.log", "noise") // 默认排除
        write("notes.txt", "random") // 非白名单根文件 → 不收
        val result = AssetIndexReader.scan(serverRoot, emptyMap(), force = false)
        val paths = result.entries.map { it.path }.toSet()
        assertEquals(setOf("plugins/Foo/a.yml", "server.properties"), paths)
        assertTrue(result.entries.none { it.path.contains('\\') }, "相对路径应为正斜杠")
        assertFalse(result.truncated)
    }

    @Test
    fun `readAsset 以扫描根能读扫描产出的两类 path（FR-164 根一致回归）`() {
        // 复现 FR-164 真机 bug：扫描根 = 服务器工作目录，asset-read 须用同根读扫描产出的 path。
        // 修复前双端 adapter 误用 pluginsBaseFolder（plugins 目录）为读取根：读 "plugins/Foo/a.yml" 拼成 plugins/plugins/... 读不到，根白名单更不在 plugins 内。
        write("plugins/Foo/a.yml", "k: v")
        write("server.properties", "motd=hi")
        val paths = AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries.map { it.path }.toSet()
        assertEquals(setOf("plugins/Foo/a.yml", "server.properties"), paths)
        // 正确根（serverRoot，与扫描一致）：扫描产出的两类 path（plugins 子路径 + 根白名单）都能读到内容。
        assertEquals("k: v", FsBrowseReader.readAsset(serverRoot, "plugins/Foo/a.yml", 0)?.content)
        assertEquals("motd=hi", FsBrowseReader.readAsset(serverRoot, "server.properties", 0)?.content)
        // 错误根（plugins 目录，修复前 adapter 的根）：读 plugins 子路径拼成 plugins/plugins/... → 读不到（复现 bug）。
        assertNull(FsBrowseReader.readAsset(File(serverRoot, "plugins"), "plugins/Foo/a.yml", 0))
    }

    @Test
    fun `sha256 与大小 mtime 与磁盘一致`() {
        val f = write("plugins/Foo/a.yml", "k: v")
        val e = AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries.first { it.path == "plugins/Foo/a.yml" }
        assertEquals(sha256Hex("k: v"), e.sha256)
        assertEquals(4L, e.size)
        assertEquals(Files.getLastModifiedTime(f.toPath()).toMillis(), e.mtimeMs, "mtime 应与磁盘一致")
        assertTrue(e.mtimeMs > 0, "mtime 应非零")
    }

    @Test
    fun `默认排除 log 与 logs 目录`() {
        write("plugins/x.log", "a")
        write("logs/latest.log", "b")
        write("plugins/cache/tmp.dat", "c") // cache 目录段命中排除
        write("plugins/keep.yml", "k: v")
        val paths = AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries.map { it.path }.toSet()
        assertEquals(setOf("plugins/keep.yml"), paths)
    }

    @Test
    fun `jar 收入清单 isText 为假`() {
        write("plugins/a.yml", "k: v")
        write("plugins/lib.jar", "MZ-binary")
        val entries = AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries
        assertTrue(entries.first { it.path == "plugins/a.yml" }.isText, "yml 应 isText=true")
        val jar = entries.firstOrNull { it.path == "plugins/lib.jar" }
        assertNotNull(jar, "jar 属清单内容（哈希 / 大小全收），不排除")
        assertFalse(jar.isText, "jar 应 isText=false")
        assertEquals(sha256Hex("MZ-binary"), jar.sha256, "jar 也算真实 sha256")
    }

    @Test
    fun `size与mtime命中缓存复用sha256 force则重哈希`() {
        val f = write("plugins/a.yml", "k: v")
        val fakeSha = "deadbeef".repeat(8)
        val mtime = Files.getLastModifiedTime(f.toPath()).toMillis()
        // 上次清单里 a.yml 的 (size,mtime) 与现磁盘一致、但 sha 是假值：命中缓存则复用假值，重哈希则得真值。
        val previous = mapOf("plugins/a.yml" to AssetEntry("plugins/a.yml", fakeSha, f.length(), mtime, true))
        val reused = AssetIndexReader.scan(serverRoot, previous, force = false).entries.first { it.path == "plugins/a.yml" }
        assertEquals(fakeSha, reused.sha256, "(size,mtime) 命中应复用上次 sha256、不重读文件")
        val forced = AssetIndexReader.scan(serverRoot, previous, force = true).entries.first { it.path == "plugins/a.yml" }
        assertEquals(sha256Hex("k: v"), forced.sha256, "force=true 应重哈希得真值")
    }

    @Test
    fun `size 不一致缓存不命中重哈希`() {
        val f = write("plugins/a.yml", "k: v2")
        val mtime = Files.getLastModifiedTime(f.toPath()).toMillis()
        val previous = mapOf("plugins/a.yml" to AssetEntry("plugins/a.yml", "deadbeef".repeat(8), 999L, mtime, true))
        val e = AssetIndexReader.scan(serverRoot, previous, force = false).entries.first { it.path == "plugins/a.yml" }
        assertEquals(sha256Hex("k: v2"), e.sha256, "size 不一致应重哈希")
    }

    @Test
    fun `agent 自身数据目录整棵排除`() {
        // 防自我指涉：agent 自写缓存 / 快照（每周期 / 每 10s 变）不得纳入清单，否则清单永不收敛。
        write("plugins/BeaconAgent/asset-manifest.json", "{}")
        write("plugins/BeaconAgent/candidates-snapshot.json", "[]")
        write("plugins/BeaconAgent/config.yml", "beacon: x")
        write("plugins/Other/keep.yml", "k: v")
        val selfDir = File(serverRoot, "plugins/BeaconAgent")
        val paths = AssetIndexReader.scan(serverRoot, emptyMap(), force = false, selfExcludeDir = selfDir).entries.map { it.path }.toSet()
        assertEquals(setOf("plugins/Other/keep.yml"), paths, "agent 自身目录整棵应被排除")
    }

    @Test
    fun `非目录 serverRoot 得空结果`() {
        val notDir = File(serverRoot, "afile").apply { writeText("x") }
        val result = AssetIndexReader.scan(notDir, emptyMap(), force = false)
        assertTrue(result.entries.isEmpty())
        assertFalse(result.truncated)
    }

    @Test
    fun `符号链接逃逸到 root 外被剔除`() {
        val secret = File(outside, "secret.txt").apply { writeText("TOP SECRET") }
        write("plugins/normal.yml", "k: v")
        val link = File(serverRoot, "plugins/leak.yml").toPath()
        // 环境不支持符号链接（Windows 无权限 / FS 不支持）→ 跳过，不让环境差异致测试红。
        if (!trySymlink(link, secret.toPath())) return
        val paths = AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries.map { it.path }
        assertTrue(paths.contains("plugins/normal.yml"), "root 内普通文件应保留")
        assertFalse(paths.contains("plugins/leak.yml"), "逃逸 serverRoot 的符号链接应剔除")
        assertTrue(
            AssetIndexReader.scan(serverRoot, emptyMap(), force = false).entries.none { it.sha256 == sha256Hex("TOP SECRET") },
            "绝不读出 root 外内容",
        )
    }

    private fun trySymlink(
        link: java.nio.file.Path,
        target: java.nio.file.Path,
    ): Boolean =
        try {
            Files.createSymbolicLink(link, target)
            true
        } catch (e: Exception) {
            false
        }
}

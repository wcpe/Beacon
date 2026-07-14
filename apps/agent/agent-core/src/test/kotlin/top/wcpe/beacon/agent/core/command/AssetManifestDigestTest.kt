package top.wcpe.beacon.agent.core.command

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotEquals

/**
 * 文件资产清单摘要算法 [AssetManifestDigest] 单测（FR-163，见 ADR asset-manifest-sync-protocol 决策 1）。
 *
 * 锁定：与控制面同源的拼接口径（path\nsha\nsize\nmtime\n）、path 升序、整体 sha256 小写 hex、
 * 顺序无关、空清单为空串 sha256。常量由独立工具（.NET SHA256）预算，锁死算法不漂移。
 */
class AssetManifestDigestTest {
    private fun entry(
        path: String,
        sha: String,
        size: Long,
        mtime: Long,
    ) = AssetEntry(path = path, sha256 = sha, size = size, mtimeMs = mtime, isText = true)

    @Test
    fun `固定两条清单摘要等于手算常量`() {
        val entries =
            listOf(
                entry("plugins/Foo/config.yml", "0000000000000000000000000000000000000000000000000000000000000001", 10, 1000),
                entry("server.properties", "0000000000000000000000000000000000000000000000000000000000000002", 20, 2000),
            )
        // 常量 = sha256("plugins/Foo/config.yml\n...0001\n10\n1000\nserver.properties\n...0002\n20\n2000\n")，独立算得。
        assertEquals(
            "0ebe64f9e91c48c48daad121d10041aba4f40ca29a14a57b3477ed1a650f1516",
            AssetManifestDigest.computeManifestDigest(entries),
        )
    }

    @Test
    fun `摘要与输入顺序无关`() {
        val a = entry("plugins/Foo/config.yml", "0000000000000000000000000000000000000000000000000000000000000001", 10, 1000)
        val b = entry("server.properties", "0000000000000000000000000000000000000000000000000000000000000002", 20, 2000)
        assertEquals(
            AssetManifestDigest.computeManifestDigest(listOf(a, b)),
            AssetManifestDigest.computeManifestDigest(listOf(b, a)),
        )
    }

    @Test
    fun `空清单摘要为空串的 sha256`() {
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            AssetManifestDigest.computeManifestDigest(emptyList()),
        )
    }

    @Test
    fun `任一维度变化摘要即变`() {
        val base = listOf(entry("a.yml", "1".repeat(64), 1, 1))
        // sha 变。
        assertNotEquals(
            AssetManifestDigest.computeManifestDigest(base),
            AssetManifestDigest.computeManifestDigest(listOf(entry("a.yml", "2".repeat(64), 1, 1))),
        )
        // 仅 mtime 变（摘要含 mtime，故也应变——增量收敛依赖此）。
        assertNotEquals(
            AssetManifestDigest.computeManifestDigest(base),
            AssetManifestDigest.computeManifestDigest(listOf(entry("a.yml", "1".repeat(64), 1, 2))),
        )
    }
}

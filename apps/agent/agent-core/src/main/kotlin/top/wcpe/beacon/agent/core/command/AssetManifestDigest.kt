package top.wcpe.beacon.agent.core.command

import java.nio.charset.StandardCharsets
import java.security.MessageDigest

/**
 * 文件资产清单摘要算法（FR-163，见 ADR asset-manifest-sync-protocol 决策 1）。**纯函数、无副作用，可穷举单测。**
 *
 * agent 与控制面**完全同源**：清单条目按 path 字节序升序，每条拼接
 * `path + "\n" + sha256 + "\n" + size + "\n" + mtimeMs + "\n"`，对全部条目拼接后的 UTF-8 字节取 sha256 小写十六进制。
 * 摘要相等即两侧清单逐字节一致，是增量协议的校准锚点。空清单 → 空字符串的 sha256。
 */
object AssetManifestDigest {
    /** 按锁定算法计算清单摘要（path 升序、逐条拼接、整体 sha256 小写 hex）。 */
    fun computeManifestDigest(entries: List<AssetEntry>): String {
        val sb = StringBuilder()
        // 按 path 升序（ASCII 路径下 Kotlin String 自然序即 UTF-8 字节序，与控制面一致）。
        for (entry in entries.sortedBy { it.path }) {
            sb.append(entry.path).append('\n')
                .append(entry.sha256).append('\n')
                .append(entry.size).append('\n')
                .append(entry.mtimeMs).append('\n')
        }
        val digest = MessageDigest.getInstance("SHA-256").digest(sb.toString().toByteArray(StandardCharsets.UTF_8))
        return digest.joinToString("") { "%02x".format(it) }
    }
}

package top.wcpe.beacon.agent.core.command

import java.nio.charset.CodingErrorAction
import java.nio.charset.StandardCharsets

/**
 * 反向抓取过滤 + 上限校验的**纯函数**（FR-39，见 ADR-0027）。无 IO、无副作用，便于穷举单测。
 *
 * 入参为「相对路径 → 原始字节」映射（由平台壳读真实 plugins 树得来，FS 级 Path 容纳与符号链接判定已在壳侧做）；
 * 出参为「过滤校验后的待 ingest 文本文件集」或一个明确的拒绝原因。
 *
 * 过滤口径（**排除项静默剔除，不致整体失败**）：
 * - 路径字符串不安全（[PluginsPathGuard]：`..` / 绝对 / 反斜杠 / 冒号 / 保留名 / 段尾点空格）→ 剔除；
 * - `.jar` 后缀（沿 ADR-0011，jar 属 P3 发布编排、非托管配置）→ 剔除；
 * - 二进制 / 非文本（含 NUL 字节或非合法 UTF-8）→ 剔除（通道B 只管文本配置）。
 *
 * 上限口径（**任一超限即整体失败、不部分上传**，与控制面 import 同口径，见 [PluginIngestLimits]）：
 * - 单文件字节 > 1MB；
 * - 保留文件总字节 > 64MB；
 * - 保留文件数 > 2000。
 *
 * 设计要点：先剔除排除项，再对**保留集**判上限——jar / 二进制不计入数量与总量，避免无关大文件挤爆配额。
 */
object PluginsTreeFilter {

    /**
     * 过滤并校验一棵 plugins 树。
     *
     * @param tree 相对路径（正斜杠分隔） → 原始字节
     * @return [FilterOutcome.Accepted]（待 ingest 文本文件集，按路径升序稳定排序）或 [FilterOutcome.Rejected]（超限原因）
     */
    fun filter(tree: Map<String, ByteArray>): FilterOutcome {
        val kept = ArrayList<IngestFile>(tree.size)
        var totalBytes = 0L

        // 稳定顺序遍历（按路径排序）：让单文件超限的报错与回传顺序确定、可复现、便于测试。
        for ((path, bytes) in tree.toSortedMap()) {
            // 1) 排除项：路径不安全 / jar / 二进制——静默剔除，不计入配额、不致整体失败。
            if (!PluginsPathGuard.isSafe(path)) continue
            if (isJar(path)) continue
            val text = decodeUtf8OrNull(bytes) ?: continue // 非合法 UTF-8 → 二进制，剔除

            // 2) 单文件上限：超限即整体失败（不部分上传）。
            if (bytes.size.toLong() > PluginIngestLimits.MAX_FILE_BYTES) {
                return FilterOutcome.Rejected(
                    "单文件超 ${PluginIngestLimits.MAX_FILE_BYTES} 字节上限：$path（${bytes.size} 字节）",
                )
            }

            kept.add(IngestFile(path = path, content = text))
            totalBytes += bytes.size.toLong()
        }

        // 3) 文件数 / 总字节上限：对保留集判，超限即整体失败。
        if (kept.size > PluginIngestLimits.MAX_FILES) {
            return FilterOutcome.Rejected("文本文件数 ${kept.size} 超 ${PluginIngestLimits.MAX_FILES} 上限")
        }
        if (totalBytes > PluginIngestLimits.MAX_TOTAL_BYTES) {
            return FilterOutcome.Rejected("聚合字节 $totalBytes 超 ${PluginIngestLimits.MAX_TOTAL_BYTES} 上限")
        }
        return FilterOutcome.Accepted(kept)
    }

    /** 是否 `.jar` 后缀（不区分大小写）。 */
    private fun isJar(path: String): Boolean = path.lowercase().endsWith(".jar")

    /**
     * 严格按 UTF-8 解码字节；含 NUL 字节或非合法 UTF-8 视作二进制返回 null。
     *
     * NUL 字节（0x00）虽是合法 UTF-8 码点，但几乎只出现在二进制文件，是文本/二进制最稳的判别信号，单独先拦。
     * 其余非法序列交严格解码器（MALFORMED/UNMAPPABLE 一律 REPORT 抛异常）判定。
     */
    private fun decodeUtf8OrNull(bytes: ByteArray): String? {
        for (b in bytes) {
            if (b.toInt() == 0) return null // NUL 字节 → 判为二进制
        }
        return try {
            val decoder = StandardCharsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
            decoder.decode(java.nio.ByteBuffer.wrap(bytes)).toString()
        } catch (e: java.nio.charset.CharacterCodingException) {
            null // 非合法 UTF-8 → 判为二进制
        }
    }
}

/** 待 ingest 的单个文本文件（相对路径 + 文本内容）。 */
data class IngestFile(
    val path: String,
    val content: String,
)

/** [PluginsTreeFilter.filter] 的结果。 */
sealed class FilterOutcome {

    /** 通过：携带过滤校验后的待 ingest 文本文件集（可空，表示无文本可传）。 */
    data class Accepted(val files: List<IngestFile>) : FilterOutcome()

    /** 拒绝：某项上限超限，整体失败、不部分上传，携带可读原因（无敏感内容）。 */
    data class Rejected(val reason: String) : FilterOutcome()
}

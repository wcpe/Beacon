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

    /**
     * scan 模式（FR-58，见 ADR-0037）：只对**元信息**（path→size）做安全过滤 + 标注，**永不失败**。
     *
     * 与 [filter] 的根本区别：不读内容、不判上限失败——超阈值文件以 `overThreshold=true` 列出而非整批拒绝。
     * 过滤口径与 [filter] 一致（不安全路径 / `.jar` 静默剔除）；isText 仅按路径名启发判定，二进制扩展名标 false
     * （scan 不读字节，无法靠 UTF-8 解码精判；真正的内容文本判定在 submit 读内容时由 [submitFilter] 兜底）。
     *
     * @param metadata 相对路径（正斜杠分隔） → 文件字节大小（由壳层只 `stat` 取得，不含内容）
     * @return 安全过滤后的元信息清单（按路径升序稳定排序；超阈值项 overThreshold=true 仍列出）
     */
    fun scan(metadata: Map<String, Long>): List<ScanFile> {
        val result = ArrayList<ScanFile>(metadata.size)
        for ((path, size) in metadata.toSortedMap()) {
            // 排除项：路径不安全 / jar 静默剔除（与 filter 同口径）；scan 不读内容，故不在此判二进制 NUL。
            if (!PluginsPathGuard.isSafe(path)) continue
            if (isJar(path)) continue
            result.add(
                ScanFile(
                    path = path,
                    size = size,
                    isText = looksTextByName(path),
                    // 超单文件阈值：仅红标，绝不因此失败（治根：超限运行时垃圾在清单里被看见、由人决定纳入/排除）。
                    overThreshold = size > PluginIngestLimits.MAX_FILE_BYTES,
                ),
            )
        }
        return result
    }

    /**
     * submit 模式（FR-58，见 ADR-0037）：按选定 path **子集**读内容过滤回传。
     *
     * 复用既有读内容 + 安全过滤口径（[filter]），但**只处理选定集内的 path**（不在选定集的整树文件一律不回传）。
     * 安全边界一条不松（不安全路径 / jar / 二进制仍剔除）；选定集里的超阈值文件由控制面侧"须确认"门控，
     * agent 只忠实回传选定——故 submit **不再判单文件超限失败**，仅文件数 / 总字节作兜底（防异常巨大提交）。
     *
     * @param tree          整棵 plugins 树（相对路径 → 原始字节，由壳层读盘得来）
     * @param selectedPaths 控制面选定回传的相对 path 子集
     * @return [FilterOutcome.Accepted]（选定集内的待 ingest 文本文件，按路径升序）或 [FilterOutcome.Rejected]（兜底超限）
     */
    fun submitFilter(tree: Map<String, ByteArray>, selectedPaths: Collection<String>): FilterOutcome {
        val selected = selectedPaths.toHashSet()
        val kept = ArrayList<IngestFile>(selected.size)
        var totalBytes = 0L

        for ((path, bytes) in tree.toSortedMap()) {
            // 只回传选定集内的 path：不在选定集的整树文件直接跳过（submit 的本质——只抓选定）。
            if (path !in selected) continue
            // 安全过滤口径不变（不安全路径 / jar / 二进制剔除）。
            if (!PluginsPathGuard.isSafe(path)) continue
            if (isJar(path)) continue
            val text = decodeUtf8OrNull(bytes) ?: continue // 非合法 UTF-8 → 二进制，剔除

            kept.add(IngestFile(path = path, content = text))
            totalBytes += bytes.size.toLong()
        }

        // 文件数 / 总字节仅作 submit 兜底（防一次提交异常巨大）；单文件超限不再整批失败（选定集已由控制面确认）。
        if (kept.size > PluginIngestLimits.MAX_FILES) {
            return FilterOutcome.Rejected("提交文本文件数 ${kept.size} 超 ${PluginIngestLimits.MAX_FILES} 上限")
        }
        if (totalBytes > PluginIngestLimits.MAX_TOTAL_BYTES) {
            return FilterOutcome.Rejected("提交聚合字节 $totalBytes 超 ${PluginIngestLimits.MAX_TOTAL_BYTES} 上限")
        }
        return FilterOutcome.Accepted(kept)
    }

    /** 是否 `.jar` 后缀（不区分大小写）。 */
    private fun isJar(path: String): Boolean = path.lowercase().endsWith(".jar")

    /**
     * 按文件名后缀启发判定是否疑似文本（scan 不读字节，只能靠名）。
     *
     * 命中常见二进制扩展名（图片 / 压缩 / 库 / 序列化数据等）判 false；其余一律 true（保守倾向文本，
     * 由 submit 读内容时再精判，scan 阶段宁可多标一个文本也不漏标该被人看见的配置）。
     * 口径与浏览（FR-109）共用，集中在 [TextFileHeuristic]（避免二进制扩展名集合复制散落）。
     */
    private fun looksTextByName(path: String): Boolean = TextFileHeuristic.looksTextByName(path)

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

/**
 * scan 模式回传的单个文件元信息（FR-58，见 ADR-0037）：只含元数据、**不含内容**。
 *
 * @param path          相对路径（正斜杠分隔）
 * @param size          文件字节大小（壳层只 `stat` 取得）
 * @param isText        是否疑似文本（scan 按名启发判定，submit 读内容时精判）
 * @param overThreshold 是否超单文件阈值（仅红标，不致失败）
 */
data class ScanFile(
    val path: String,
    val size: Long,
    val isText: Boolean,
    val overThreshold: Boolean,
)

/** [PluginsTreeFilter.filter] 的结果。 */
sealed class FilterOutcome {

    /** 通过：携带过滤校验后的待 ingest 文本文件集（可空，表示无文本可传）。 */
    data class Accepted(val files: List<IngestFile>) : FilterOutcome()

    /** 拒绝：某项上限超限，整体失败、不部分上传，携带可读原因（无敏感内容）。 */
    data class Rejected(val reason: String) : FilterOutcome()
}

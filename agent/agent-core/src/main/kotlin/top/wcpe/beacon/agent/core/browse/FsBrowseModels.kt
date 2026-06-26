package top.wcpe.beacon.agent.core.browse

/**
 * 只读文件浏览的回传数据模型（FR-109，见 ADR-0049）。纯数据类、无 IO。
 *
 * 与反向抓取 ScanFile 不同：浏览是「点开才看」的惰加载，相对路径都以 `plugins/` 根（正斜杠分隔）为基准。
 */

/**
 * 一个浏览条目（目录或文件）的元信息。
 *
 * @param name    条目名（单段，不含路径分隔符）
 * @param relPath 相对 `plugins/` 根的相对路径（正斜杠分隔，跨平台一致）
 * @param dir     是否目录
 * @param size    文件字节大小（目录为 0）
 * @param text    是否疑似文本（按名启发判定；目录为 false）
 */
data class BrowseEntry(
    val name: String,
    val relPath: String,
    val dir: Boolean,
    val size: Long,
    val text: Boolean,
)

/**
 * 列目录（懒列）的分页结果（FR-109 原语①）。
 *
 * @param path    被列目录的相对路径（空串 = `plugins/` 根）
 * @param entries 本页直接子项（目录优先 + 名称升序稳定排序后的切片）
 * @param offset  本页起始偏移
 * @param limit   本页请求条数上限
 * @param total   该目录直接子项总数（用于前端翻页）
 * @param hasMore 是否还有更多（offset + entries.size < total）
 */
data class DirListing(
    val path: String,
    val entries: List<BrowseEntry>,
    val offset: Int,
    val limit: Int,
    val total: Int,
    val hasMore: Boolean,
)

/**
 * 读文件树（按需展开子树）的一个节点（FR-109 原语②）。
 *
 * 逐层有界：超出展开深度 / 节点上限的目录 `truncated=true`、children 为空（前端可继续点开懒列）。
 *
 * @param name      节点名（单段）
 * @param relPath   相对 `plugins/` 根的相对路径（正斜杠分隔）
 * @param dir       是否目录
 * @param size      文件字节大小（目录为 0）
 * @param text      是否疑似文本（目录为 false）
 * @param children  子节点（仅目录有；未展开 / 文件为空列表）
 * @param truncated 目录是否因深度 / 节点上限未完全展开（true 表示还有未列出的子项）
 */
data class TreeNode(
    val name: String,
    val relPath: String,
    val dir: Boolean,
    val size: Long,
    val text: Boolean,
    val children: List<TreeNode>,
    val truncated: Boolean,
)

/**
 * 读单文件内容的结果（FR-109 原语③）。
 *
 * @param path      文件相对路径（正斜杠分隔）
 * @param content   文本内容（UTF-8 解码；超单文件上限时为截断前缀）
 * @param truncated 是否因超单文件上限被截断（true 表示 content 非全文）
 */
data class FileContent(
    val path: String,
    val content: String,
    val truncated: Boolean,
)

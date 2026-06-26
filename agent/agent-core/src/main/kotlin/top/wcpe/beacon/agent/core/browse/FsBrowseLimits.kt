package top.wcpe.beacon.agent.core.browse

import top.wcpe.beacon.agent.core.command.PluginIngestLimits

/**
 * 只读交互式文件浏览的硬上限（FR-109，见 ADR-0049 决策 6）。
 *
 * 浏览是「点开才读」的惰加载，与反向抓取一次性 scan 不同：列目录分页、读子树逐层有界、读单文件受单文件上限。
 * 单文件内容上限**复用** [PluginIngestLimits.MAX_FILE_BYTES]（与反向抓取同口径，不另立常量），
 * 列目录 / 读子树的体量上限是浏览专属，集中在此。
 */
object FsBrowseLimits {

    /** 列目录单页最多返回的直接子项数（防大目录一次性拉全；超出由 offset/limit 翻页）。 */
    const val MAX_DIR_PAGE: Int = 500

    /** 列目录单次请求允许的最大 limit（请求超此值即收口到此，防越界拉全）。 */
    const val MAX_LIST_LIMIT: Int = MAX_DIR_PAGE

    /** 读子树允许展开的最大深度（自请求根算起；超此深度的层不再展开，逐层有界）。 */
    const val MAX_TREE_DEPTH: Int = 8

    /** 读子树单次返回的最大节点数（含目录与文件；达上限即停止再收，非整盘一次拉全）。 */
    const val MAX_TREE_NODES: Int = 2000

    /** 单文件内容读取上限（字节）——复用反向抓取单文件阈值（1MB），超限不读全文。 */
    const val MAX_FILE_BYTES: Long = PluginIngestLimits.MAX_FILE_BYTES
}

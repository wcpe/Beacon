package top.wcpe.beacon.agent.core.command

/**
 * 反向抓取 ingest 的硬上限（FR-39，见 ADR-0027）。
 *
 * 与控制面 import 常量同口径（internal/service/file_service.go：MaxFileContentBytes / MaxImportFiles /
 * MaxImportTotalBytes），agent 作最终权威先按此过滤，控制面入库前再同口径校验（双保险）。
 * 任一超限即**整体失败、不部分上传**——避免半截覆盖污染基线。
 */
object PluginIngestLimits {

    /** 单文件内容字节上限（1MB）。 */
    const val MAX_FILE_BYTES: Long = 1024L * 1024L

    /** 单次抓取聚合字节上限（64MB）。 */
    const val MAX_TOTAL_BYTES: Long = 64L * 1024L * 1024L

    /** 单次抓取文件数上限。 */
    const val MAX_FILES: Int = 2000
}

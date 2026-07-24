package top.wcpe.beacon.agent.core.filetree

/**
 * 文件树有效清单（对应 files/manifest 200 响应，通道B）。
 *
 * 只含 path→md5（不含内容）；agent 据此与本地已落盘清单比对，仅取/删变更文件。
 * 与有效配置 md5 相互独立（见 ADR-0010）。
 *
 * @param namespace   环境
 * @param serverId    本机身份
 * @param group       控制面解析的大区
 * @param zone        控制面指派的小区（未指派为 null）
 * @param fileTreeMd5 整棵文件树指纹（小写 hex）
 * @param entries     有效文件清单（path→md5，不含内容）
 */
data class FileManifest(
    val namespace: String,
    val serverId: String,
    val group: String?,
    val zone: String?,
    val fileTreeMd5: String,
    val entries: List<FileManifestEntry>,
)

/**
 * 清单中的单个文件条目（path→md5，不含内容）。
 *
 * @param path 相对路径（落盘相对目标根；禁绝对 / `..` 穿越 / 反斜杠，由落盘前校验）
 * @param md5  单文件内容指纹（小写 hex）
 */
data class FileManifestEntry(
    val path: String,
    val md5: String,
)

/**
 * 取单个文件内容的结果（对应 files/content 200 响应，通道B）。
 *
 * @param path    相对路径
 * @param md5     单文件内容指纹
 * @param content 整文件文本
 */
data class FileContent(
    val path: String,
    val md5: String,
    val content: String,
)

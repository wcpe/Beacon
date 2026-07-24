package top.wcpe.beacon.agent.core.command

/**
 * 文件资产清单的一个条目（FR-163）：相对服务器工作目录的一个文件的索引元数据（不含内容）。
 *
 * @param path    相对服务器工作目录的规范化相对路径，正斜杠分隔（如 plugins/Foo/config.yml、server.properties）
 * @param sha256  文件内容 sha256 小写十六进制
 * @param size    文件字节数
 * @param mtimeMs 文件修改时间，UTC epoch 毫秒
 * @param isText  按扩展名启发的文本提示（弱提示，非权威二进制判定；权威判定在预览期做）
 */
data class AssetEntry(
    val path: String,
    val sha256: String,
    val size: Long,
    val mtimeMs: Long,
    val isText: Boolean,
)

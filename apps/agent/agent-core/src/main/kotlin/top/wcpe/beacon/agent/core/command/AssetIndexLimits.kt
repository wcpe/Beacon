package top.wcpe.beacon.agent.core.command

/**
 * 文件资产索引扫描的硬上限（FR-163，见 ADR asset-manifest-sync-protocol）。
 *
 * 与控制面同口径：单服清单文件数上限、哈希分块读取块大小、单次清单上报条目上限（超限即分片）。
 */
object AssetIndexLimits {
    /** 单服清单文件数上限（规格 §4.1）：超限按路径字节序截断、truncated=true 随概要上报。 */
    const val MAX_FILES: Int = 50000

    /** 哈希分块读取块大小（128 KiB，规格 §4.2）：逐块喂 MessageDigest，绝不整文件载内存。 */
    const val HASH_CHUNK_BYTES: Int = 128 * 1024

    /** 单次清单上报条目上限（规格 §4.3）：全量超此数即分片（uploadId + seq，末批 eof）。 */
    const val MAX_MANIFEST_UPLOAD: Int = 2000
}

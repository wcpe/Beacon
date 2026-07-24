package top.wcpe.beacon.agent.core.delivery

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest

/**
 * 交付数据面文件 sha256 分块计算（FR-165）。
 *
 * 与 [top.wcpe.beacon.agent.core.command.AssetIndexReader] 同口径：逐 128 KiB 块喂 MessageDigest，
 * **绝不整文件载内存**（大 jar / 地图包场景内存安全）。纯 java.io、无副作用，供覆盖器本地比对与下载器校验复用。
 */
object DeliverySha256 {
    /** 哈希分块读取块大小（128 KiB）：逐块喂摘要，绝不整文件载内存。 */
    private const val HASH_CHUNK_BYTES: Int = 128 * 1024

    /** 分块算文件 sha256 小写 hex；读盘失败返回 null（由调用方按缺失处理）。 */
    fun ofFile(file: File): String? =
        try {
            val digest = MessageDigest.getInstance("SHA-256")
            file.inputStream().use { input -> updateDigest(digest, input) }
            digest.digest().joinToString("") { "%02x".format(it) }
        } catch (e: IOException) {
            null
        }

    /** 逐 128 KiB 块喂 MessageDigest，直至流末。 */
    private fun updateDigest(
        digest: MessageDigest,
        input: InputStream,
    ) {
        val buffer = ByteArray(HASH_CHUNK_BYTES)
        while (true) {
            val n = input.read(buffer)
            if (n < 0) break
            digest.update(buffer, 0, n)
        }
    }
}

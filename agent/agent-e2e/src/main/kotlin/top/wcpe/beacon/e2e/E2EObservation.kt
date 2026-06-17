package top.wcpe.beacon.e2e

import java.io.File
import java.security.MessageDigest
import java.time.OffsetDateTime

/**
 * E2E 验收观测的共用写出工具：供覆盖探针（[OverrideE2EProbe]）与文件树探针（[FileTreeE2EProbe]）共享，
 * 统一标记文件的字节 md5 计算与单行追加格式，避免各探针重复实现。
 *
 * 标记行格式：时间 | 来源 | path | md5 | 内容（内容里换行转义为 \\n，回车剔除），外部驱动按行 SplitN 解析。
 */
object E2EObservation {

    /** 写文件锁：命令回调（主线程）与轮询（异步线程）可能并发追加，串行化。 */
    private val writeLock = Any()

    /** 计算字节 md5（小写 hex，按字节基准，避免编码 / BOM 往返差异）。 */
    fun md5Hex(bytes: ByteArray): String {
        val digest = MessageDigest.getInstance("MD5").digest(bytes)
        return digest.joinToString("") { "%02x".format(it) }
    }

    /** 向标记文件追加一行观测。 */
    fun append(file: File, source: String, path: String, md5: String, raw: String) {
        synchronized(writeLock) {
            file.parentFile?.mkdirs()
            val escaped = raw.replace("\\", "\\\\").replace("\n", "\\n").replace("\r", "")
            val line = "${OffsetDateTime.now()}|$source|$path|$md5|$escaped\n"
            file.appendText(line, Charsets.UTF_8)
        }
    }
}

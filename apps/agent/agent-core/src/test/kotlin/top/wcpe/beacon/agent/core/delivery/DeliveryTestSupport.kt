package top.wcpe.beacon.agent.core.delivery

import java.io.File
import java.nio.file.Files
import java.security.MessageDigest

/** 交付单测共用：sha256 小写 hex + 临时目录 / 文件创建。 */
object DeliveryTestSupport {
    /** 计算字节数组 sha256 小写 hex（与 [DeliverySha256] 同口径，供构造清单期望值）。 */
    fun sha256(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

    /** 新建一次性临时目录。 */
    fun tempDir(prefix: String): File = Files.createTempDirectory(prefix).toFile()

    /** 在 [root] 下按相对路径写文件（自动建父目录），返回该文件。 */
    fun writeFile(
        root: File,
        relPath: String,
        content: ByteArray,
    ): File {
        val file = File(root, relPath)
        file.parentFile?.mkdirs()
        file.writeBytes(content)
        return file
    }
}

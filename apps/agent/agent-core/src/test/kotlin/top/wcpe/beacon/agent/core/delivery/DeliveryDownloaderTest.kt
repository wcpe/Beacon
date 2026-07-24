package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.testutil.FakeBlobStreamTransport
import top.wcpe.beacon.agent.core.transport.BlobDownloadOutcome
import java.io.File
import java.io.IOException
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 交付下载器 [DeliveryDownloader] 单测（FR-165，spec §4.5.3）：
 * - 中断后按 Range 从已收字节续传，最终齐全并校验通过；
 * - 校验不符删除重下，重下正确即成功；
 * - 校验持续不符，重试上限（3）后判失败。
 */
class DeliveryDownloaderTest {
    private val tempDir: File = DeliveryTestSupport.tempDir("delivery-dl-tmp")
    private val adapter = ManualAsyncAdapter(tempDir)
    private val transport = FakeBlobStreamTransport()

    private fun downloader(): DeliveryDownloader =
        DeliveryDownloader(transport, blobUrl = { it }, authHeaders = { emptyMap() }, adapter = adapter)

    @Test
    fun `中断后按 Range 续传直至齐全并校验通过`() {
        val content = ByteArray(100) { (it % 251).toByte() }
        val sha = DeliveryTestSupport.sha256(content)
        val half = 40
        var call = 0
        transport.onDownload = { _, rangeStart, sink ->
            call++
            if (call == 1) {
                // 首次：写一半后模拟网络中断（保留部分供续传）。
                sink.write(content, 0, half)
                sink.flush()
                throw IOException("模拟网络中断")
            } else {
                val start = rangeStart.toInt()
                sink.write(content, start, content.size - start)
                BlobDownloadOutcome(206, (content.size - start).toLong())
            }
        }

        val result = downloader().downloadAll(listOf(op("plugins/big.bin", sha, content.size.toLong())), tempDir)

        assertTrue(result.ok)
        assertContentEquals(content, File(tempDir, "plugins/big.bin").readBytes())
        assertEquals(listOf(0L, half.toLong()), transport.downloadCalls.map { it.second }, "第二次应带 rangeStart 续传")
    }

    @Test
    fun `校验不符删除重下重下正确即成功`() {
        val content = "correct-content".toByteArray()
        val sha = DeliveryTestSupport.sha256(content)
        val wrong = ByteArray(content.size) { 0 } // 同长度、错内容
        var call = 0
        transport.onDownload = { _, _, sink ->
            call++
            if (call == 1) sink.write(wrong) else sink.write(content)
            BlobDownloadOutcome(200, content.size.toLong())
        }

        val result = downloader().downloadAll(listOf(op("plugins/x.dat", sha, content.size.toLong())), tempDir)

        assertTrue(result.ok)
        assertContentEquals(content, File(tempDir, "plugins/x.dat").readBytes())
        assertEquals(listOf(0L, 0L), transport.downloadCalls.map { it.second }, "校验不符删除后应从头重下")
    }

    @Test
    fun `校验持续不符重试上限后判失败`() {
        val content = "want".toByteArray()
        val sha = DeliveryTestSupport.sha256(content)
        transport.onDownload = { _, _, sink ->
            sink.write("junk".toByteArray()) // 同长度错内容
            BlobDownloadOutcome(200, 4L)
        }

        val result = downloader().downloadAll(listOf(op("plugins/y.dat", sha, content.size.toLong())), tempDir)

        assertFalse(result.ok)
        assertTrue(result.error.contains("plugins/y.dat"))
        assertEquals(3, transport.downloadCalls.size, "下载重试上限为 3")
    }

    private fun op(
        path: String,
        sha: String,
        size: Long,
    ) = DeliveryFileOp(path, DeliveryFileOp.Kind.ADD, sha, size)
}

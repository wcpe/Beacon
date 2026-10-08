package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.testutil.FakeBlobStreamTransport
import top.wcpe.beacon.agent.core.transport.BlobHeadResult
import top.wcpe.beacon.agent.core.transport.BlobTransferOutcome
import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 交付上传器 [DeliveryUploader] 单测（FR-165，spec §4.5.2）：
 * - HEAD 就绪 → 去重跳过，不 PUT；
 * - HEAD 未就绪 → 流式 PUT 成功；
 * - PUT 持续失败 → 整文件重试上限（3）后判失败，且每次重试前按指数退避等待（FR-269）。
 */
class DeliveryUploaderTest {
    private val serverRoot: File = DeliveryTestSupport.tempDir("delivery-up-src")
    private val adapter = ManualAsyncAdapter(serverRoot)
    private val transport = FakeBlobStreamTransport()

    /** 记录退避等待时长（注入 sleep，测试不真等）。 */
    private val delays = mutableListOf<Long>()

    private fun uploader(): DeliveryUploader =
        DeliveryUploader(
            transport = transport,
            resolver = DeliveryTargetResolver(serverRoot, File(serverRoot, "no-such-agent-dir")),
            blobUrl = { it }, // url 即 sha，便于断言
            authHeaders = { emptyMap() },
            adapter = adapter,
            backoff = BACKOFF,
            sleep = delays::add,
        )

    @Test
    fun `HEAD 就绪则去重跳过不上传`() {
        val bytes = "jar-content".toByteArray()
        val sha = DeliveryTestSupport.sha256(bytes)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/a.jar", bytes)
        transport.onHead = { BlobHeadResult(200, ready = true, sizeBytes = bytes.size.toLong()) }

        val result = uploader().upload(listOf(DeliveryUploadItem("plugins/a.jar", sha, bytes.size.toLong())))

        assertTrue(result.ok)
        assertEquals(1, result.deduped)
        assertEquals(0, result.uploaded)
        assertTrue(transport.uploadUrls.isEmpty(), "已就绪应跳过 PUT")
    }

    @Test
    fun `HEAD 未就绪则流式 PUT 成功`() {
        val bytes = "new-jar".toByteArray()
        val sha = DeliveryTestSupport.sha256(bytes)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/b.jar", bytes)
        transport.onHead = { BlobHeadResult(404, ready = false, sizeBytes = -1L) }
        transport.onUpload = { _, len, _ -> if (len == bytes.size.toLong()) BlobTransferOutcome(204) else BlobTransferOutcome(500) }

        val result = uploader().upload(listOf(DeliveryUploadItem("plugins/b.jar", sha, bytes.size.toLong())))

        assertTrue(result.ok)
        assertEquals(1, result.uploaded)
        assertEquals(listOf(sha), transport.uploadUrls)
    }

    @Test
    fun `PUT 持续失败则重试三次后判失败`() {
        val bytes = "flaky".toByteArray()
        val sha = DeliveryTestSupport.sha256(bytes)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/c.jar", bytes)
        transport.onHead = { BlobHeadResult(404, ready = false, sizeBytes = -1L) }
        transport.onUpload = { _, _, _ -> BlobTransferOutcome(500) }

        val result = uploader().upload(listOf(DeliveryUploadItem("plugins/c.jar", sha, bytes.size.toLong())))

        assertFalse(result.ok)
        assertTrue(result.error.contains("plugins/c.jar"))
        assertEquals(3, transport.uploadUrls.size, "整文件重试上限为 3")
    }

    @Test
    fun `源文件缺失判失败不上传`() {
        transport.onHead = { BlobHeadResult(404, ready = false, sizeBytes = -1L) }

        val result = uploader().upload(listOf(DeliveryUploadItem("plugins/missing.jar", "deadbeef", 10L)))

        assertFalse(result.ok)
        assertTrue(transport.uploadUrls.isEmpty())
    }

    @Test
    fun `上传重试间按指数退避等待`() {
        val bytes = "flaky".toByteArray()
        val sha = DeliveryTestSupport.sha256(bytes)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/d.jar", bytes)
        transport.onHead = { BlobHeadResult(404, ready = false, sizeBytes = -1L) }
        transport.onUpload = { _, _, _ -> BlobTransferOutcome(503) }

        val result = uploader().upload(listOf(DeliveryUploadItem("plugins/d.jar", sha, bytes.size.toLong())))

        assertFalse(result.ok)
        assertEquals(listOf(10L, 20L), delays, "重试间隔应按指数退避递增（10 → 20），且不等待最后一次之后的间隔")
    }

    private companion object {
        /** 退避设置：抖动归零、间隔小且可预期（10 → 20），配合注入 sleep 断言确定性序列。 */
        private val BACKOFF = BackoffSettings(initialMs = 10, maxMs = 100, multiplier = 2.0, jitterRatio = 0.0)
    }
}

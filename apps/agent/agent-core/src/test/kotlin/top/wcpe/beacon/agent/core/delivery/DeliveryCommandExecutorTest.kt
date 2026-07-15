package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.command.DeliveryCommandPayload
import top.wcpe.beacon.agent.core.command.IngestCommandPayload
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.testutil.FakeBlobStreamTransport
import top.wcpe.beacon.agent.core.transport.BlobDownloadOutcome
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 交付命令执行器 [DeliveryCommandExecutor] 推送编排单测（FR-165，spec §4.5.3 / §4.7.1）：
 * - push 成功：下载 → 备份 → 覆盖 / 删除 / 跳过 → 清临时目录 → 回执 success（含 changed/skipped/backupPresent）；
 * - 备份失败：绝不覆盖原文件，回执 failed。
 */
class DeliveryCommandExecutorTest {
    private val serverRoot: File = DeliveryTestSupport.tempDir("delivery-exec-root")
    private val dataDir: File = DeliveryTestSupport.tempDir("delivery-exec-data")
    private val adapter = ManualAsyncAdapter(dataDir)
    private val blob = FakeBlobStreamTransport()

    private val updNew = "NEW-CONTENT".toByteArray()
    private val addContent = "ADD-CONTENT".toByteArray()
    private val same = "SAME".toByteArray()
    private val resultBody = AtomicReference<String?>(null)

    @Test
    fun `push 成功编排下载备份覆盖并回执 success`() {
        seedServerRoot()
        blob.onDownload = { url, _, sink -> writeBlob(url, sink) }
        executor(backupRoot = File(dataDir, "delivery-backups")).execute(pushCommand())

        assertEquals("NEW-CONTENT", File(serverRoot, "plugins/upd.txt").readText())
        assertEquals("ADD-CONTENT", File(serverRoot, "plugins/new.txt").readText())
        assertFalse(File(serverRoot, "plugins/del.txt").exists(), "delete 项应被删")
        assertEquals("SAME", File(serverRoot, "plugins/skip.txt").readText(), "同 hash 项应跳过不动")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("phase=push"))
        assertTrue(body.contains("status=success"))
        assertTrue(body.contains("changedFileCount=3"), "upd+new+del 三项变更：$body")
        assertTrue(body.contains("skippedFileCount=1"), "skip 一项：$body")
        assertTrue(body.contains("backupPresent=true"))
        assertFalse(File(dataDir, "delivery-tmp/1").exists(), "成功后临时目录应清理")
        assertTrue(File(dataDir, "delivery-backups/1").exists(), "应生成备份")
    }

    @Test
    fun `备份失败绝不覆盖原文件并回执 failed`() {
        seedServerRoot()
        blob.onDownload = { url, _, sink -> writeBlob(url, sink) }
        // 备份根设为一个普通文件，令备份创建目录 / 复制必失败。
        val backupFile = File(dataDir, "broken-backup-root").apply { writeText("not a dir") }
        executor(backupRoot = backupFile).execute(pushCommand())

        assertEquals("OLD", File(serverRoot, "plugins/upd.txt").readText(), "备份失败绝不覆盖原文件")
        assertTrue(File(serverRoot, "plugins/del.txt").exists(), "备份失败不删 delete 项")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("status=failed"))
        assertTrue(body.contains("备份失败"), "失败原因应指明备份失败：$body")
    }

    /** 铺设模板目标现状：upd 将被覆盖、skip 同 hash 跳过、del 将删除、new 尚不存在。 */
    private fun seedServerRoot() {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/skip.txt", same)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", "X".toByteArray())
    }

    /** 按 url（=sha）向 sink 写模拟 blob 内容（首次下载 rangeStart 恒 0，续传由下载器单测覆盖）。 */
    private fun writeBlob(
        url: String,
        sink: java.io.OutputStream,
    ): BlobDownloadOutcome {
        val bytes =
            when (url) {
                DeliveryTestSupport.sha256(updNew) -> updNew
                DeliveryTestSupport.sha256(addContent) -> addContent
                else -> ByteArray(0)
            }
        sink.write(bytes)
        return BlobDownloadOutcome(200, bytes.size.toLong())
    }

    private fun executor(backupRoot: File): DeliveryCommandExecutor {
        val resolver = DeliveryTargetResolver(serverRoot, dataDir)
        val apiClient = BeaconApiClient(RoutingTransport(resultBody), ManifestCodec(manifestTree()), settings())
        val pipeline =
            DeliveryPipeline(
                uploader = DeliveryUploader(blob, resolver, { it }, { emptyMap() }, adapter),
                downloader = DeliveryDownloader(blob, { it }, { emptyMap() }, adapter),
                backupManager = DeliveryBackupManager(backupRoot, resolver, ManifestCodec(manifestTree()), adapter),
                overwriter = DeliveryOverwriter(resolver),
                tempRoot = File(dataDir, "delivery-tmp"),
            )
        return DeliveryCommandExecutor(identity(), apiClient, adapter, pipeline)
    }

    /** 目标差异清单树（parseDeliveryManifest 直接从此树读键）。 */
    private fun manifestTree(): Map<String, Any?> =
        mapOf(
            "orderId" to 1L,
            "activationMethod" to "restart",
            "files" to
                listOf(
                    fileNode("plugins/upd.txt", "update", DeliveryTestSupport.sha256(updNew), updNew.size),
                    fileNode("plugins/skip.txt", "update", DeliveryTestSupport.sha256(same), same.size),
                    fileNode("plugins/new.txt", "add", DeliveryTestSupport.sha256(addContent), addContent.size),
                    fileNode("plugins/del.txt", "delete", "", 0),
                ),
        )

    private fun fileNode(
        path: String,
        action: String,
        sha: String,
        size: Int,
    ): Map<String, Any?> = mapOf("path" to path, "action" to action, "sha256" to sha, "size" to size.toLong())

    private fun pushCommand(): AgentCommand =
        AgentCommand(
            id = 5L,
            type = AgentCommand.TYPE_DELIVERY_PUSH,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L),
        )

    private fun identity(): AgentIdentity =
        AgentIdentity(
            namespace = "prod",
            serverId = "lobby-1",
            role = "bukkit",
            groupHint = "area1",
            address = "10.0.0.7:25565",
            version = "1.0",
            capacity = 100,
            weight = 100,
            metadata = emptyMap(),
            identityId = "id-1",
            bootId = "boot-1",
        )

    private fun settings(): AgentSettings =
        AgentSettings(
            endpoints = listOf("http://127.0.0.1:8080"),
            bootstrapToken = "t",
            pollTimeoutMs = 30000,
            requestTimeoutMs = 5000,
            heartbeatFallbackMs = 10000,
            backoff = BackoffSettings(1000, 30000, 2.0, 0.2),
            snapshotEnabled = false,
            snapshotFileName = "snap.json",
            fileTree = FileTreeSettings(false, "", "ft.json"),
            override = OverrideSettings(emptySet(), "ob"),
        )

    /** 按 URL 路由：manifest → 200 清单体；result → 204 并记回执体。 */
    private class RoutingTransport(private val resultBody: AtomicReference<String?>) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse =
            when {
                request.url.endsWith("/manifest") -> HttpResponse(200, "manifest")
                request.url.endsWith("/result") -> {
                    resultBody.set(request.body)
                    HttpResponse(204, "")
                }

                else -> HttpResponse(404, "")
            }
    }

    /** decode 恒返清单树；encode 回传 toString 供断言回执体（result）与写备份 manifest（内容无关）。 */
    private class ManifestCodec(private val tree: Map<String, Any?>) : JsonCodec {
        override fun encode(value: Any?): String = value.toString()

        override fun decode(json: String): Any? = tree
    }
}

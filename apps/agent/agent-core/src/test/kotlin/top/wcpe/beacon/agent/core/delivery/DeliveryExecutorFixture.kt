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

/**
 * 交付命令执行器测试夹具：临时目录、假 blob 传输、回执记录与清单 / 命令构造，供用例类**继承**共享。
 *
 * 抽成基类而非堆在单个用例类里：交付编排用例数量多（推送 / 生效 / 回滚 / 失败路径），全堆一处会顶到
 * detekt `LargeClass` 阈值；用例类继承本夹具即可共享全部构造器，且自身不再膨胀。
 */
open class DeliveryExecutorFixture {
    protected val serverRoot: File = DeliveryTestSupport.tempDir("delivery-exec-root")
    protected val dataDir: File = DeliveryTestSupport.tempDir("delivery-exec-data")
    protected val adapter = ManualAsyncAdapter(dataDir)
    protected val blob = FakeBlobStreamTransport()

    protected val updNew = "NEW-CONTENT".toByteArray()
    protected val addContent = "ADD-CONTENT".toByteArray()
    protected val same = "SAME".toByteArray()

    /** 全部回执体（按到达顺序）：用于断言「同一条命令只回执一次」与并发拒收回执。 */
    protected val resultBodies = mutableListOf<String>()

    /** 造一份正推用例可用的备份管理器（不参与断言，仅满足管道装配）。 */
    protected fun seededBackupForeverUnused(): DeliveryBackupManager {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD".toByteArray())
        return DeliveryBackupManager(
            File(dataDir, "delivery-backups"),
            DeliveryTargetResolver(serverRoot, dataDir),
            BackupManifestCodec(),
            adapter,
        )
    }

    /** 造一份 update 项备份（旧内容 OLD），返回其 backupManager 供回滚测试复用（往返 codec）。 */
    protected fun seededBackup(): DeliveryBackupManager {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD".toByteArray())
        val backupManager =
            DeliveryBackupManager(
                File(dataDir, "delivery-backups"),
                DeliveryTargetResolver(serverRoot, dataDir),
                RollbackRoundTripCodec(),
                adapter,
            )
        backupManager.backup(1L, listOf(DeliveryFileOp("plugins/upd.txt", DeliveryFileOp.Kind.UPDATE, "", 0L)))
        return backupManager
    }

    /** 用给定 backupManager 构造执行器（回滚测试复用同一备份实例的往返 codec）。 */
    protected fun executorWith(
        backupManager: DeliveryBackupManager,
        manifest: Map<String, Any?> = manifestTree(),
    ): DeliveryCommandExecutor {
        val resolver = DeliveryTargetResolver(serverRoot, dataDir)
        val apiClient = BeaconApiClient(RoutingTransport(resultBodies), ManifestCodec(manifest), settings())
        val pipeline =
            DeliveryPipeline(
                uploader = DeliveryUploader(blob, resolver, { it }, { emptyMap() }, adapter, TEST_BACKOFF, sleep = {}),
                downloader = DeliveryDownloader(blob, { it }, { emptyMap() }, adapter, TEST_BACKOFF, sleep = {}),
                backupManager = backupManager,
                overwriter = DeliveryOverwriter(resolver),
                tempRoot = File(dataDir, "delivery-tmp"),
            )
        return DeliveryCommandExecutor(identity(), apiClient, adapter, pipeline)
    }

    /** 构造一条 delivery_rollback 命令（携指定生效方式，orderId=1）。 */
    protected fun rollbackCommand(activationMethod: String): AgentCommand =
        AgentCommand(
            id = 7L,
            type = AgentCommand.TYPE_DELIVERY_ROLLBACK,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L, activationMethod = activationMethod),
        )

    /** 铺设模板目标现状：upd 将被覆盖、skip 同 hash 跳过、del 将删除、new 尚不存在。 */
    protected fun seedServerRoot() {
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "OLD".toByteArray())
        DeliveryTestSupport.writeFile(serverRoot, "plugins/skip.txt", same)
        DeliveryTestSupport.writeFile(serverRoot, "plugins/del.txt", "X".toByteArray())
    }

    /** 按 url（=sha）向 sink 写模拟 blob 内容（首次下载 rangeStart 恒 0，续传由下载器单测覆盖）。 */
    protected fun writeBlob(
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

    protected fun executor(
        backupRoot: File,
        manifest: Map<String, Any?> = manifestTree(),
        manifestError: RuntimeException? = null,
        manifestStatus: Int = 200,
        manifestBody: String = "manifest",
    ): DeliveryCommandExecutor {
        val resolver = DeliveryTargetResolver(serverRoot, dataDir)
        val apiClient =
            BeaconApiClient(
                RoutingTransport(resultBodies, manifestError, manifestStatus, manifestBody),
                ManifestCodec(manifest),
                settings(),
            )
        val pipeline =
            DeliveryPipeline(
                uploader = DeliveryUploader(blob, resolver, { it }, { emptyMap() }, adapter, TEST_BACKOFF, sleep = {}),
                downloader = DeliveryDownloader(blob, { it }, { emptyMap() }, adapter, TEST_BACKOFF, sleep = {}),
                backupManager = DeliveryBackupManager(backupRoot, resolver, BackupManifestCodec(), adapter),
                overwriter = DeliveryOverwriter(resolver),
                tempRoot = File(dataDir, "delivery-tmp"),
            )
        return DeliveryCommandExecutor(identity(), apiClient, adapter, pipeline)
    }

    /** 目标差异清单树（parseDeliveryManifest 直接从此树读键）。 */
    protected fun manifestTree(
        files: List<Map<String, Any?>> =
            listOf(
                fileNode("plugins/upd.txt", "update", DeliveryTestSupport.sha256(updNew), updNew.size),
                fileNode("plugins/skip.txt", "update", DeliveryTestSupport.sha256(same), same.size),
                fileNode("plugins/new.txt", "add", DeliveryTestSupport.sha256(addContent), addContent.size),
                fileNode("plugins/del.txt", "delete", "", 0),
            ),
    ): Map<String, Any?> =
        mapOf(
            "orderId" to 1L,
            "activationMethod" to "restart",
            "files" to files,
        )

    protected fun configFileNode(
        path: String,
        sha: String,
    ): Map<String, Any?> = fileNode(path, "update", sha, 1, DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT)

    protected fun fileNode(
        path: String,
        action: String,
        sha: String,
        size: Int,
        sourceKind: String? = null,
    ): Map<String, Any?> =
        buildMap {
            put("path", path)
            put("action", action)
            put("sha256", sha)
            put("size", size.toLong())
            sourceKind?.let { put("sourceKind", it) }
        }

    protected fun pushCommand(): AgentCommand =
        AgentCommand(
            id = 5L,
            type = AgentCommand.TYPE_DELIVERY_PUSH,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L),
        )

    /** 构造一条 delivery_activate 命令（携指定生效方式，orderId=1）。 */
    protected fun activateCommand(activationMethod: String): AgentCommand =
        AgentCommand(
            id = 6L,
            type = AgentCommand.TYPE_DELIVERY_ACTIVATE,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L, activationMethod = activationMethod),
        )

    protected fun identity(): AgentIdentity =
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

    protected fun settings(): AgentSettings =
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

    private companion object {
        /** 测试用退避设置：抖动归零、间隔极小，配合注入的空 sleep 使重试用例确定性且不真等。 */
        private val TEST_BACKOFF = BackoffSettings(initialMs = 1, maxMs = 4, multiplier = 2.0, jitterRatio = 0.0)
    }
}

internal class RollbackRoundTripCodec : JsonCodec {
    private var last: Any? = null

    override fun encode(value: Any?): String {
        last = value
        return "rt"
    }

    override fun decode(json: String): Any? = last
}

internal class BackupManifestCodec : JsonCodec {
    private var last: Any? = null

    override fun encode(value: Any?): String {
        last = value
        return "[]"
    }

    override fun decode(json: String): Any? = last
}

/** 按 URL 路由：manifest → 可注入状态码 / 响应体；result → 204 并记回执体。 */
internal class RoutingTransport(
    private val resultBodies: MutableList<String>,
    private val manifestError: RuntimeException? = null,
    private val manifestStatus: Int = 200,
    private val manifestBody: String = "manifest",
) : HttpTransport {
    override fun execute(request: HttpRequest): HttpResponse =
        when {
            request.url.endsWith("/manifest") -> {
                manifestError?.let { throw it }
                HttpResponse(manifestStatus, manifestBody)
            }
            request.url.endsWith("/result") -> {
                resultBodies.add(request.body ?: "")
                HttpResponse(204, "")
            }

            else -> HttpResponse(404, "")
        }
}

/** decode 恒返清单树；encode 回传 toString 供断言回执体（result）与写备份 manifest（内容无关）。 */
internal class ManifestCodec(private val tree: Map<String, Any?>) : JsonCodec {
    override fun encode(value: Any?): String = value.toString()

    override fun decode(json: String): Any? = tree
}

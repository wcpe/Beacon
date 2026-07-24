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
import kotlin.test.assertNotEquals
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

    @Test
    fun `push 遇未知 sourceKind 时在落盘前回执 failed`() {
        seedServerRoot()
        blob.onDownload = { url, _, sink -> writeBlob(url, sink) }
        val tree =
            manifestTree(
                listOf(
                    fileNode(
                        "plugins/upd.txt",
                        "update",
                        DeliveryTestSupport.sha256(updNew),
                        updNew.size,
                        "future_config_kind",
                    ),
                ),
            )

        executor(File(dataDir, "delivery-backups"), tree).execute(pushCommand())

        assertEquals("OLD", File(serverRoot, "plugins/upd.txt").readText(), "未知来源类型不得触碰目标文件")
        assertTrue(resultBody.get()!!.contains("status=failed"), "未知来源类型必须在 push 阶段 fail-closed")
        assertFalse(File(dataDir, "delivery-backups/1").exists(), "校验失败前不应生成备份")
    }

    @Test
    fun `restart 生效先同步回执开始生效再优雅关服`() {
        val exec = executor(backupRoot = File(dataDir, "delivery-backups"))
        exec.execute(activateCommand("restart"))

        // 关服前：已同步回执 activate success「开始生效」（postResult 阻塞至送达）。
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("phase=activate"), "应回执 activate 阶段：$body")
        assertTrue(body.contains("status=success"), "restart 先回执 success「开始生效」：$body")
        // 时序关键：回执已发但关服尚未执行——关服被排入延迟队列、还没触发。
        assertEquals(0, adapter.shutdownReasons.size, "回执后、延迟任务执行前绝不应已关服")
        assertEquals(1, adapter.delayedCount(), "应恰好调度一个延迟优雅关服任务")

        // 推进延迟任务 → 真正触发优雅关服。
        adapter.drainOne()
        assertEquals(1, adapter.shutdownReasons.size, "延迟任务执行后应优雅关服一次")
        assertTrue(adapter.shutdownReasons.first().contains("#1"), "关服原因应含 orderId：${adapter.shutdownReasons.first()}")
    }

    @Test
    fun `restart 关服原语抛异常回执 failed`() {
        adapter.shutdownError = RuntimeException("调度器不可用")
        val exec = executor(backupRoot = File(dataDir, "delivery-backups"))
        exec.execute(activateCommand("restart"))

        // 先回执了 success「开始生效」，关服延迟任务入队。
        assertEquals(1, adapter.delayedCount())
        // 执行关服 → 原语抛异常 → 捕获后回执 activate failed（控制面据「关服指令回执失败」判 failed 熔断止血）。
        adapter.drainOne()
        assertEquals(1, adapter.shutdownReasons.size, "关服原语已被尝试")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("phase=activate"), "失败回执仍为 activate 阶段：$body")
        assertTrue(body.contains("status=failed"), "关服抛异常应回执 failed：$body")
        assertTrue(body.contains("优雅关服失败"), "失败原因应指明关服失败：$body")
    }

    @Test
    fun `manifest sourceKind 缺失兼容 file_diff 并解析 config_artifact`() {
        val tree =
            manifestTree(
                listOf(
                    fileNode("plugins/Demo/plugin.jar", "update", "a".repeat(64), 1),
                    fileNode(
                        "plugins/Demo/config.yml",
                        "update",
                        "b".repeat(64),
                        1,
                        DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT,
                    ),
                ),
            )

        val manifest =
            BeaconApiClient(RoutingTransport(resultBody), ManifestCodec(tree), settings())
                .fetchDeliveryManifest(identity(), 1L) ?: error("未拉到清单")

        assertEquals(DeliveryManifestFile.SOURCE_KIND_FILE_DIFF, manifest.files[0].sourceKind)
        assertEquals(DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT, manifest.files[1].sourceKind)
    }

    @Test
    fun `hot_reload 遇非字符串 sourceKind 时回执 failed`() {
        val invalid = fileNode("plugins/Demo/config.yml", "update", "a".repeat(64), 1).toMutableMap()
        invalid["sourceKind"] = 7

        executor(File(dataDir, "delivery-backups"), manifestTree(listOf(invalid)))
            .execute(activateCommand("hot_reload"))

        assertTrue(adapter.configChanges.isEmpty(), "非法来源类型不应触发配置回调")
        assertTrue(resultBody.get()!!.contains("status=failed"), "显式非字符串来源类型必须 fail-closed")
    }

    @Test
    fun `hot_reload 遇未知 sourceKind 时回执 failed 而非静默 no-op`() {
        val tree =
            manifestTree(
                listOf(
                    fileNode("plugins/Demo/config.yml", "update", "a".repeat(64), 1, "future_config_kind"),
                ),
            )

        executor(File(dataDir, "delivery-backups"), tree).execute(activateCommand("hot_reload"))

        assertTrue(adapter.configChanges.isEmpty(), "未知来源类型不应触发配置回调")
        assertTrue(resultBody.get()!!.contains("status=failed"), "未知来源类型必须 fail-closed")
        assertEquals(0, adapter.shutdownReasons.size, "未知来源类型绝不关服")
    }

    @Test
    fun `hot_reload 仅普通文件和 jar 时成功 no-op 且绝不关服`() {
        val tree =
            manifestTree(
                listOf(
                    fileNode("plugins/Demo/readme.txt", "update", "a".repeat(64), 1),
                    fileNode(
                        "plugins/Demo/plugin.jar",
                        "update",
                        "b".repeat(64),
                        1,
                        DeliveryManifestFile.SOURCE_KIND_FILE_DIFF,
                    ),
                ),
            )

        executor(File(dataDir, "delivery-backups"), tree).execute(activateCommand("hot_reload"))

        assertTrue(adapter.configChanges.isEmpty(), "普通文件与 jar 不应触发配置回调")
        assertEquals(0, adapter.shutdownReasons.size, "hot_reload 不应触发关服")
        assertEquals(0, adapter.delayedCount(), "hot_reload 不应调度关服任务")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("phase=activate"))
        assertTrue(body.contains("status=success"), "无配置工件应成功 no-op：$body")
    }

    @Test
    fun `hot_reload 配置路径稳定去重排序且摘要不受清单顺序影响`() {
        val first =
            listOf(
                configFileNode("plugins/Z/config.yml", "b".repeat(64)),
                configFileNode("plugins/A/config.yml", "a".repeat(64)),
                configFileNode("plugins/A/config.yml", "a".repeat(64)),
            )
        executor(File(dataDir, "delivery-backups"), manifestTree(first)).execute(activateCommand("hot_reload"))
        val firstChange = adapter.configChanges.single()

        adapter.configChanges.clear()
        executor(File(dataDir, "delivery-backups"), manifestTree(first.reversed())).execute(activateCommand("hot_reload"))
        val secondChange = adapter.configChanges.single()

        assertEquals(listOf("plugins/A/config.yml", "plugins/Z/config.yml"), firstChange.first.toList())
        assertEquals(firstChange, secondChange, "配置摘要与 changed 顺序应稳定")
        assertTrue(firstChange.second.matches(Regex("[0-9a-f]{32}")), "配置摘要应为小写 md5")
        assertTrue(resultBody.get()!!.contains("status=success"))
        assertEquals(0, adapter.shutdownReasons.size, "hot_reload 绝不关服")
    }

    @Test
    fun `hot_reload 重新拉取 manifest 异常时回执 failed 且不回调`() {
        executor(File(dataDir, "delivery-backups"), manifestError = RuntimeException("控制面不可用"))
            .execute(activateCommand("hot_reload"))

        assertTrue(adapter.configChanges.isEmpty())
        assertTrue(resultBody.get()!!.contains("status=failed"))
        assertEquals(0, adapter.shutdownReasons.size, "拉取失败绝不关服")
    }

    @Test
    fun `hot_reload 配置回调异常时回执 failed 且绝不关服`() {
        adapter.configChangeError = RuntimeException("事件总线不可用")
        val tree = manifestTree(listOf(configFileNode("plugins/Demo/config.yml", "a".repeat(64))))

        executor(File(dataDir, "delivery-backups"), tree).execute(activateCommand("hot_reload"))

        assertEquals(1, adapter.configChanges.size, "应尝试一次配置回调")
        assertTrue(resultBody.get()!!.contains("status=failed"))
        assertEquals(0, adapter.shutdownReasons.size, "回调失败绝不关服")
        assertEquals(0, adapter.delayedCount(), "回调失败不应调度关服")
    }

    @Test
    fun `restart 回滚还原备份后回执并优雅关服`() {
        val backupManager = seededBackup()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray()) // 模拟正推覆盖

        executorWith(backupManager).execute(rollbackCommand("restart"))

        assertEquals("OLD", File(serverRoot, "plugins/upd.txt").readText(), "回滚应从备份还原旧内容")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("phase=rollback"), "应回执 rollback 阶段：$body")
        assertTrue(body.contains("status=success"), "还原成功先回执 success：$body")
        assertEquals(0, adapter.shutdownReasons.size, "回执后、延迟任务前不应已关服")
        assertEquals(1, adapter.delayedCount(), "restart 回滚应调度一个延迟优雅关服")
        adapter.drainOne()
        assertEquals(1, adapter.shutdownReasons.size, "延迟任务执行后应优雅关服一次")
    }

    @Test
    fun `push_only 回滚还原不关服`() {
        val backupManager = seededBackup()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray())

        executorWith(backupManager).execute(rollbackCommand("push_only"))

        assertEquals("OLD", File(serverRoot, "plugins/upd.txt").readText())
        assertEquals(0, adapter.delayedCount(), "push_only 回滚不关服")
        assertTrue(resultBody.get()!!.contains("status=success"))
    }

    @Test
    fun `hot_reload 回滚还原成功后通知配置再回执 success`() {
        val backupManager = seededBackup()
        DeliveryTestSupport.writeFile(serverRoot, "plugins/upd.txt", "NEW".toByteArray())
        val tree = manifestTree(listOf(configFileNode("plugins/upd.txt", DeliveryTestSupport.sha256("NEW".toByteArray()))))
        val exec = executorWith(backupManager, tree)

        exec.execute(activateCommand("hot_reload"))
        val activatedMd5 = adapter.configChanges.single().second
        adapter.configChanges.clear()
        exec.execute(rollbackCommand("hot_reload"))

        assertEquals("OLD", File(serverRoot, "plugins/upd.txt").readText(), "应先完成备份还原")
        val rollbackChange = adapter.configChanges.single()
        assertEquals(listOf("plugins/upd.txt"), rollbackChange.first.toList())
        assertNotEquals(activatedMd5, rollbackChange.second, "回滚后的摘要应反映还原后的磁盘状态，避免被监听方当作重复通知")
        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("status=success"))
        assertTrue(body.contains("backupPresent=true"), "回滚成功应保留已有回执语义：$body")
        assertEquals(0, adapter.shutdownReasons.size, "hot_reload 回滚绝不关服")
        assertEquals(0, adapter.delayedCount(), "hot_reload 回滚不应调度关服")
    }

    @Test
    fun `hot_reload 回滚备份缺失回执 failed 且不通知`() {
        val backupManager =
            DeliveryBackupManager(
                File(dataDir, "delivery-backups-empty"),
                DeliveryTargetResolver(serverRoot, dataDir),
                RollbackRoundTripCodec(),
                adapter,
            )
        val tree = manifestTree(listOf(configFileNode("plugins/upd.txt", "a".repeat(64))))

        executorWith(backupManager, tree).execute(rollbackCommand("hot_reload"))

        val body = resultBody.get() ?: error("未回执")
        assertTrue(body.contains("status=failed"), "备份缺失应回执 failed：$body")
        assertTrue(adapter.configChanges.isEmpty(), "备份还原失败不应通知配置")
        assertEquals(0, adapter.shutdownReasons.size, "备份缺失不关服")
    }

    /** 造一份 update 项备份（旧内容 OLD），返回其 backupManager 供回滚测试复用（往返 codec）。 */
    private fun seededBackup(): DeliveryBackupManager {
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
    private fun executorWith(
        backupManager: DeliveryBackupManager,
        manifest: Map<String, Any?> = manifestTree(),
    ): DeliveryCommandExecutor {
        val resolver = DeliveryTargetResolver(serverRoot, dataDir)
        val apiClient = BeaconApiClient(RoutingTransport(resultBody), ManifestCodec(manifest), settings())
        val pipeline =
            DeliveryPipeline(
                uploader = DeliveryUploader(blob, resolver, { it }, { emptyMap() }, adapter),
                downloader = DeliveryDownloader(blob, { it }, { emptyMap() }, adapter),
                backupManager = backupManager,
                overwriter = DeliveryOverwriter(resolver),
                tempRoot = File(dataDir, "delivery-tmp"),
            )
        return DeliveryCommandExecutor(identity(), apiClient, adapter, pipeline)
    }

    /** 构造一条 delivery_rollback 命令（携指定生效方式，orderId=1）。 */
    private fun rollbackCommand(activationMethod: String): AgentCommand =
        AgentCommand(
            id = 7L,
            type = AgentCommand.TYPE_DELIVERY_ROLLBACK,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L, activationMethod = activationMethod),
        )

    /** 往返 codec：encode 记住入参、decode 返回它，供 backup→restore 往返（回滚测试用）。 */
    private class RollbackRoundTripCodec : JsonCodec {
        private var last: Any? = null

        override fun encode(value: Any?): String {
            last = value
            return "rt"
        }

        override fun decode(json: String): Any? = last
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

    private fun executor(
        backupRoot: File,
        manifest: Map<String, Any?> = manifestTree(),
        manifestError: RuntimeException? = null,
    ): DeliveryCommandExecutor {
        val resolver = DeliveryTargetResolver(serverRoot, dataDir)
        val apiClient = BeaconApiClient(RoutingTransport(resultBody, manifestError), ManifestCodec(manifest), settings())
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
    private fun manifestTree(
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

    private fun configFileNode(
        path: String,
        sha: String,
    ): Map<String, Any?> = fileNode(path, "update", sha, 1, DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT)

    private fun fileNode(
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

    private fun pushCommand(): AgentCommand =
        AgentCommand(
            id = 5L,
            type = AgentCommand.TYPE_DELIVERY_PUSH,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L),
        )

    /** 构造一条 delivery_activate 命令（携指定生效方式，orderId=1）。 */
    private fun activateCommand(activationMethod: String): AgentCommand =
        AgentCommand(
            id = 6L,
            type = AgentCommand.TYPE_DELIVERY_ACTIVATE,
            payload = IngestCommandPayload("", "", ""),
            deliveryPayload = DeliveryCommandPayload(orderId = 1L, activationMethod = activationMethod),
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
    private class RoutingTransport(
        private val resultBody: AtomicReference<String?>,
        private val manifestError: RuntimeException? = null,
    ) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse =
            when {
                request.url.endsWith("/manifest") -> {
                    manifestError?.let { throw it }
                    HttpResponse(200, "manifest")
                }
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

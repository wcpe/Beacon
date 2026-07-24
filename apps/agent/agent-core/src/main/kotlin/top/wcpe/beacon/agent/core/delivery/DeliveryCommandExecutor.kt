package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.security.MessageDigest
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 交付执行机器打包（上传 / 下载 / 备份 / 覆盖 + 临时根），收敛 [DeliveryCommandExecutor] 构造参数。
 *
 * @param uploader      模板源上传器
 * @param downloader    目标下载器
 * @param backupManager 覆盖前备份 + 保留清理
 * @param overwriter    本地重判 + 原子覆盖
 * @param tempRoot      临时下载根（`dataFolder()/delivery-tmp`）
 */
data class DeliveryPipeline(
    val uploader: DeliveryUploader,
    val downloader: DeliveryDownloader,
    val backupManager: DeliveryBackupManager,
    val overwriter: DeliveryOverwriter,
    val tempRoot: File,
)

/**
 * 交付命令执行器（FR-165，见 ADR-0069）：串联上传 / 推送数据面全流程，回执阶段结果。
 *
 * **命令拉取单一入口在 [top.wcpe.beacon.agent.core.command.ReverseFetchExecutor]**（既有命令通道单飞排空），
 * delivery 类型委派本执行器执行——避免另起拉取循环与既有命令队列争抢（双拉取竞争）。本类只执行已拉到的命令，
 * 不自拉命令。[running] 单飞门为纵深防御：正常路径下委派方已串行化，本门兜底杜绝并发覆盖同一目标。
 *
 * 生效（activate，FR-171/M4）：restart 回执后优雅关服；hot_reload 重拉 V2 清单并直接派发配置工件变更通知。
 * 回滚（rollback，M5）：备份还原成功后按生效方式重启、通知配置或仅回执，不把 V2 配置喂入 Legacy 配置链路。
 *
 * 全程 async、流式、不整读大文件入内存；备份失败绝不动原文件（时序由 [executePush] 保证）。
 *
 * @param identity  本 agent 身份（回执携带）
 * @param apiClient REST 客户端（拉清单 / 回执）
 * @param adapter   平台日志
 * @param pipeline  交付执行机器
 */
@Suppress("TooManyFunctions") // 交付编排类天然多阶段方法：上传 / 推送 / 生效 / 回滚各一编排入口 + 私有步骤方法
class DeliveryCommandExecutor(
    private val identity: AgentIdentity,
    private val apiClient: BeaconApiClient,
    private val adapter: PlatformAdapter,
    private val pipeline: DeliveryPipeline,
) {
    private val running = AtomicBoolean(false)

    /** 执行一条已拉到的交付命令（须在 async 线程调用）。单飞门兜底：并发进入则跳过（正常不应发生）。 */
    fun execute(command: AgentCommand) {
        if (!running.compareAndSet(false, true)) {
            adapter.warn("交付命令已有一条在执行，跳过（单飞门兜底）：id=${command.id}，type=${command.type}")
            return
        }
        try {
            dispatch(command)
        } finally {
            running.set(false)
        }
    }

    /** 按类型分派；orderId 缺失或非法则忽略（不回执，控制面按命令超时清理）。 */
    private fun dispatch(command: AgentCommand) {
        val orderId = command.deliveryPayload?.orderId ?: 0L
        if (orderId <= 0L) {
            adapter.warn("交付命令缺少有效 orderId，忽略：id=${command.id}，type=${command.type}")
            return
        }
        when (command.type) {
            AgentCommand.TYPE_DELIVERY_UPLOAD -> runUpload(orderId)
            AgentCommand.TYPE_DELIVERY_PUSH -> runPush(orderId)
            AgentCommand.TYPE_DELIVERY_ACTIVATE -> runActivate(orderId, command.deliveryPayload?.activationMethod ?: "")
            AgentCommand.TYPE_DELIVERY_ROLLBACK -> runRollback(orderId, command.deliveryPayload?.activationMethod ?: "")
            else -> adapter.warn("非交付命令类型错入交付执行器（忽略）：id=${command.id}，type=${command.type}")
        }
    }

    /** 上传流程（§4.5.2）：拉待传清单 → 逐文件 HEAD 去重 + 流式 PUT → 回执 upload。 */
    private fun runUpload(orderId: Long) {
        val manifest =
            apiClient.fetchDeliveryUploadManifest(identity, orderId)
                ?: return failResult(orderId, PHASE_UPLOAD, "拉取待上传清单失败（控制面不可达 / 命令态不符）")
        val result = pipeline.uploader.upload(manifest.items)
        if (!result.ok) {
            failResult(orderId, PHASE_UPLOAD, result.error)
            return
        }
        postResult(orderId, DeliveryStageReport(PHASE_UPLOAD, STATUS_SUCCESS, result.uploaded, result.deduped, false, ""))
        adapter.info("交付上传完成：orderId=$orderId，上传=${result.uploaded}，去重跳过=${result.deduped}")
    }

    /**
     * 推送流程（§4.5.3 / §4.7.1）：拉清单 → 本地重判 → 全量下临时目录并校验 → 备份 → 覆盖 / 删除 →
     * 清临时目录 → 回执 push。失败经 [DeliveryPushException] / [IOException] 归到一处回执 failed；
     * **备份失败绝不动原文件**（[executePush] 时序保证）。
     */
    private fun runPush(orderId: Long) {
        val manifest =
            apiClient.fetchDeliveryManifest(identity, orderId)
                ?: return failResult(orderId, PHASE_PUSH, "拉取差异清单失败（控制面不可达 / 命令态不符）")
        if (hasUnsupportedSourceKind(manifest.files)) {
            failResult(orderId, PHASE_PUSH, "差异清单包含当前 Agent 不支持的 sourceKind")
        } else {
            runPushManifest(orderId, manifest)
        }
    }

    /** 校验来源类型后的推送流程：本地重判 → 下载 → 备份 → 覆盖 → 回执。 */
    private fun runPushManifest(
        orderId: Long,
        manifest: DeliveryTargetManifest,
    ) {
        val plan =
            try {
                pipeline.overwriter.plan(manifest.files)
            } catch (e: IOException) {
                return failResult(orderId, PHASE_PUSH, "路径校验失败：${reasonOf(e)}")
            }
        try {
            postResult(orderId, executePush(orderId, plan))
            adapter.info("交付推送完成：orderId=$orderId")
        } catch (e: DeliveryPushException) {
            failResult(orderId, PHASE_PUSH, e.message ?: "推送失败")
        } catch (e: IOException) {
            // 覆盖阶段 IO 失败：备份已在盘、可整单回滚。
            failResult(orderId, PHASE_PUSH, "覆盖失败（备份已生成、可回滚）：${reasonOf(e)}")
        }
    }

    /**
     * 执行推送计划：下载 → 备份 → 覆盖 / 删除 → 清临时目录，返回成功回执。
     *
     * 下载失败 / 备份失败抛 [DeliveryPushException]（各携脱敏原因）；覆盖阶段 IO 失败以 [IOException] 上抛。
     */
    private fun executePush(
        orderId: Long,
        plan: List<DeliveryFileOp>,
    ): DeliveryStageReport {
        val skipped = plan.count { it.kind == DeliveryFileOp.Kind.SKIP }
        val downloadWork = plan.filter { it.kind == DeliveryFileOp.Kind.ADD || it.kind == DeliveryFileOp.Kind.UPDATE }
        val tempDir = File(pipeline.tempRoot, orderId.toString())
        val download = pipeline.downloader.downloadAll(downloadWork, tempDir)
        if (!download.ok) throw DeliveryPushException(download.error)
        val backupPresent = backup(orderId, plan)
        val changed = pipeline.overwriter.apply(plan, tempDir)
        cleanTemp(tempDir)
        return DeliveryStageReport(PHASE_PUSH, STATUS_SUCCESS, changed, skipped, backupPresent, "")
    }

    /** 覆盖前备份工作集并机会式修剪保留；备份 IO 失败转 [DeliveryPushException]（未触碰任何原文件）。 */
    private fun backup(
        orderId: Long,
        plan: List<DeliveryFileOp>,
    ): Boolean =
        try {
            val present = pipeline.backupManager.backup(orderId, plan.filter { it.kind != DeliveryFileOp.Kind.SKIP })
            if (present) pipeline.backupManager.enforceRetention()
            present
        } catch (e: IOException) {
            throw DeliveryPushException("备份失败，未改动原文件：${reasonOf(e)}")
        }

    /**
     * 生效编排（FR-171，M4，见 ADR-0070 / spec §4.6.1）：按 activation_method 分派。
     *
     * - **restart**（真生效）：**先同步回执 activate success「开始生效」**（语义 = 我要关服了、非生效完成）→ 极短延迟后
     *   切平台原语优雅关服 → 宿主自启拉起 → agent 随进程重启重新注册 / 心跳回归。
     *   关服前必须先把回执发出（关服后进程没了就发不出）：[postResult] 同步阻塞至控制面收到才返回，保证送达在关服前；
     *   activated 由控制面观测心跳回归判定、**非本回执成功**（决策 3，注册 / 健康真源 = Go 进程内存）；
     *   关服原语调用本身抛异常（极少见）→ 回执 activate failed，让控制面据「关服指令回执失败」判 failed 并熔断止血。
     * - **hot_reload**：重拉差异清单，仅提取 config_artifact，直接派发平台配置变更通知后回执 success；无配置工件成功 no-op。
     * - 其它（push_only 控制面侧立即 activated、不下发 activate；空 / 未知值）：诚实回执 failed，暴露非预期。
     */
    private fun runActivate(
        orderId: Long,
        activationMethod: String,
    ) {
        when (activationMethod) {
            ACTIVATION_RESTART -> {
                adapter.info("交付 restart 生效：回执「开始生效」后将优雅关服，等宿主自启拉起并心跳回归：orderId=$orderId")
                // 关服前先同步回执「开始生效」：postResult 阻塞至送达，返回即已抵达控制面（关服后进程消失便发不出）。
                postResult(orderId, DeliveryStageReport(PHASE_ACTIVATE, STATUS_SUCCESS, 0, 0, false, ""))
                // 极短延迟后关服：让本命令执行调用栈（单飞门释放 / 委派方）先解开，再触发关服原语（平台原语内部切主线程执行）。
                adapter.runAsyncDelayed(RESTART_SHUTDOWN_DELAY_MS) {
                    try {
                        adapter.gracefulShutdown("交付变更单 #$orderId restart 生效")
                    } catch (e: Exception) {
                        failResult(orderId, PHASE_ACTIVATE, "优雅关服失败：${reasonOf(e)}")
                    }
                }
            }
            ACTIVATION_HOT_RELOAD -> runHotReload(orderId, PHASE_ACTIVATE, 0, false)
            else -> runUnsupportedSkeleton(orderId, PHASE_ACTIVATE, "未知生效方式「$activationMethod」")
        }
    }

    /**
     * 整单回滚（FR-167，spec §4.7.2）：从覆盖前备份还原磁盘（update / delete 复原、add 删除），回执 rollback；
     * 生效方式复用正推语义——restart 还原后优雅关服（宿主自启拉起、控制面观测心跳回归判 rolled_back），
     * push_only 只还原不关服（随下次自然重启读盘）；hot_reload 还原后重拉清单并通知配置工件路径。
     * 备份缺失 / 还原 IO 失败 → 回执 failed 且不通知；清单拉取 / 通知失败同样回执 failed。
     */
    private fun runRollback(
        orderId: Long,
        activationMethod: String,
    ) {
        val restored =
            try {
                pipeline.backupManager.restore(orderId)
            } catch (e: IOException) {
                return failResult(orderId, PHASE_ROLLBACK, "备份还原失败：${reasonOf(e)}")
            }
        when (activationMethod) {
            ACTIVATION_HOT_RELOAD -> runHotReload(orderId, PHASE_ROLLBACK, restored, true)
            ACTIVATION_RESTART -> restartAfterRollback(orderId, restored)
            else -> finishPushOnlyRollback(orderId, restored)
        }
    }

    /** restart 回滚：先回执，再延迟触发优雅关服。 */
    private fun restartAfterRollback(
        orderId: Long,
        restored: Int,
    ) {
        adapter.info("交付 restart 回滚：还原备份后回执并优雅关服，等宿主自启拉起并心跳回归：orderId=$orderId，还原=$restored")
        postResult(orderId, DeliveryStageReport(PHASE_ROLLBACK, STATUS_SUCCESS, restored, 0, true, ""))
        adapter.runAsyncDelayed(RESTART_SHUTDOWN_DELAY_MS) {
            try {
                adapter.gracefulShutdown("交付变更单 #$orderId 回滚后重启生效")
            } catch (e: Exception) {
                failResult(orderId, PHASE_ROLLBACK, "回滚后优雅关服失败：${reasonOf(e)}")
            }
        }
    }

    /** push_only（及其它非 restart）回滚：还原即够，随目标下次自然重启读盘。 */
    private fun finishPushOnlyRollback(
        orderId: Long,
        restored: Int,
    ) {
        postResult(orderId, DeliveryStageReport(PHASE_ROLLBACK, STATUS_SUCCESS, restored, 0, true, ""))
        adapter.info("交付回滚完成（还原即生效，随下次自然重启读盘）：orderId=$orderId，还原=$restored")
    }

    /** hot_reload：重拉 V2 清单并直接通知配置工件路径，不经过 Legacy ConfigApplier / EffectiveConfigStore。 */
    private fun runHotReload(
        orderId: Long,
        phase: String,
        changedFileCount: Int,
        backupPresent: Boolean,
    ) {
        val manifest = fetchHotReloadManifest(orderId, phase) ?: return
        if (hasUnsupportedSourceKind(manifest.files)) {
            failResult(orderId, phase, "差异清单包含当前 Agent 不支持的 sourceKind")
            return
        }
        val configFiles = normalizedConfigFiles(manifest.files)
        if (publishConfigChanged(orderId, phase, configFiles)) {
            postResult(orderId, DeliveryStageReport(phase, STATUS_SUCCESS, changedFileCount, 0, backupPresent, ""))
            adapter.info("交付 hot_reload 完成：orderId=$orderId，phase=$phase，配置工件=${configFiles.size}")
        }
    }

    /** 拉取 hot_reload 清单；连接、命令态或解析失败均在此统一回执 failed。 */
    private fun fetchHotReloadManifest(
        orderId: Long,
        phase: String,
    ): DeliveryTargetManifest? =
        try {
            apiClient.fetchDeliveryManifest(identity, orderId).also {
                if (it == null) failResult(orderId, phase, "重新拉取差异清单失败（控制面不可达 / 命令态不符）")
            }
        } catch (e: Exception) {
            failResult(orderId, phase, "重新拉取差异清单异常：${reasonOf(e)}")
            null
        }

    /** 是否包含当前 Agent 不认识的来源类型；未知类型必须 fail-closed，不能静默 no-op。 */
    private fun hasUnsupportedSourceKind(files: List<DeliveryManifestFile>): Boolean =
        files.any {
            it.sourceKind != DeliveryManifestFile.SOURCE_KIND_FILE_DIFF &&
                it.sourceKind != DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT
        }

    /** 配置工件按 path + sha256 稳定排序，并按 path 去重。 */
    private fun normalizedConfigFiles(files: List<DeliveryManifestFile>): List<DeliveryManifestFile> =
        files
            .filter { it.sourceKind == DeliveryManifestFile.SOURCE_KIND_CONFIG_ARTIFACT }
            .sortedWith(compareBy<DeliveryManifestFile> { it.path }.thenBy { it.sha256 })
            .distinctBy { it.path }

    /** 有配置工件时派发一次通知；无配置工件成功 no-op。 */
    private fun publishConfigChanged(
        orderId: Long,
        phase: String,
        configFiles: List<DeliveryManifestFile>,
    ): Boolean {
        if (configFiles.isEmpty()) return true
        val changed = configFiles.mapTo(linkedSetOf()) { it.path }
        return try {
            adapter.publishConfigChanged(changed, configArtifactMd5(configFiles))
            true
        } catch (e: Exception) {
            failResult(orderId, phase, "配置变更通知失败：${reasonOf(e)}")
            false
        }
    }

    /** 按通知时磁盘实际状态计算小写 md5；回滚后摘要会随还原内容变化。 */
    private fun configArtifactMd5(configFiles: List<DeliveryManifestFile>): String {
        val canonical =
            buildString {
                for (file in configFiles) {
                    val currentSha256 = pipeline.overwriter.currentSha256(file.path) ?: MISSING_FILE_MARKER
                    append(file.path).append('\n').append(currentSha256).append('\n')
                }
            }
        return MessageDigest
            .getInstance("MD5")
            .digest(canonical.toByteArray(StandardCharsets.UTF_8))
            .joinToString("") { "%02x".format(it) }
    }

    /** 未知生效方式接缝骨架：诚实回执 failed，不误报 done。 */
    private fun runUnsupportedSkeleton(
        orderId: Long,
        phase: String,
        milestone: String,
    ) {
        adapter.warn("收到交付 $phase 命令但$milestone 尚未实现（接缝，回执暂未支持）：orderId=$orderId")
        failResult(orderId, phase, "交付 $phase 暂未支持，待$milestone 实现")
    }

    /** 回执一条阶段结果；失败仅记 warn（best-effort，控制面按命令超时清理兜底）。 */
    private fun postResult(
        orderId: Long,
        report: DeliveryStageReport,
    ) {
        val ok = apiClient.postDeliveryResult(identity, orderId, report)
        if (!ok) adapter.warn("交付阶段回执失败（命令态不符 / 连接失败）：orderId=$orderId，phase=${report.phase}")
    }

    /** 回执一条失败结果（计数归零、backupPresent=false），并记 error 级日志。 */
    private fun failResult(
        orderId: Long,
        phase: String,
        error: String,
    ) {
        adapter.error("交付阶段失败：orderId=$orderId，phase=$phase，原因=$error", null)
        postResult(orderId, DeliveryStageReport(phase, STATUS_FAILED, 0, 0, false, error))
    }

    companion object {
        /** 阶段回执 phase 取值（与控制面 spec §5.2 对齐）。 */
        const val PHASE_UPLOAD = "upload"
        const val PHASE_PUSH = "push"
        const val PHASE_ACTIVATE = "activate"
        const val PHASE_ROLLBACK = "rollback"

        /** 阶段回执 status 取值。 */
        const val STATUS_SUCCESS = "success"
        const val STATUS_FAILED = "failed"

        /** 生效方式取值（与控制面 activation_method 下发值对齐，spec §4.6.1）。 */
        const val ACTIVATION_RESTART = "restart"
        const val ACTIVATION_HOT_RELOAD = "hot_reload"

        /** 配置工件被回滚为不存在时参与通知摘要的稳定标记。 */
        private const val MISSING_FILE_MARKER = "<missing>"

        /**
         * restart 回执「开始生效」后到触发优雅关服的极短延迟（毫秒）。
         *
         * 仅用于让本命令执行调用栈（单飞门释放 / 委派方）先行解开再关服，非契约、无需外置配置。
         */
        private const val RESTART_SHUTDOWN_DELAY_MS = 200L
    }
}

/** 推送阶段失败（携脱敏原因）：由 executePush 内各失败点抛出、runPush 统一回执 failed。 */
private class DeliveryPushException(
    message: String,
) : Exception(message)

/** 异常摘要（类名 + 消息，无凭据；上层再经控制面脱敏兜底）。 */
private fun reasonOf(e: Exception): String = "${e.javaClass.simpleName}: ${e.message ?: "无错误信息"}"

/** 清理本单临时下载目录（推送成功后）。 */
private fun cleanTemp(tempDir: File) {
    if (tempDir.exists()) tempDir.deleteRecursively()
}

package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.io.IOException
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
 * 生效（activate，M4）/ 回滚（rollback，M5）本期仅建回执骨架：诚实回执 failed「暂未支持」，不误报 done，
 * 待后续里程碑实现（接缝见 [runUnsupportedSkeleton] 调用点）。
 *
 * 全程 async、流式、不整读大文件入内存；备份失败绝不动原文件（时序由 [executePush] 保证）。
 *
 * @param identity  本 agent 身份（回执携带）
 * @param apiClient REST 客户端（拉清单 / 回执）
 * @param adapter   平台日志
 * @param pipeline  交付执行机器
 */
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
            AgentCommand.TYPE_DELIVERY_ACTIVATE -> runUnsupportedSkeleton(orderId, PHASE_ACTIVATE, "生效编排（M4）")
            AgentCommand.TYPE_DELIVERY_ROLLBACK -> runUnsupportedSkeleton(orderId, PHASE_ROLLBACK, "整单回滚（M5）")
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

    /** 生效 / 回滚接缝骨架（M4 / M5）：诚实回执 failed，不误报 done。 */
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

package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.api.ConfigReloadReport
import top.wcpe.beacon.agent.core.api.PluginReloadOutcome

/*
 * 交付数据面清单与操作模型（FR-165，见 ADR-0069 / spec §5.2）。
 *
 * 全部为 core 侧纯数据类，不含 @Serializable（守 ADR-0005）；由 top.wcpe.beacon.agent.core.client.BeaconApiClient
 * 从泛型 JSON 树映射填充，供交付执行器消费。
 */

/**
 * 模板源待上传 blob 清单（GET .../upload-manifest，spec §5.2）。
 *
 * @param orderId 变更单 id
 * @param items   待上传项（已就绪 blob 不在列；同 sha 多路径逐项列出，agent 侧 HEAD 天然去重）
 */
data class DeliveryUploadManifest(
    val orderId: Long,
    val items: List<DeliveryUploadItem>,
)

/**
 * 一条待上传项。
 *
 * @param path      服务器根内相对路径（模板源侧读盘用）
 * @param sha256    内容哈希（小写 hex，寻址与去重主键）
 * @param sizeBytes 字节数（Content-Length）
 */
data class DeliveryUploadItem(
    val path: String,
    val sha256: String,
    val sizeBytes: Long,
)

/**
 * 目标本服差异清单（GET .../manifest，spec §5.2）。
 *
 * 普通文件差异与 V2 配置冻结工件统一承载于 files，并由 sourceKind 区分；历史 configs 字段不建模。
 *
 * @param orderId          变更单 id
 * @param activationMethod 生效方式（restart / hot_reload / push_only）
 * @param files            普通文件差异与配置冻结工件全集（推送阶段统一按本地状态重判）
 */
data class DeliveryTargetManifest(
    val orderId: Long,
    val activationMethod: String,
    val files: List<DeliveryManifestFile>,
)

/**
 * 一条文件差异项。
 *
 * @param path       服务器根内相对路径
 * @param action     add / update / delete（相对目标语义由 agent 按本地清单重判，spec §4.2.3）
 * @param sha256     模板源侧内容哈希（delete 项为空）
 * @param sizeBytes  字节数（delete 项为 0）
 * @param sourceKind 来源类型：file_diff / config_artifact；旧清单缺字段时兼容为 file_diff
 */
data class DeliveryManifestFile(
    val path: String,
    val action: String,
    val sha256: String,
    val sizeBytes: Long,
    val sourceKind: String = SOURCE_KIND_FILE_DIFF,
) {
    companion object {
        /** 新增：模板源有、目标无。 */
        const val ACTION_ADD = "add"

        /** 覆盖：两侧都有、内容不同。 */
        const val ACTION_UPDATE = "update"

        /** 删除：模板源已删、目标仍存在。 */
        const val ACTION_DELETE = "delete"

        /** 普通文件差异；旧清单缺 sourceKind 时的兼容默认值。 */
        const val SOURCE_KIND_FILE_DIFF = "file_diff"

        /** V2 配置工件；仅用于交付 hot_reload 配置变更通知。 */
        const val SOURCE_KIND_CONFIG_ARTIFACT = "config_artifact"
    }
}

/**
 * 执行期按目标本地清单重判后的单文件操作（spec §4.2.3）。
 *
 * 由 [DeliveryOverwriter.plan] 逐文件本地 sha256 比对得出：相同则 [Kind.SKIP]；缺失则 [Kind.ADD]；
 * 不同则 [Kind.UPDATE]；delete 项目标存在则 [Kind.DELETE]、不存在则 [Kind.SKIP]。
 *
 * @param path      服务器根内相对路径
 * @param kind      重判后的操作
 * @param sha256    目标内容哈希（下载 / 校验用；delete / skip 项无意义）
 * @param sizeBytes 目标字节数（Content-Length 参考；delete / skip 项为 0）
 */
data class DeliveryFileOp(
    val path: String,
    val kind: Kind,
    val sha256: String,
    val sizeBytes: Long,
) {
    /** 单文件操作分类。 */
    enum class Kind {
        /** 本地同 hash（或 delete 项目标已不存在），跳过。 */
        SKIP,

        /** 本地缺失，需下载并落盘（回滚时删除）。 */
        ADD,

        /** 本地存在但内容不同，需下载覆盖（回滚时还原旧内容）。 */
        UPDATE,

        /** 目标存在的 delete 项，需备份后删除（回滚时还原旧内容）。 */
        DELETE,
    }
}

/**
 * 交付阶段回执报文（POST .../result，spec §5.2）。收敛回执字段为一个值对象，避免回执方法长参数列表。
 *
 * @param phase            阶段：upload / push / activate / rollback
 * @param status           结果：success / failed
 * @param changedFileCount 实际变更文件数（push 回执）
 * @param skippedFileCount 本地同 hash 跳过文件数（push 回执）
 * @param backupPresent    是否已生成本地备份（push 回执，回滚预检依据）
 * @param error            失败原因（脱敏；成功为空串）
 * @param reload           逐插件重载结果（FR-274）；仅 activate / rollback 阶段且实际派发过配置变更时非空，
 *                         其余阶段为 null。老控制面会忽略该字段，故为加性扩展。
 */
data class DeliveryStageReport(
    val phase: String,
    val status: String,
    val changedFileCount: Int,
    val skippedFileCount: Int,
    val backupPresent: Boolean,
    val error: String,
    val reload: DeliveryReloadReport? = null,
)

/**
 * 逐插件重载结果的回执形状（FR-274）。
 *
 * 只带控制面需要落库与展示的最小信息：谁、成功与否、为什么失败、实际加载了什么。
 * [plugins] 的顺序按监听器类名排序，保证同一批变更的重推产生可比对的稳定回执。
 */
data class DeliveryReloadReport(
    val total: Int,
    val okCount: Int,
    val failedCount: Int,
    val timeoutMs: Long,
    val plugins: List<DeliveryReloadPluginOutcome>,
) {
    /**
     * 回执 JSON 形状（FR-274 §3.4）。字段名与 `docs/specs/delivery-reload-ack.md` 保持一致，
     * 控制面按 camelCase 解码；未知字段由 Go 侧忽略，故该段可安全加性演进。
     */
    fun toWireMap(): Map<String, Any?> =
        linkedMapOf(
            "total" to total,
            "okCount" to okCount,
            "failedCount" to failedCount,
            "timeoutMs" to timeoutMs,
            "plugins" to plugins.map { it.toWireMap() },
        )

    companion object {
        /** 由聚合报告构造；无监听器时也产出（total=0），以便控制面区分「无插件」与「旧版 agent 未上报」。 */
        fun from(
            report: ConfigReloadReport,
            timeoutMs: Long,
        ): DeliveryReloadReport {
            val outcomes = report.outcomes.sortedBy { it.listener }
            return DeliveryReloadReport(
                total = outcomes.size,
                okCount = outcomes.count { it.ok },
                failedCount = outcomes.count { !it.ok },
                timeoutMs = timeoutMs,
                plugins = outcomes.map { DeliveryReloadPluginOutcome.from(it) },
            )
        }
    }
}

/** 单个插件的重载结果回执项（FR-274）。字段全为基本类型，控制面可直接落库与展示。 */
data class DeliveryReloadPluginOutcome(
    val listener: String,
    val status: String,
    val reason: String,
    val timeout: Boolean,
    val appliedMd5: String,
    val appliedVersion: String,
    val appliedCount: Int?,
    val detailJson: String,
) {
    /** 回执 JSON 形状（FR-274 §3.4）；[appliedCount] 未知时不写该键，避免与「加载了 0 条」混淆。 */
    fun toWireMap(): Map<String, Any?> =
        linkedMapOf<String, Any?>(
            "listener" to listener,
            "status" to status,
            "reason" to reason,
            "timeout" to timeout,
            "appliedMd5" to appliedMd5,
            "appliedVersion" to appliedVersion,
        ).apply {
            appliedCount?.let { put("appliedCount", it) }
            if (detailJson.isNotEmpty()) put("detailJson", detailJson)
        }

    companion object {
        const val STATUS_OK = "ok"
        const val STATUS_FAILED = "failed"

        fun from(outcome: PluginReloadOutcome): DeliveryReloadPluginOutcome =
            DeliveryReloadPluginOutcome(
                listener = outcome.listener,
                status = if (outcome.ok) STATUS_OK else STATUS_FAILED,
                reason = outcome.reason,
                timeout = outcome.timeout,
                appliedMd5 = outcome.appliedMd5,
                appliedVersion = outcome.appliedVersion,
                appliedCount = outcome.appliedCount,
                detailJson = outcome.detailJson,
            )
    }
}

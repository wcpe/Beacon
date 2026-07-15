package top.wcpe.beacon.agent.core.delivery

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
 * 配置项摘要（configs）归 M4 生效编排消费，M2 数据面只落文件项，故此处不建模 configs。
 *
 * @param orderId          变更单 id
 * @param activationMethod 生效方式（restart / hot_reload / push_only；M2 推送阶段不消费，仅可预知）
 * @param files            文件差异项全集（agent 按本地清单重判相对目标语义并对同 hash 文件跳过）
 */
data class DeliveryTargetManifest(
    val orderId: Long,
    val activationMethod: String,
    val files: List<DeliveryManifestFile>,
)

/**
 * 一条文件差异项。
 *
 * @param path      服务器根内相对路径
 * @param action    add / update / delete（相对目标语义由 agent 按本地清单重判，spec §4.2.3）
 * @param sha256    模板源侧内容哈希（delete 项为空）
 * @param sizeBytes 字节数（delete 项为 0）
 */
data class DeliveryManifestFile(
    val path: String,
    val action: String,
    val sha256: String,
    val sizeBytes: Long,
) {
    companion object {
        /** 新增：模板源有、目标无。 */
        const val ACTION_ADD = "add"

        /** 覆盖：两侧都有、内容不同。 */
        const val ACTION_UPDATE = "update"

        /** 删除：模板源已删、目标仍存在。 */
        const val ACTION_DELETE = "delete"
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
 */
data class DeliveryStageReport(
    val phase: String,
    val status: String,
    val changedFileCount: Int,
    val skippedFileCount: Int,
    val backupPresent: Boolean,
    val error: String,
)

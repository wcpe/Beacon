package top.wcpe.beacon.agent.core.command

/**
 * 控制面下发给本 agent 的一条待办命令（FR-39，见 ADR-0027）。
 *
 * 对应 `GET /beacon/v1/agent/commands` 的 200 响应 JSON：
 * `{"id":<n>,"type":"ingest-plugins","payload":{"scope":"group|server","group":"<g>","target":"<t>"}}`。
 * 现支持三类类型：ingest-plugins（读真实 plugins 目录文本配置回传，FR-39）、
 * tail-logs（读 agent 自身日志环形缓冲快照回传，FR-88）、
 * resync-config（重拉控制面权威的有效配置/文件树/覆盖集并 apply，FR-91）。
 *
 * 交付编排（FR-165，见 ADR-0069）新增四类命令（delivery_upload / delivery_push / delivery_activate /
 * delivery_rollback）：其 payload 只含 orderId（控制信息，绝不含文件内容），解析到 [deliveryPayload]，
 * 与 [IngestCommandPayload] 分离、不互相膨胀。
 *
 * @param id      命令 id（回传结果时带回引用）
 * @param type    命令类型（ingest-plugins / tail-logs / resync-config / delivery_*）
 * @param payload 载荷（scope / group / target，见 [IngestCommandPayload]）
 * @param deliveryPayload 交付命令专用载荷（仅 delivery_* 类型非空，见 [DeliveryCommandPayload]）
 */
data class AgentCommand(
    val id: Long,
    val type: String,
    val payload: IngestCommandPayload,
    val deliveryPayload: DeliveryCommandPayload? = null,
) {
    companion object {
        /** 反向抓取命令类型：抓取 plugins 文本配置回传 ingest（FR-39，见 ADR-0027）。 */
        const val TYPE_INGEST_PLUGINS = "ingest-plugins"

        /** 取日志命令类型：读 agent 自身日志环形缓冲快照回传（FR-88，见 ADR-0040）。 */
        const val TYPE_TAIL_LOGS = "tail-logs"

        /** 强制重同步命令类型：重拉控制面权威的有效配置/文件树/覆盖集并 apply（FR-91，复用命令队列、无业务载荷）。 */
        const val TYPE_RESYNC_CONFIG = "resync-config"

        /** 只读文件浏览命令类型：列目录 / 读子树 / 读单文件回传（FR-110，见 ADR-0049；纯只读、不写盘）。 */
        const val TYPE_FS_BROWSE = "fs-browse"

        /** 文件资产重扫命令类型：调扫描协调器立即扫描并全量上报清单（FR-163，见 ADR asset-manifest-sync-protocol；payload.force 忽略 mtime 缓存全部重哈希）。 */
        const val TYPE_ASSET_RESCAN = "asset-rescan"

        /** 文件资产内容读取命令类型：读单文本文件回传供预览 / diff（FR-164，见 v2-file-assets.md §4.5；纯只读、不写盘）。 */
        const val TYPE_ASSET_READ = "asset-read"

        /** 交付上传命令类型：模板源流式上传缺失 blob 到控制面中转存储（FR-165，见 ADR-0069 §4.5.2；snake_case 与控制面 enums 对齐）。 */
        const val TYPE_DELIVERY_UPLOAD = "delivery_upload"

        /** 交付推送命令类型：目标拉清单、覆盖前备份、流式下载 blob 后落盘覆盖（FR-165，见 §4.5.3）。 */
        const val TYPE_DELIVERY_PUSH = "delivery_push"

        /** 交付生效命令类型：目标按 activation_method 生效（FR-171，见 §4.6.1；M4 实现，M2 仅回执骨架）。 */
        const val TYPE_DELIVERY_ACTIVATE = "delivery_activate"

        /** 交付回滚命令类型：目标按备份 manifest 还原被覆盖 / 删除文件（FR-167，见 §4.7.2；M5 实现，M2 仅回执骨架）。 */
        const val TYPE_DELIVERY_ROLLBACK = "delivery_rollback"

        /** 判断某命令类型是否为交付命令（据此在解析时构建 [DeliveryCommandPayload]）。 */
        fun isDeliveryType(type: String): Boolean =
            type == TYPE_DELIVERY_UPLOAD ||
                type == TYPE_DELIVERY_PUSH ||
                type == TYPE_DELIVERY_ACTIVATE ||
                type == TYPE_DELIVERY_ROLLBACK
    }
}

/**
 * 交付命令专用载荷（FR-165，见 ADR-0069 §4.5.1）：payload 只含控制信息，**绝不含文件内容**。
 *
 * orderId 对全部 delivery_* 命令有效（完整清单经 agent 面 GET 拉取）；activationMethod 仅 delivery_activate
 * 命令携带（restart / hot_reload，由控制面按单级配置下发，spec §4.6.1），据此在生效阶段分派——其它 delivery
 * 命令缺省空串。刻意不塞进 [IngestCommandPayload] 以免两个载荷互相膨胀。
 *
 * @param orderId          变更单 id（拉取清单 / 回执时引用）
 * @param activationMethod 生效方式（仅 delivery_activate 携带：restart / hot_reload；其它 delivery 命令为空串）
 */
data class DeliveryCommandPayload(
    val orderId: Long,
    val activationMethod: String = "",
)

/**
 * ingest-plugins 命令载荷：ingest 落到哪个覆盖层 + 两段式抓取模式（FR-58，见 ADR-0037）。
 *
 * agent 不消费 scope/group/target 做落盘决策（落盘层由控制面 ingest 决定），仅原样回传命令 id；
 * 这里解析出来仅供日志可读，不参与抓取逻辑。
 *
 * 两段式（FR-58）：[mode] 区分 scan（只列元信息清单、永不失败）与 submit（只读选定 path 内容回传）。
 * 旧 agent / 旧控制面无 mode 字段 → mode 为空串，executor 维持旧整树读内容回传行为（向后兼容）。
 *
 * @param scope         覆盖层（group / server）
 * @param group         目标大区
 * @param target        server 层目标 serverId（group 层为空）
 * @param mode          抓取模式（[MODE_SCAN] / [MODE_SUBMIT]；空串=旧整树行为，兼容 land/imprint 等既有 mode 维度）
 * @param selectedPaths submit 模式下选定回传的相对 path 子集（scan 模式 / 旧行为为空）
 * @param op            文件浏览操作（FR-110，仅 fs-browse 命令用：[OP_LIST] / [OP_TREE] / [OP_FILE]；其它命令为空）
 * @param path          文件浏览目标相对 path（FR-110，相对 plugins 根；list/tree 可空=列根）
 * @param offset        文件浏览列目录分页偏移（FR-110，仅 op=list 用）
 * @param limit         文件浏览列目录分页条数（FR-110，仅 op=list 用；0=由 core 收口到默认/上限）
 * @param maxDepth      文件浏览读子树展开深度（FR-110，仅 op=tree 用）
 * @param force         文件资产重扫是否忽略本地 mtime 缓存全部重哈希（FR-163，仅 asset-rescan 命令用；其它命令缺省 false）
 * @param maxBytes      文件资产读取单文件上限字节（FR-164，仅 asset-read 命令用；0=由 core 收口到默认上限）
 */
data class IngestCommandPayload(
    val scope: String,
    val group: String,
    val target: String,
    val mode: String = "",
    val selectedPaths: List<String> = emptyList(),
    val op: String = "",
    val path: String = "",
    val offset: Int = 0,
    val limit: Int = 0,
    val maxDepth: Int = 0,
    val force: Boolean = false,
    val maxBytes: Int = 0,
) {
    companion object {
        /** 扫描模式：只列元信息清单（path/size/isText/overThreshold），不读内容、永不失败。 */
        const val MODE_SCAN = "scan"

        /** 提交模式：只读选定 path 子集的内容回传 ingest。 */
        const val MODE_SUBMIT = "submit"

        /** 文件浏览·列目录（FR-110）：懒列某目录直接子项（分页）。 */
        const val OP_LIST = "list"

        /** 文件浏览·读子树（FR-110）：按需展开某子树（逐层有界）。 */
        const val OP_TREE = "tree"

        /** 文件浏览·读单文件（FR-110）：读单文本文件内容（受单文件上限）。 */
        const val OP_FILE = "file"
    }
}

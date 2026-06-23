package top.wcpe.beacon.agent.core.command

/**
 * 控制面下发给本 agent 的一条待办命令（FR-39，见 ADR-0027）。
 *
 * 对应 `GET /beacon/v1/agent/commands` 的 200 响应 JSON：
 * `{"id":<n>,"type":"ingest-plugins","payload":{"scope":"group|server","group":"<g>","target":"<t>"}}`。
 * 本期唯一类型 ingest-plugins：令 agent 读真实 plugins 目录文本配置回传。
 *
 * @param id      命令 id（回传 ingest 时带回引用）
 * @param type    命令类型（本期仅 ingest-plugins）
 * @param payload 载荷（scope / group / target，见 [IngestCommandPayload]）
 */
data class AgentCommand(
    val id: Long,
    val type: String,
    val payload: IngestCommandPayload,
) {
    companion object {
        /** 本期唯一命令类型：抓取 plugins 文本配置回传 ingest。 */
        const val TYPE_INGEST_PLUGINS = "ingest-plugins"
    }
}

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
 */
data class IngestCommandPayload(
    val scope: String,
    val group: String,
    val target: String,
    val mode: String = "",
    val selectedPaths: List<String> = emptyList(),
) {
    companion object {
        /** 扫描模式：只列元信息清单（path/size/isText/overThreshold），不读内容、永不失败。 */
        const val MODE_SCAN = "scan"

        /** 提交模式：只读选定 path 子集的内容回传 ingest。 */
        const val MODE_SUBMIT = "submit"
    }
}

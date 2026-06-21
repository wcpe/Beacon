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
 * ingest-plugins 命令载荷：ingest 落到哪个覆盖层。
 *
 * agent 不消费 scope/group/target 做落盘决策（落盘层由控制面 ingest 决定），仅原样回传命令 id；
 * 这里解析出来仅供日志可读，不参与抓取逻辑。
 *
 * @param scope  覆盖层（group / server）
 * @param group  目标大区
 * @param target server 层目标 serverId（group 层为空）
 */
data class IngestCommandPayload(
    val scope: String,
    val group: String,
    val target: String,
)

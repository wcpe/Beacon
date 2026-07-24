package top.wcpe.beacon.agent.core.connection

/**
 * 连接事件类型（FR-145 §4.1）：open 建立会话行、close 更新同 connId 行。
 *
 * @property wire 上线枚举值（应用层校验，落 VARCHAR，禁 DB 专有 ENUM）
 */
enum class ConnectionEventKind(val wire: String) {
    OPEN("open"),
    CLOSE("close"),
}

/**
 * 一条玩家连接事件（proxy 采集，FR-145）。会话行语义：open 插入、close 更新同 [connId]。
 *
 * 时间字段为 Unix 毫秒（内部机器友好），上线经适配器格式化为 UTC ISO8601。close 特有字段
 * （closedAt/duration 由控制面据 opened_at 算/closeKind/backend 摘要）仅 close 事件填，open 事件为 null。
 *
 * @param connId 连接会话 UUIDv7（open 时生成，close 复用同值）
 * @param playerUuid 玩家 UUID
 * @param playerName 登录时玩家名
 * @param clientIp 客户端地址（可空）
 * @param protocolVersion MC 协议号（可空）
 * @param openedAtMs 连接建立时刻（Unix 毫秒）
 * @param closedAtMs 断开时刻（Unix 毫秒）；open 事件为 null
 * @param closeKind 断开分类 quit/kick/timeout/proxy_shutdown/error；open 事件为 null
 * @param closeReason 断开原文（可空）
 * @param firstBackend 首个后端子服（close 事件填，会话内首次 backend 连接）
 * @param lastBackend 断开时所在后端子服（close 事件填）
 * @param backendSwitchCount 会话内后端切换次数摘要（close 事件填）
 */
data class ConnectionEvent(
    val kind: ConnectionEventKind,
    val connId: String,
    val playerUuid: String,
    val playerName: String,
    val clientIp: String?,
    val protocolVersion: Int?,
    val openedAtMs: Long,
    val closedAtMs: Long?,
    val closeKind: String?,
    val closeReason: String?,
    val firstBackend: String?,
    val lastBackend: String?,
    val backendSwitchCount: Int?,
)

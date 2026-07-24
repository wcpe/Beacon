package top.wcpe.beacon.agent.core.messaging

/**
 * 跨服消息中间件运行参数。
 *
 * Redis 连接（host/port/db/password）由 **Beacon 配置中心下发**（ADR-0016 决策 15），不在本结构里——
 * 本结构只放与连接无关的本地行为参数。开关 [enabled] 默认关（ADR-0016 决策 6）。
 *
 * @param enabled            是否启用消息模块（默认关；关时 isAvailable=false，业务侧降级）
 * @param rpcTimeoutMs       RPC 默认超时（毫秒）：超时未收回信即 Future 失败并清理
 * @param streamMaxLen       每条收件流的近似 MAXLEN 上限（ADR-0016 决策 12：旧消息自动淘汰，防无限增长）
 * @param consumerName       消费组内消费者名（同服多进程场景区分；单进程默认即可）
 */
data class MessagingSettings(
    val enabled: Boolean,
    val rpcTimeoutMs: Long,
    val streamMaxLen: Long,
    val consumerName: String,
)

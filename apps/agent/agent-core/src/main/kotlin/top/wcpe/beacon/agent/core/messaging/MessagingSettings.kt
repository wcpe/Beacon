package top.wcpe.beacon.agent.core.messaging

/**
 * 跨服消息运行参数。
 *
 * 开关 [enabled] 默认关；HTTP 中转模式下无需连接参数（控制面地址复用 agent 主连接）。
 *
 * @param enabled       是否启用消息模块（默认关；关时 isAvailable=false，业务侧降级）
 * @param rpcTimeoutMs  RPC 默认超时（毫秒）：超时未收回信即 Future 失败并清理
 */
data class MessagingSettings(
    val enabled: Boolean,
    val rpcTimeoutMs: Long,
)

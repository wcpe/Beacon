package top.wcpe.beacon.agent.core.settings

/**
 * agent 运行参数（由壳层读 config.yml 构造）。不含身份（身份见 AgentIdentity）。
 *
 * @param endpoints          控制面地址列表（MVP 取首个；多个用于容灾轮询）
 * @param bootstrapToken     共享令牌，置于请求头 X-Beacon-Token（仅防误连，非安全边界）
 * @param pollTimeoutMs      长轮询客户端期望挂起上限（毫秒）
 * @param requestTimeoutMs   注册/心跳/上报普通请求的读超时（毫秒）
 * @param heartbeatFallbackMs 心跳周期兜底值（毫秒），拿到注册响应前过渡用
 * @param backoff            退避参数
 * @param snapshotEnabled    是否启用本地快照 fail-static
 * @param snapshotFileName   快照文件名（落数据目录）
 */
data class AgentSettings(
    val endpoints: List<String>,
    val bootstrapToken: String,
    val pollTimeoutMs: Long,
    val requestTimeoutMs: Long,
    val heartbeatFallbackMs: Long,
    val backoff: BackoffSettings,
    val snapshotEnabled: Boolean,
    val snapshotFileName: String,
) {
    /** 当前选用的控制面基址（MVP：首个 endpoint）。 */
    fun primaryEndpoint(): String = endpoints.first().trimEnd('/')
}

/**
 * 指数退避参数。
 *
 * @param initialMs   初始等待（毫秒）
 * @param maxMs       等待上限（毫秒）
 * @param multiplier  每次倍率
 * @param jitterRatio 抖动比例（±）
 */
data class BackoffSettings(
    val initialMs: Long,
    val maxMs: Long,
    val multiplier: Double,
    val jitterRatio: Double,
)

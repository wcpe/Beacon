package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.messaging.MessagingSettings

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
 * @param fileTree           文件树托管（通道B）同步参数
 * @param override           三方插件文件覆盖兼容（FR-15）本地参数（含本地命令白名单）
 * @param messaging          跨服消息中间件（FR-26）本地行为参数（Redis 连接由控制面下发，不在此）
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
    val fileTree: FileTreeSettings,
    val override: OverrideSettings,
    // 默认关闭的消息参数：让既有测试与不关心消息的调用方无需显式构造（FR-26 增量字段）。
    val messaging: MessagingSettings = MessagingSettings(
        enabled = false,
        rpcTimeoutMs = 5000,
        streamMaxLen = 10000,
        consumerName = "default",
    ),
) {
    /** 当前选用的控制面基址（MVP：首个 endpoint）。 */
    fun primaryEndpoint(): String = endpoints.first().trimEnd('/')
}

/**
 * 文件树托管（通道B）同步参数。
 *
 * @param enabled              是否启用文件树同步循环（关则不拉、不落盘，纯配置中心行为）
 * @param targetSubDir         镜像目标子目录（相对插件 plugins 基目录；空串=直接落 plugins 根）
 * @param appliedManifestFileName 本地已落盘清单文件名（落 agent 数据目录）
 */
data class FileTreeSettings(
    val enabled: Boolean,
    val targetSubDir: String,
    val appliedManifestFileName: String,
)

/**
 * 三方插件文件覆盖兼容（FR-15）本地参数（ADR-0011）。
 *
 * 命令白名单**放本地、不由控制面下发**（反向制衡）：控制面被攻破也无法越白名单提权。**默认空**。
 *
 * @param commandWhitelist 重载命令首 token 白名单（默认空 = 命令派发能力实质关闭）
 * @param backupDirName    覆盖前备份区目录名（落 agent 数据目录下，与镜像目标分离）
 */
data class OverrideSettings(
    val commandWhitelist: Set<String>,
    val backupDirName: String,
)

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

package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.filetree.FileManifest
import top.wcpe.beacon.agent.core.override.OverrideManifest

/**
 * 注册结果（对应 register 200 响应）。
 *
 * @param instanceKey          实例键（namespace/serverId）
 * @param resolvedGroup        控制面解析的大区
 * @param resolvedZone         控制面指派的小区（未指派为 null）
 * @param heartbeatIntervalSec 下发的心跳周期（秒）
 * @param ttlSec               失联判定 TTL（秒）
 * @param assigned             是否已分配 zone
 */
data class RegisterResult(
    val instanceKey: String,
    val resolvedGroup: String?,
    val resolvedZone: String?,
    val heartbeatIntervalSec: Int,
    val ttlSec: Int,
    val assigned: Boolean,
)

/** 心跳结果（对应 heartbeat 200 响应）。 */
data class HeartbeatResult(
    val ttlSec: Int,
    val configDirty: Boolean,
)

/**
 * 打开 SSE 流时上报的各通道当前 md5（供控制面"连接即对账"，FR-24/FR-29）。
 *
 * 配置/文件/覆盖三通道与原长轮询一一对应；topology 为本地已知拓扑摘要（FR-29，首连为空让控制面补一次）。
 * 空串表示本地尚无该通道内容（首连 / 未启用），控制面据此补发全量。
 */
data class ReportedChannelMd5(
    val config: String,
    val file: String,
    val override: String,
    // 拓扑摘要（FR-29）：agent 不本地维护拓扑，首连恒为空让控制面补发一次 topology-changed。
    val topology: String = "",
)

/** 长轮询有效配置的结果。 */
sealed class PollResult {
    /** 200：有变更，携带新有效配置。 */
    data class Changed(val effective: EffectiveResult) : PollResult()

    /** 304：超时无变更，续杯（沿用旧 md5）。 */
    object NotModified : PollResult()

    /** 404：未注册，需回到注册流程。 */
    object NotRegistered : PollResult()

    /** 连接级失败/其它非预期状态：退避后重试。 */
    data class Failed(val reason: String) : PollResult()
}

/** 长轮询文件清单的结果（通道B，与配置长轮询独立）。 */
sealed class FileManifestPollResult {
    /** 200：fileTreeMd5 有变更，携带新清单（path→md5，不含内容）。 */
    data class Changed(val manifest: FileManifest) : FileManifestPollResult()

    /** 304：超时无变更，续杯（沿用旧 fileTreeMd5）。 */
    object NotModified : FileManifestPollResult()

    /** 404：未注册，需回到注册流程。 */
    object NotRegistered : FileManifestPollResult()

    /** 连接级失败/其它非预期状态：退避后重试。 */
    data class Failed(val reason: String) : FileManifestPollResult()
}

/** 长轮询三方覆盖集投递的结果（FR-15，与文件长轮询独立的 md5 维度）。 */
sealed class OverridePollResult {
    /** 200：overrideMd5 有变更，携带新清单（目标根 + 命令 + 成员 path，不含内容）。 */
    data class Changed(val manifest: OverrideManifest) : OverridePollResult()

    /** 304：超时无变更，续杯（沿用旧 overrideMd5）。 */
    object NotModified : OverridePollResult()

    /** 404：未注册，需回到注册流程。 */
    object NotRegistered : OverridePollResult()

    /** 连接级失败/其它非预期状态：退避后重试。 */
    data class Failed(val reason: String) : OverridePollResult()
}

/** 注册结果的状态分类（区分成功 / 重复 / 鉴权失败 / 身份缺失 / 连接失败）。 */
sealed class RegisterOutcome {
    /** 200：注册成功。 */
    data class Success(val result: RegisterResult) : RegisterOutcome()

    /** 202：v2 身份已进入待人工确认。 */
    data class PendingApproval(val serverId: String, val namespace: String) : RegisterOutcome()

    /** 200：v2 身份已确认但被禁用。 */
    object Disabled : RegisterOutcome()

    /** 403：v2 身份已被拒绝。 */
    object Rejected : RegisterOutcome()

    /** 409：v2 身份处于冲突态，等待后台处置。 */
    object IdentityConflict : RegisterOutcome()

    /** 409：重复 serverId。 */
    object DuplicateServerId : RegisterOutcome()

    /** 403：实例已被控制面主动下线，拒绝接入（FR-49，区别于 404 未注册 / 409 重复）。 */
    object OfflineRejected : RegisterOutcome()

    /** 401：token 缺失/错误。 */
    object Unauthorized : RegisterOutcome()

    /** 400：身份缺失（本地已前置守卫，理论不应到此）。 */
    object IdentityRequired : RegisterOutcome()

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : RegisterOutcome()
}

/**
 * v2 指标批量上报结果（FR-144 §5.1）。区分成功 / 过载 / 未确认 / 拒绝 / 连接失败，
 * 供上报循环决定 ack 缓冲还是保留重试（仅 [Accepted] 才移除已上报样本）。
 */
sealed class MetricsReportOutcome {
    /** 202：控制面已受理该批。accepted / deduplicated 为控制面回报的入库 / 去重数，rttMs 为本批上报往返毫秒。 */
    data class Accepted(val accepted: Int, val deduplicated: Int, val rttMs: Int) : MetricsReportOutcome()

    /** 429：控制面写入队列忙（过载保护）；agent 视为上报失败保留缓冲、下一 tick 重试。 */
    object Busy : MetricsReportOutcome()

    /** 403：身份未确认，尚不能上报指标（基座 §2）；保留缓冲，待确认后再报。 */
    object Forbidden : MetricsReportOutcome()

    /** 400：请求被拒（如 clock_skew_too_large 时钟偏移过大）；保留缓冲，携带脱敏原因供告警。 */
    data class Rejected(val reason: String) : MetricsReportOutcome()

    /** 连接级失败 / 5xx / 其它非预期状态：保留缓冲，退避外由固定 5s 节奏重试。 */
    data class Failed(val reason: String) : MetricsReportOutcome()
}

/** v2 registration 长轮询结果。 */
sealed class RegistrationPollResult {
    /** 身份已确认可继续注册数据面。 */
    object Active : RegistrationPollResult()

    /** 仍在等待人工确认。 */
    object Pending : RegistrationPollResult()

    /** 身份被禁用。 */
    object Disabled : RegistrationPollResult()

    /** 身份被拒绝。 */
    object Rejected : RegistrationPollResult()

    /** 身份冲突。 */
    object Conflict : RegistrationPollResult()

    /** 304：本轮无状态变化。 */
    object NotModified : RegistrationPollResult()

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : RegistrationPollResult()
}

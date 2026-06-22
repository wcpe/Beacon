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

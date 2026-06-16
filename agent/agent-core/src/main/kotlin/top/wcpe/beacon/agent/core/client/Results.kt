package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.filetree.FileManifest

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

/** 注册结果的状态分类（区分成功 / 重复 / 鉴权失败 / 身份缺失 / 连接失败）。 */
sealed class RegisterOutcome {

    /** 200：注册成功。 */
    data class Success(val result: RegisterResult) : RegisterOutcome()

    /** 409：重复 serverId。 */
    object DuplicateServerId : RegisterOutcome()

    /** 401：token 缺失/错误。 */
    object Unauthorized : RegisterOutcome()

    /** 400：身份缺失（本地已前置守卫，理论不应到此）。 */
    object IdentityRequired : RegisterOutcome()

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : RegisterOutcome()
}

package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.command.AssetEntry
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

/** 服务发现拉取结果：成功空列表是权威快照，失败则调用方必须保留既有状态。 */
sealed class DiscoveryFetchResult<out T> {
    /** 200：成功取得权威实例快照，列表可为空。 */
    data class Success<T>(val instances: List<T>) : DiscoveryFetchResult<T>()

    /** 连接级失败或非 200：不得把失败误当权威空快照。 */
    data class Failed(val reason: String) : DiscoveryFetchResult<Nothing>()
}

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
    /**
     * 202：控制面已受理该批。accepted / deduplicated 为控制面回报的入库 / 去重数，rttMs 为本批上报往返毫秒。
     * self 为控制面顺带回传的自身健康视图（FR-147/FR-148），无视图时为 null，供 `selfHealth()` 消费。
     */
    data class Accepted(
        val accepted: Int,
        val deduplicated: Int,
        val rttMs: Int,
        val self: SelfHealth? = null,
    ) : MetricsReportOutcome()

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

// ---- 调度 / 健康（FR-148 §5.1）：wire DTO 与调用结果 ----
// 线上键为 camelCase（v2 API 通用约定）；level 为小写线上值（healthy/degraded/unhealthy），
// 到 API 强类型枚举的映射在 SchedulingView 边界完成，client 只承载 wire 值，不依赖 api 枚举。

/** 自身健康视图（指标上报 202 响应内的 self 段）；level 为小写线上值。 */
data class SelfHealth(
    val score: Int,
    val level: String,
    val schedulable: Boolean,
    val reasons: List<String>,
)

/** 单个候选（candidates 快照元素 / 本地快照元素）；level 为小写线上值。 */
data class CandidateEntry(
    val serverId: String,
    val score: Int,
    val level: String,
    val schedulable: Boolean,
    val onlineCount: Int,
    val maxOnline: Int,
)

/** 某小区的候选集（candidates 响应 zones 元素）。 */
data class ZoneCandidates(
    val zone: String,
    val candidates: List<CandidateEntry>,
)

/** candidates 200 响应体（生成时刻 + 各 zone 候选）。 */
data class SchedCandidates(
    val generatedAtMs: Long,
    val zones: List<ZoneCandidates>,
)

/** 拉取候选快照的结果。 */
sealed class SchedCandidatesOutcome {
    /** 200：成功，携带候选快照。 */
    data class Success(val candidates: SchedCandidates) : SchedCandidatesOutcome()

    /** 连接级失败 / 其它非预期状态：本轮放弃刷新，沿用上一快照（fail-static）。 */
    data class Failed(val reason: String) : SchedCandidatesOutcome()
}

/** decide 命中的选择（serverId + 健康分）。 */
data class DecidedChoice(
    val serverId: String,
    val score: Int,
)

/** 请求控制面做一次决策的结果。 */
sealed class SchedDecideOutcome {
    /** 200：控制面完成决策（chosen 为 null 表示无候选，此时 failReason 非空）。 */
    data class Decided(
        val traceId: String,
        val chosen: DecidedChoice?,
        val candidateCount: Int,
        val excludedCount: Int,
        val failReason: String?,
    ) : SchedDecideOutcome()

    /** 404：目标小区不存在（zone_not_found）——控制面权威判定，非降级触发。 */
    object ZoneNotFound : SchedDecideOutcome()

    /** 403：跨 namespace 被拒（cross_namespace）——控制面权威判定。 */
    object CrossNamespace : SchedDecideOutcome()

    /** 400：请求被拒（如 INVALID_PARAM）；携脱敏原因。 */
    data class Rejected(val reason: String) : SchedDecideOutcome()

    /** 连接级失败 / 超时 / 5xx / 其它：触发本地快照降级决策（fail-static）。 */
    data class Failed(val reason: String) : SchedDecideOutcome()
}

/** 一台候选的排除记录（report-local 补报的 excluded 元素）。 */
data class ExcludedRef(
    val serverId: String,
    val reason: String,
)

/** 降级期一条本地决策的补报记录（report-local 请求 decisions 元素）。 */
data class LocalDecisionReport(
    val localTraceId: String,
    val tsMs: Long,
    val zone: String,
    val plugin: String?,
    val purpose: String?,
    val candidateCount: Int,
    val excluded: List<ExcludedRef>,
    val chosenServerId: String?,
    val failReason: String?,
)

/** 补报降级决策的结果。 */
sealed class SchedReportLocalOutcome {
    /** 202：控制面已受理（accepted / deduplicated 为入库 / 按 localTraceId 去重数）。 */
    data class Accepted(val accepted: Int, val deduplicated: Int) : SchedReportLocalOutcome()

    /** 400：请求被拒（如超 100 条 / 参数非法）；携脱敏原因。 */
    data class Rejected(val reason: String) : SchedReportLocalOutcome()

    /** 403：身份未确认，尚不能补报；保留队列待恢复后重试。 */
    object Forbidden : SchedReportLocalOutcome()

    /** 连接级失败 / 其它非预期状态：保留队列下次恢复再补报（best-effort）。 */
    data class Failed(val reason: String) : SchedReportLocalOutcome()
}

// ---- P5 连接采集批上报（FR-145 §5.1）：wire DTO 与调用结果 ----

/** 连接批上报的结果（对应 /connections/batch 202 / 429）。 */
sealed class ConnectionsReportOutcome {
    /** 202：控制面已受理（accepted / duplicated 为入库 / 幂等去重数）。 */
    data class Accepted(val accepted: Int, val duplicated: Int) : ConnectionsReportOutcome()

    /** 429：控制面写入队列忙（过载保护）；agent 保留缓冲退避后重报。 */
    object Busy : ConnectionsReportOutcome()

    /** 403：身份未确认，尚不能上报；保留缓冲待确认后再报。 */
    object Forbidden : ConnectionsReportOutcome()

    /** 连接级失败 / 其它非预期状态：保留缓冲下一轮重报。 */
    data class Failed(val reason: String) : ConnectionsReportOutcome()
}

// ---- P5 跨服消息（FR-149）：wire DTO 与调用结果 ----

/**
 * 上行发送的一条消息（client 侧 wire DTO）。
 *
 * 由 messaging 适配器从信封解出后传入，避免 client 反向依赖 messaging.Message（保持包依赖单向）。
 *
 * @param targetKind server / player / broadcast（[top.wcpe.beacon.agent.core.messaging.Message.TARGET_SERVER] 等值）
 * @param targetServerId targetKind=server 时的目标子服；否则 null
 * @param targetPlayerUuid targetKind=player 时的目标玩家；否则 null
 * @param targetZone targetKind=broadcast 时的可选 zone 级定向（FR-180）；否则 null
 * @param sentAtMs 发送时刻（Unix 毫秒）；上线格式化为 UTC ISO8601
 */
data class OutboundMessage(
    val messageId: String,
    val msgType: String,
    val targetKind: String,
    val targetServerId: String?,
    val targetPlayerUuid: String?,
    val correlationId: String?,
    val payload: Any?,
    val sentAtMs: Long,
    val targetZone: String? = null,
)

/**
 * 长轮询取回的一条待投消息（对应 /messages/poll 200 的 messages 元素）。
 *
 * @param broadcast 广播投递标记（wire additive 键，FR-180）：true 时按 topic 订阅分发表路由，定向缺省 false
 */
data class PolledMessage(
    val messageId: String,
    val msgType: String,
    val sourceServerId: String,
    val correlationId: String?,
    val payload: Any?,
    val createdAt: String,
    val broadcast: Boolean = false,
)

/** 消息上行发送的结果（对应 /messages/send 200 / 403 / 400）。 */
sealed class MessageSendOutcome {
    /** 200：控制面已受理（status 为 accepted / failed 等控制面回报状态）。 */
    data class Ok(val messageId: String, val status: String) : MessageSendOutcome()

    /** 403：跨 namespace 无信任被拒。 */
    object Forbidden : MessageSendOutcome()

    /** 400：请求被拒（如 payload_too_large）；携脱敏原因码。 */
    data class Rejected(val reason: String) : MessageSendOutcome()

    /** 连接级失败 / 其它非预期状态。 */
    data class Failed(val reason: String) : MessageSendOutcome()
}

/** 长轮询取消息的结果（对应 /messages/poll 200 / 204）。 */
sealed class MessagePollOutcome {
    /** 200：取到一批待投消息。 */
    data class Messages(val messages: List<PolledMessage>) : MessagePollOutcome()

    /** 204：本轮无消息（长轮询超时）。 */
    object Empty : MessagePollOutcome()

    /** 连接级失败 / 其它非预期状态：退避后重试。 */
    data class Failed(val reason: String) : MessagePollOutcome()
}

/** 一条消息回执（ack 请求的 results 元素）。 */
data class MessageAck(
    val messageId: String,
    val status: String,
    val reason: String?,
    val deliveredAtMs: Long,
    val handlerCostMs: Long?,
)

/** 批量回执的结果（对应 /messages/ack 200）。 */
sealed class MessageAckOutcome {
    /** 200：控制面已应用（applied / ignored 为生效 / 未知 messageId 忽略数）。 */
    data class Applied(val applied: Int, val ignored: Int) : MessageAckOutcome()

    /** 连接级失败 / 其它非预期状态：best-effort，下一轮取消息后仍会重投则再 ack。 */
    data class Failed(val reason: String) : MessageAckOutcome()
}

// ---- P8 文件资产清单上报（FR-163 §5.1，见 ADR asset-manifest-sync-protocol）：wire DTO 与调用结果 ----

/**
 * 文件资产清单上报的公共报文段（FR-163）：模式 + 扫描时刻 / 耗时 / 截断 + 本次条目（delta upserts / full 分片）。
 *
 * 收敛 [BeaconApiClient.reportAssetManifest] 参数（避免长参数列表）；mode 决定 delta（携 baseDigest+deleted）
 * 还是 full（携 uploadId+seq+eof）分支。
 *
 * @param mode          "delta" 或 "full"
 * @param scannedAtMs   扫描完成时刻（Unix 毫秒），上线格式化为 UTC ISO8601
 * @param scanDurationMs 扫描耗时（毫秒）
 * @param truncated     是否因超单服文件数上限被截断
 * @param upserts       本次上报的条目（delta=变化项，full=该分片条目）
 */
data class AssetManifestMeta(
    val mode: String,
    val scannedAtMs: Long,
    val scanDurationMs: Long,
    val truncated: Boolean,
    val upserts: List<AssetEntry>,
)

/**
 * 文件资产清单上报结果（对应 POST /beacon/v2/agent/assets/manifest 的 200/409/400）。
 *
 * 供扫描协调器决定：入库确认（[Accepted] 且返回摘要与本地一致）、退全量自愈（[OutOfSync]）、
 * 告警脱敏原因（[Rejected]）、fail-static 保留本地状态等下周期（[Failed]）。
 */
sealed class AssetManifestOutcome {
    /** 200：控制面已应用。digest 为应用后清单摘要（全量非末批分片可能为空串），fileCount 为清单文件数。 */
    data class Accepted(val digest: String, val fileCount: Int) : AssetManifestOutcome()

    /** 409：delta 基线失配 / 全量暂存丢失过期 / seq 跳号（asset_manifest_out_of_sync）；agent 下次改发全量。 */
    object OutOfSync : AssetManifestOutcome()

    /** 400：参数错误（携脱敏原因码）。 */
    data class Rejected(val code: String) : AssetManifestOutcome()

    /** 连接级失败 / 其它非预期状态：fail-static 保留本地状态、等下周期。 */
    data class Failed(val reason: String) : AssetManifestOutcome()
}

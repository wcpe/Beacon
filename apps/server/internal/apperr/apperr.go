// Package apperr 定义带业务码与 HTTP 状态的领域错误。
// 由 service 层产生、handler 层经 render 统一转换为对外错误响应体。
// 它是叶子包（不依赖其它内部包），供各层共用，避免反向依赖。
package apperr

import "net/http"

// Error 是带业务码与 HTTP 状态的领域错误。
type Error struct {
	// 业务码，如 NAMESPACE_CONFLICT
	Code string
	// 面向调用方的中文说明
	Message string
	// 对应的 HTTP 状态码
	Status int
}

// Error 实现 error 接口。
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New 构造一个业务错误。
func New(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

// 预定义业务错误（按需新增，不预留未使用项）。
var (
	// ErrInvalidParam 参数错误。
	ErrInvalidParam = New(http.StatusBadRequest, "INVALID_PARAM", "参数错误")
	// ErrNamespaceConflict 同名环境已存在。
	ErrNamespaceConflict = New(http.StatusConflict, "NAMESPACE_CONFLICT", "同名环境已存在")
	// ErrNamespaceNotFound 环境不存在（改名 / 删除目标缺失，FR-53）。
	ErrNamespaceNotFound = New(http.StatusNotFound, "NAMESPACE_NOT_FOUND", "环境不存在")
	// ErrNamespaceHasInstances 环境下仍有已注册实例，禁删（FR-53 删除守卫①）。
	ErrNamespaceHasInstances = New(http.StatusConflict, "NAMESPACE_HAS_INSTANCES", "环境下仍有已注册实例，请先下线后再删除")
	// ErrNamespaceHasAssignments 环境下仍有已指派 zone，禁删（FR-53 删除守卫②）。
	ErrNamespaceHasAssignments = New(http.StatusConflict, "NAMESPACE_HAS_ASSIGNMENTS", "环境下仍有已指派的 zone，请先取消指派后再删除")
	// ErrNamespaceHasConfigs 环境下仍有配置项，禁删（FR-53 删除守卫③）。
	ErrNamespaceHasConfigs = New(http.StatusConflict, "NAMESPACE_HAS_CONFIGS", "环境下仍有配置，请先删除配置后再删除")
	// ErrNamespaceHasFiles 环境下仍有文件树（通道B），禁删（FR-53 删除守卫④）。
	ErrNamespaceHasFiles = New(http.StatusConflict, "NAMESPACE_HAS_FILES", "环境下仍有文件树，请先删除文件后再删除")
	// ErrNamespaceHasOverrideSets 环境下仍有覆盖集（FR-15），禁删（FR-53 删除守卫⑤）。
	ErrNamespaceHasOverrideSets = New(http.StatusConflict, "NAMESPACE_HAS_OVERRIDE_SETS", "环境下仍有覆盖集，请先删除覆盖集后再删除")

	// ErrInvalidScope 覆盖层或其目标键不合法。
	ErrInvalidScope = New(http.StatusBadRequest, "INVALID_SCOPE", "覆盖层或目标键不合法")
	// ErrConfigNotFound 配置项不存在。
	ErrConfigNotFound = New(http.StatusNotFound, "CONFIG_NOT_FOUND", "配置项不存在")
	// ErrRevisionNotFound 回滚目标版本不存在。
	ErrRevisionNotFound = New(http.StatusNotFound, "REVISION_NOT_FOUND", "目标版本不存在")
	// ErrConfigConflict 同标识配置项已存在。
	ErrConfigConflict = New(http.StatusConflict, "CONFIG_CONFLICT", "同标识配置项已存在")
	// ErrContentTooLarge 内容超出大小上限。
	ErrContentTooLarge = New(http.StatusUnprocessableEntity, "CONTENT_TOO_LARGE", "配置内容超出大小上限")
	// ErrContentInvalid 内容按声明格式解析失败。
	ErrContentInvalid = New(http.StatusUnprocessableEntity, "CONTENT_INVALID", "配置内容解析失败")
	// ErrContentSchemaInvalid 内容结构 / 类型 / 必填项校验不通过（FR-27，发布前拦截坏配置）。
	ErrContentSchemaInvalid = New(http.StatusUnprocessableEntity, "CONTENT_SCHEMA_INVALID", "配置结构或类型校验不通过")
	// ErrFormatInconsistent 同一 dataId 跨层格式不一致。
	ErrFormatInconsistent = New(http.StatusUnprocessableEntity, "FORMAT_INCONSISTENT", "同一 dataId 跨层格式不一致")
	// ErrGrayNotFound 灰度不存在（promote/abort 时无活跃灰度，FR-9）。
	ErrGrayNotFound = New(http.StatusNotFound, "GRAY_NOT_FOUND", "灰度不存在")
	// ErrEmptyCohort 灰度 cohort 名单为空（无意义灰度，FR-9）。
	ErrEmptyCohort = New(http.StatusBadRequest, "EMPTY_COHORT", "灰度 cohort 名单为空")

	// ErrFileNotFound 文件对象不存在（通道B）。
	ErrFileNotFound = New(http.StatusNotFound, "FILE_NOT_FOUND", "文件对象不存在")
	// ErrFileConflict 同标识文件对象已存在（通道B）。
	ErrFileConflict = New(http.StatusConflict, "FILE_CONFLICT", "同标识文件对象已存在")
	// ErrInvalidPath 文件相对 path 不合法（通道B）。
	ErrInvalidPath = New(http.StatusBadRequest, "INVALID_PATH", "文件路径不合法")
	// ErrTooManyFiles 单次导入文件数超出上限（FR-38）。
	ErrTooManyFiles = New(http.StatusUnprocessableEntity, "TOO_MANY_FILES", "单次导入文件数超出上限")
	// ErrCommandNotFound agent 命令不存在或已不可回传（过期 / 已完成 / 状态不符，FR-39）。
	ErrCommandNotFound = New(http.StatusNotFound, "COMMAND_NOT_FOUND", "命令不存在或已失效")
	// ErrImprintNotReady 拓印命令非 ready 态，不可 diff / confirm（FR-46）。
	ErrImprintNotReady = New(http.StatusConflict, "IMPRINT_NOT_READY", "拓印命令未就绪")
	// ErrImprintReviewMismatch 拓印确认自审 md5 与已抓取内容不符（强制看过 diff，FR-46）。
	ErrImprintReviewMismatch = New(http.StatusPreconditionFailed, "IMPRINT_REVIEW_MISMATCH", "拓印自审内容已变更，请重新查看 diff")
	// ErrAgentLogActive 该实例已有进行中的取日志命令，限速拒新建（FR-88，见 ADR-0040）。
	ErrAgentLogActive = New(http.StatusConflict, "AGENT_LOG_ACTIVE", "该实例已有进行中的取日志请求，请稍候")
	// ErrBrowseTimeout 文件浏览等待 agent 回传超时（agent 离线 / 未及时回传，FR-110，见 ADR-0049）。
	ErrBrowseTimeout = New(http.StatusGatewayTimeout, "BROWSE_TIMEOUT", "目标实例未在限期内返回浏览结果")
	// ErrBrowseTargetNotFound 文件浏览目标越权 / 非目录 / 非文本（agent 原语拒读，FR-110）。
	ErrBrowseTargetNotFound = New(http.StatusNotFound, "BROWSE_TARGET_NOT_FOUND", "浏览目标不存在或不可读")
	// ErrFileSyncTaskNotFound 文件同步任务不存在（FR-129/FR-131）。
	ErrFileSyncTaskNotFound = New(http.StatusNotFound, "FILE_SYNC_TASK_NOT_FOUND", "文件同步任务不存在")
	// ErrFileSyncTaskState 文件同步任务状态不允许当前操作（FR-131）。
	ErrFileSyncTaskState = New(http.StatusConflict, "FILE_SYNC_TASK_STATE", "文件同步任务状态不允许当前操作")
	// ErrFileSyncSourceInvalid 文件同步源实例不存在、离线或不是 bukkit（FR-129）。
	ErrFileSyncSourceInvalid = New(http.StatusBadRequest, "FILE_SYNC_SOURCE_INVALID", "文件同步源实例必须在线且为 bukkit")
	// ErrFileSyncTargetInvalid 文件同步目标实例不存在、离线或不是 bukkit（FR-131）。
	ErrFileSyncTargetInvalid = New(http.StatusBadRequest, "FILE_SYNC_TARGET_INVALID", "文件同步目标实例必须在线且为 bukkit")
	// ErrFileSyncNoTargets 文件同步目标为空（FR-131）。
	ErrFileSyncNoTargets = New(http.StatusBadRequest, "FILE_SYNC_NO_TARGETS", "文件同步目标不能为空")

	// ErrReverseFetchTaskNotFound 反向抓取受管任务不存在（FR-58）。
	ErrReverseFetchTaskNotFound = New(http.StatusNotFound, "REVERSE_FETCH_TASK_NOT_FOUND", "反向抓取任务不存在")
	// ErrReverseFetchTaskActive 该实例已有活跃（非终态）反向抓取任务，单实例互斥拒新建（FR-58，见 ADR-0037）。
	ErrReverseFetchTaskActive = New(http.StatusConflict, "REVERSE_FETCH_TASK_ACTIVE", "该实例已有活跃反向抓取任务，请先完成或取消")
	// ErrReverseFetchTaskState 反向抓取任务状态不符（当前态不允许该操作 / 已被并发终结，FR-58）。
	ErrReverseFetchTaskState = New(http.StatusConflict, "REVERSE_FETCH_TASK_STATE", "反向抓取任务状态不允许该操作")
	// ErrOverThresholdNotConfirmed 选定集含超单文件阈值的文件但未显式确认（只拒该文件，不拒整批，FR-58）。
	ErrOverThresholdNotConfirmed = New(http.StatusBadRequest, "OVER_THRESHOLD_NOT_CONFIRMED", "选定集含超阈值文件，须显式确认才能纳入")
	// ErrReverseFetchReviewMismatch 冲突审核确认覆盖时自审 md5 与抓取内容不符（强制看过 diff，盲确认拒，FR-59）。
	ErrReverseFetchReviewMismatch = New(http.StatusPreconditionFailed, "REVERSE_FETCH_REVIEW_MISMATCH", "冲突审核自审内容已变更，请重新查看 diff")
	// ErrReverseFetchConflictNotFound 请求的冲突 path 不在本任务冲突集内（diff / resolve 目标缺失，FR-59）。
	ErrReverseFetchConflictNotFound = New(http.StatusNotFound, "REVERSE_FETCH_CONFLICT_NOT_FOUND", "冲突文件不存在")

	// ErrOverrideSetNotFound 覆盖集不存在（FR-15）。
	ErrOverrideSetNotFound = New(http.StatusNotFound, "OVERRIDE_SET_NOT_FOUND", "覆盖集不存在")
	// ErrOverrideSetConflict 同标识覆盖集已存在（FR-15）。
	ErrOverrideSetConflict = New(http.StatusConflict, "OVERRIDE_SET_CONFLICT", "同标识覆盖集已存在")
	// ErrInvalidTargetRoot 覆盖集目标根目录不合法（FR-15，见 ADR-0011 决策 4）。
	ErrInvalidTargetRoot = New(http.StatusBadRequest, "INVALID_TARGET_ROOT", "目标根目录不合法")
	// ErrInvalidReloadCommand 重载命令不合法（含元字符 / 多条 / 越限等，FR-15，见 ADR-0011 决策 3）。
	ErrInvalidReloadCommand = New(http.StatusBadRequest, "INVALID_RELOAD_COMMAND", "重载命令不合法")

	// ErrUnauthorized agent 端缺少或错误的 token。
	ErrUnauthorized = New(http.StatusUnauthorized, "UNAUTHORIZED", "缺少或非法的 token")
	// ErrAgentNotConfirmed agent 身份未人工确认（status≠active），禁止调数据面上报端点（v2 指标 / 调度，spec §4.2）。
	// 区别于 401（token / 身份非法）——已识别但未获准，故 403。code 用 agent 面小写下划线约定。
	ErrAgentNotConfirmed = New(http.StatusForbidden, "agent_not_confirmed", "agent 身份未确认，禁止上报")
	// ErrClockSkewTooLarge agent 上报时钟与控制面偏移超阈值（>5min），整批拒绝，倒逼校时（spec §4.2/§5.1）。
	ErrClockSkewTooLarge = New(http.StatusBadRequest, "clock_skew_too_large", "上报时钟与控制面偏移过大，请校时后重试")
	// ErrMetricsIngestBusy 指标写入队列已满，控制面过载保护，agent 保留缓冲重试不丢数据（spec §4.3/§5.1）。
	ErrMetricsIngestBusy = New(http.StatusTooManyRequests, "metrics_ingest_busy", "指标写入繁忙，请稍后重试")
	// ErrAssetManifestOutOfSync 文件清单增量基线摘要失配（delta baseDigest ≠ 库内摘要 / 全量分片暂存丢失或乱序），
	// agent 收到即改发全量自愈（FR-163，spec §4.3，见 ADR 清单上报协议）。code 用 agent 面小写下划线约定。
	ErrAssetManifestOutOfSync = New(http.StatusConflict, "asset_manifest_out_of_sync", "文件清单基线失配，请改发全量")
	// ErrConnIngestBusy 连接明细写入队列已满，控制面过载保护，agent 退避后重报不丢数据（FR-145，spec §4.1/§5.1）。
	ErrConnIngestBusy = New(http.StatusTooManyRequests, "conn_ingest_busy", "连接明细写入繁忙，请稍后重试")
	// ErrPayloadTooLarge 消息 payload 超出上限（默认 64KB），发送请求被拒不截断（FR-150，spec §3.4/§5.1）。
	ErrPayloadTooLarge = New(http.StatusBadRequest, "payload_too_large", "消息 payload 超出大小上限")
	// ErrMessageCrossNamespaceNoTrust 跨 namespace 消息目标无 capability=message 信任，拒绝并记 failed（FR-149，spec §4.2）。
	ErrMessageCrossNamespaceNoTrust = New(http.StatusForbidden, "namespace_not_trusted", "跨环境消息目标未授信")
	// ErrSchedZoneNotFound 调度请求的目标 zone 在请求方 namespace 内不存在（FR-146，spec §4.6）。
	ErrSchedZoneNotFound = New(http.StatusNotFound, "zone_not_found", "目标 zone 不存在")
	// ErrSchedCrossNamespace 跨 namespace 调度请求默认拒绝（信任放行规则归 namespace 隔离域，FR-146，spec §4.6）。
	ErrSchedCrossNamespace = New(http.StatusForbidden, "cross_namespace", "禁止跨 namespace 调度请求")
	// ErrInvalidHealthWeights 健康权重配置校验不通过（权重非负 / good、bad 边界有序 / 等级阈值有序，FR-147，spec §4.4）。
	ErrInvalidHealthWeights = New(http.StatusBadRequest, "invalid_health_weights", "健康权重配置不合法")
	// ErrBadCredentials 管理台登录用户名或口令错误。
	ErrBadCredentials = New(http.StatusUnauthorized, "BAD_CREDENTIALS", "用户名或口令错误")
	// ErrAdminUnauthorized 管理台缺少或非法的登录令牌 / API 密钥（含过期 / 已吊销）。
	ErrAdminUnauthorized = New(http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "缺少或非法的登录令牌")
	// ErrForbidden 已认证但无权执行该操作（只读密钥访问写端点，FR-42，见 ADR-0026）。
	ErrForbidden = New(http.StatusForbidden, "FORBIDDEN", "只读密钥无权执行写操作")
	// ErrIdentityRejected 身份已被拒绝，需后台允许重新申请。
	ErrIdentityRejected = New(http.StatusForbidden, "identity_rejected", "身份已被拒绝，请先在后台允许重新申请")
	// ErrIdentityBindingMismatch identityId 与当前 namespace/serverId 绑定不一致。
	ErrIdentityBindingMismatch = New(http.StatusConflict, "identity_binding_mismatch", "身份绑定与当前 namespace 或 serverId 不一致")
	// ErrIdentityConflict 身份处于冲突态，需后台处置。
	ErrIdentityConflict = New(http.StatusConflict, "identity_conflict", "身份处于冲突态，请在后台处置")
	// ErrIdentityConflictLoser 冲突处置落败方 / 副本实例持续拒绝，附人工处理指引（FR-177，spec §4.5）。
	ErrIdentityConflictLoser = New(http.StatusConflict, "identity_conflict", "本实例身份已被判为副本，请删除本目录下 identity.yml 后按新身份重新接入，或直接下线本实例")
	// ErrConflictKeepBootInvalid 冲突处置指定保留的 bootId 不在冲突双方内（FR-177，spec §5.2；code 对齐 devmock）。
	ErrConflictKeepBootInvalid = New(http.StatusBadRequest, "boot_id_not_in_conflict", "保留的 bootId 不在冲突双方内")
	// ErrAgentStaleReregister 数据面上报携带陈旧 bootId（与权威 boot 不一致但未判冲突）→ 404 促 agent 重注册，
	// 喂养并发双实例往复检测（FR-177，spec §4.5）。选 404 而非 401：复用 agent 既有「404→重注册」路径，
	// 活着的旧实例被顶替后据此主动重注册触发往复转 conflict；已死旧实例收 404 不会重注册，故不误判单向切换。
	ErrAgentStaleReregister = New(http.StatusNotFound, "agent_stale_reregister", "boot 已过期或被顶替，请重新注册")
	// ErrServerIDPendingElsewhere 同 namespace/serverId 已有其他待确认身份。
	ErrServerIDPendingElsewhere = New(http.StatusConflict, "server_id_pending_elsewhere", "该 serverId 已有其他待确认身份")
	// ErrServerIDOccupied 同 namespace/serverId 已被其他有效身份占用。
	ErrServerIDOccupied = New(http.StatusConflict, "server_id_occupied", "该 serverId 已被其他身份占用")
	// ErrIllegalState 当前状态不允许该操作。
	ErrIllegalState = New(http.StatusConflict, "illegal_state", "当前状态不允许该操作")
	// ErrRezoneRequired 已分配 server 改归属必须走换区工单。
	ErrRezoneRequired = New(http.StatusConflict, "rezone_required", "已分配 server 改归属必须走换区工单")
	// ErrRezoneNotAssigned 换区工单选中未分配 server（应走首次分配，FR-155）。
	ErrRezoneNotAssigned = New(http.StatusBadRequest, "not_assigned", "server 未分配，应走首次分配")
	// ErrDefaultEntryNotAssigned 未分配小区的 server 不能设为默认入口（FR-155）。
	ErrDefaultEntryNotAssigned = New(http.StatusConflict, "not_assigned", "未分配小区的 server 不能设为默认入口")
	// ErrAPIKeyNotFound API 密钥不存在（吊销 / 重置目标不存在或已吊销，FR-42）。
	ErrAPIKeyNotFound = New(http.StatusNotFound, "API_KEY_NOT_FOUND", "API 密钥不存在")
	// ErrIdentityRequired 注册缺少必要身份（serverId/namespace）。
	ErrIdentityRequired = New(http.StatusBadRequest, "IDENTITY_REQUIRED", "缺少必要的身份标识")
	// ErrDuplicateServerID 同 serverId 已有仍新鲜的不同地址实例在线。
	ErrDuplicateServerID = New(http.StatusConflict, "DUPLICATE_SERVER_ID", "serverId 冲突：已有不同地址实例在线")
	// ErrNotRegistered 心跳/上报时实例未注册。
	ErrNotRegistered = New(http.StatusNotFound, "NOT_REGISTERED", "实例未注册")
	// ErrStreamingUnsupported 当前 ResponseWriter 不支持流式刷写（无 http.Flusher），无法承载 SSE 推送。
	ErrStreamingUnsupported = New(http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "服务端不支持流式推送")
	// ErrInternal 服务端内部错误（依赖未装配等编程/装配错误的兜底）。
	ErrInternal = New(http.StatusInternalServerError, "INTERNAL", "服务端内部错误")
	// ErrInstanceNotFound 实例不存在。
	ErrInstanceNotFound = New(http.StatusNotFound, "INSTANCE_NOT_FOUND", "实例不存在")
	// ErrAssignmentNotFound zone 指派不存在。
	ErrAssignmentNotFound = New(http.StatusNotFound, "ASSIGNMENT_NOT_FOUND", "zone 指派不存在")
	// ErrZoneNotAssignableToBC zone 仅供 bukkit 子服归派，不可分配给 BC 代理实例（FR-8/FR-35）。
	ErrZoneNotAssignableToBC = New(http.StatusBadRequest, "ZONE_NOT_ASSIGNABLE_TO_BC", "zone 不可分配给 BC 代理实例")
	// ErrZoneServerOnlineNonempty 服务器在线且有玩家，禁止变更其区归属（排空门硬闸，FR-71/ADR-0036）。
	ErrZoneServerOnlineNonempty = New(http.StatusConflict, "ZONE_SERVER_ONLINE_NONEMPTY", "服务器在线且有玩家，禁止变更其区归属；请先排空（drain 或等玩家离开）后再操作")
	// ErrDrainNotFound 取消 drain 时该标记不存在（FR-10）。
	ErrDrainNotFound = New(http.StatusNotFound, "DRAIN_NOT_FOUND", "drain 标记不存在")
	// ErrInstanceOfflineRejected 实例已被主动下线，拒绝其注册接入（FR-49，区别于 NOT_REGISTERED / DUPLICATE_SERVER_ID）。
	ErrInstanceOfflineRejected = New(http.StatusForbidden, "INSTANCE_OFFLINE_REJECTED", "实例已被主动下线，禁止接入")
	// ErrOfflineNotFound 取消下线时该下线标记不存在（FR-49）。
	ErrOfflineNotFound = New(http.StatusNotFound, "OFFLINE_NOT_FOUND", "下线标记不存在")

	// ErrSettingKeyNotAllowed 设置 key 不在热改白名单内（写非白名单 / 启动 / 安全项一律拒，FR-61，见 ADR-0038）。
	ErrSettingKeyNotAllowed = New(http.StatusBadRequest, "SETTING_KEY_NOT_ALLOWED", "设置项不存在或不可热改")
	// ErrSettingValueInvalid 设置值非法（类型 / 范围 / 枚举校验不通过，FR-61）。
	ErrSettingValueInvalid = New(http.StatusBadRequest, "SETTING_VALUE_INVALID", "设置值不合法")

	// ErrReversibleOpNotFound 可逆操作账目不存在（撤回目标缺失，FR-116，见 ADR-0051）。
	ErrReversibleOpNotFound = New(http.StatusNotFound, "REVERSIBLE_OP_NOT_FOUND", "可逆操作不存在")
	// ErrReversibleOpExpired 可逆操作已超过可撤回时间窗，不可撤回（FR-116，ADR-0051 决策 8）。
	ErrReversibleOpExpired = New(http.StatusConflict, "REVERSIBLE_OP_EXPIRED", "该操作已超过可撤回时限，不可撤回")
	// ErrReversibleOpSuperseded 可逆操作已被后续操作覆盖，不可撤回（防脏撤回，FR-116，ADR-0051 决策 8）。
	ErrReversibleOpSuperseded = New(http.StatusConflict, "REVERSIBLE_OP_SUPERSEDED", "该操作已被后续操作覆盖，不可撤回")
	// ErrReversibleOpState 可逆操作状态不符 / 反向快照损坏，不可撤回（FR-116）。
	ErrReversibleOpState = New(http.StatusConflict, "REVERSIBLE_OP_STATE", "可逆操作状态不允许撤回")

	// ErrNoRollbackAvailable 无可回退的上一版本备份（.old 不存在，FR-120）。
	ErrNoRollbackAvailable = New(http.StatusConflict, "NO_ROLLBACK_AVAILABLE", "无可回退的上一版本")
	// ErrUpdateInProgress 已有一次在线更新进行中，拒绝并发触发（fix-1：apply 异步后并发守卫）。
	ErrUpdateInProgress = New(http.StatusConflict, "UPDATE_IN_PROGRESS", "已有更新正在进行中")

	// ErrSchedDecisionNotFound 调度决策记录在保留窗内不存在（FR-146，spec §5.2；code 对齐 devmock）。
	ErrSchedDecisionNotFound = New(http.StatusNotFound, "decision_not_found", "调度决策记录不存在")

	// ErrQueryGuardViolation 连接 / 消息列表查询未满足查询防护（无精确 ID 时须带 serverId/playerUuid + 时间范围 ≤168h，
	// FR-145，spec §4.3；code 对齐 devmock）。
	ErrQueryGuardViolation = New(http.StatusBadRequest, "query_guard_violation", "查询须携带精确 ID，或服务器/玩家过滤加不超过 168 小时的时间范围")
	// ErrConnectionNotFound 连接明细在保留窗内不存在（FR-145，spec §5.2；code 对齐 devmock）。
	ErrConnectionNotFound = New(http.StatusNotFound, "connection_not_found", "连接不存在")
	// ErrMessageNotFound 消息记录在保留窗内不存在（FR-149，spec §5.2；code 对齐 devmock）。
	ErrMessageNotFound = New(http.StatusNotFound, "message_not_found", "消息不存在")
	// ErrPayloadReasonRequired 查看 payload 未填原因或原因超长（≤255 字，FR-150，spec §4.4；code 对齐 devmock）。
	ErrPayloadReasonRequired = New(http.StatusBadRequest, "missing_reason", "查看 payload 必须填写原因（≤255 字）")
	// ErrAlertEventNotFound 告警事件不存在（处理目标缺失，FR-157，见 ADR-0064）。
	ErrAlertEventNotFound = New(http.StatusNotFound, "ALERT_EVENT_NOT_FOUND", "告警事件不存在")
	// ErrAlertActionInvalid 告警处理动作不合法（仅允许 acknowledge / resolve，FR-157）。
	ErrAlertActionInvalid = New(http.StatusBadRequest, "ALERT_ACTION_INVALID", "告警处理动作不合法")

	// ErrArchiveJobRunning 已有归档任务在执行中，单飞拒绝并发创建 / 重试（FR-151，spec §4.3，见 ADR-0066）。
	ErrArchiveJobRunning = New(http.StatusConflict, "ARCHIVE_JOB_RUNNING", "已有归档任务在执行中，请稍后再试")
	// ErrArchiveJobNotFound 归档任务不存在（重试 / 取消 / 详情目标缺失，FR-151）。
	ErrArchiveJobNotFound = New(http.StatusNotFound, "ARCHIVE_JOB_NOT_FOUND", "归档任务不存在")
	// ErrArchiveJobState 归档任务当前状态不允许该操作（仅 failed 可重试、仅 pending/running 可取消，FR-151）。
	ErrArchiveJobState = New(http.StatusConflict, "ARCHIVE_JOB_STATE", "归档任务状态不允许该操作")
	// ErrArchiveUnavailable 归档库不可达，归档能力降级不可用（FR-151，spec §4.1，见 ADR-0066）。
	ErrArchiveUnavailable = New(http.StatusServiceUnavailable, "ARCHIVE_UNAVAILABLE", "归档库不可用，归档能力暂不可用")
	// ErrArchiveDomainInvalid 请求的归档域不在注册表内（FR-151，spec §3.1）。
	ErrArchiveDomainInvalid = New(http.StatusBadRequest, "ARCHIVE_DOMAIN_INVALID", "未知的归档域")

	// ErrConfigFileNotFound 配置文件不存在或已入回收站（除回收站端点外一律视同不存在，FR-160，spec §4.9）。
	ErrConfigFileNotFound = New(http.StatusNotFound, "CONFIG_FILE_NOT_FOUND", "配置文件不存在")
	// ErrConfigVersionNotFound 配置层版本不存在（版本详情 / 回退 / diff 引用目标缺失，FR-161）。
	ErrConfigVersionNotFound = New(http.StatusNotFound, "CONFIG_VERSION_NOT_FOUND", "配置版本不存在")
	// ErrConfigFileDuplicate 同 namespace 内逻辑名已被未删除文件占用（创建 / 恢复冲突，spec §4.9）。
	ErrConfigFileDuplicate = New(http.StatusConflict, "CONFIG_FILE_DUPLICATE", "同名配置文件已存在")
	// ErrConfigFileNotTrashed 彻底删除仅对回收站内文件可执行（spec §4.9）。
	ErrConfigFileNotTrashed = New(http.StatusBadRequest, "CONFIG_FILE_NOT_TRASHED", "仅回收站内文件可彻底删除")
	// ErrConfigScopeMismatch 作用域实体不存在或不属于文件 namespace（跨 namespace 强隔离，spec §4.8）。
	ErrConfigScopeMismatch = New(http.StatusBadRequest, "CONFIG_SCOPE_MISMATCH", "作用域不存在或不属于文件所在环境")
	// ErrConfigSyntaxInvalid 配置内容按声明格式解析失败（保存前阻断，spec §4.2；具体原因经 New 构造同码错误携带）。
	ErrConfigSyntaxInvalid = New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", "配置内容语法解析失败")
	// ErrConfigSchemaViolation 内容未通过文件 JSON Schema 校验（逐条 {path,message} 由 service 附带，spec §4.4）。
	ErrConfigSchemaViolation = New(http.StatusBadRequest, "CONFIG_SCHEMA_VIOLATION", "配置内容未通过 schema 校验")
	// ErrConfigVersionConflict 乐观并发冲突：basedOnVersionId 不等于链当前 head（spec §4.2）。
	ErrConfigVersionConflict = New(http.StatusConflict, "CONFIG_VERSION_CONFLICT", "基线版本已过期，请重新加载后合并")
	// ErrConfigNoChange 归一化内容与当前 head 相同，不产生空版本（spec §4.2/§4.6）。
	ErrConfigNoChange = New(http.StatusBadRequest, "CONFIG_NO_CHANGE", "内容与当前版本相同，未产生变更")
	// ErrConfigContentTooLarge 单版本内容超出 1 MiB 上限（spec §3.3）。
	ErrConfigContentTooLarge = New(http.StatusUnprocessableEntity, "CONFIG_CONTENT_TOO_LARGE", "配置内容超出 1 MiB 上限")
	// ErrConfigSensitivePlaceholderInvalid 敏感占位符出现在新增键 / 无上一版本可回填处（spec §4.7）。
	ErrConfigSensitivePlaceholderInvalid = New(http.StatusBadRequest, "CONFIG_SENSITIVE_PLACEHOLDER_INVALID", "敏感占位符无上一版本明文可回填")
	// ErrConfigReasonRequired 高风险配置操作（彻底删除 / 撤销层贡献 / 改敏感路径）必须填写原因（code 对齐 devmock）。
	ErrConfigReasonRequired = New(http.StatusBadRequest, "missing_reason", "该操作必须填写原因")

	// —— 交付编排 V2 变更单（FR-162，M1 组单 / 差异 / 影响预览 / 审批；code 对齐 devmock 小写下划线约定）——
	// ErrChangeOrderNotFound 变更单不存在。
	ErrChangeOrderNotFound = New(http.StatusNotFound, "change_order_not_found", "变更单不存在")
	// ErrChangeItemNotFound 变更项不存在或不是文件差异项（file-diff 目标缺失）。
	ErrChangeItemNotFound = New(http.StatusNotFound, "item_not_found", "文件差异项不存在")
	// ErrChangeSourceMissing 未指定黄金模板源，无法扫描文件差异。
	ErrChangeSourceMissing = New(http.StatusBadRequest, "missing_source", "未指定黄金模板源，无法扫描文件差异")
	// ErrChangeSourceInvalid 模板源不合格（不存在 / 非 backend / 身份未确认绑定 / 不在线）。
	ErrChangeSourceInvalid = New(http.StatusBadRequest, "source_invalid", "模板源必须是本环境内已确认绑定且在线的 backend 子服")
	// ErrChangeSourceSnapshotMissing 模板源尚无文件资产快照，差异计算无依据（防把「源未上报」误判成「源为空」而生成全删差异）。
	ErrChangeSourceSnapshotMissing = New(http.StatusConflict, "source_snapshot_missing", "模板源尚无文件资产快照，请先等待 agent 上报或触发重扫")
	// ErrChangeNoItems 变更单没有任何变更项，不可提交审批。
	ErrChangeNoItems = New(http.StatusBadRequest, "no_items", "变更单没有任何变更项，无法提交审批")
	// ErrChangeNoTarget selector 未解析出任何合格目标。
	ErrChangeNoTarget = New(http.StatusBadRequest, "no_target", "selector 未解析出任何合格目标")
	// ErrChangeSelectorCrossNamespace selector 引用了异 namespace 或不存在的实体（FR-162 跨 namespace 拒绝；
	// 具体冲突实体经 service 用同码 New 携带在 message 中）。
	ErrChangeSelectorCrossNamespace = New(http.StatusBadRequest, "selector_cross_namespace", "selector 引用了不属于本环境的实体")
	// ErrChangeConfigVersionInvalid 配置变更项引用的目标版本不存在、与作用域不匹配或跨 namespace。
	ErrChangeConfigVersionInvalid = New(http.StatusBadRequest, "config_version_invalid", "配置版本不存在或与作用域不匹配")
	// ErrChangeApproverSeparation 审批职责分离：审批人不得是创建人（默认开启，可在运维设置关闭，spec §4.7）。
	ErrChangeApproverSeparation = New(http.StatusForbidden, "approver_separation", "审批人不得是创建人（可在运维设置关闭该限制）")
	// ErrChangeNotCreator 仅创建人可撤回变更单（spec §4.1）。
	ErrChangeNotCreator = New(http.StatusForbidden, "not_creator", "仅创建人可撤回变更单")

	// —— M3 灰度编排（FR-166，spec §4.1/§4.4，见 ADR-0071）——
	// ErrChangeBatchNotFound 批次不存在（推进门确认引用了不存在的批次号）。
	ErrChangeBatchNotFound = New(http.StatusNotFound, "batch_not_found", "批次不存在")
	// ErrChangeResumeModeRequired 熔断 / 准备失败暂停继续时必须指定恢复模式与原因（spec §4.4.5）。
	ErrChangeResumeModeRequired = New(http.StatusBadRequest, "resume_mode_required", "继续熔断 / 准备失败暂停必须指定 mode（retry_failed / skip_failed）与原因")
	// ErrChangeConfigGrayUnsupported M3 暂不支持含配置变更项的变更单启动（配置灰度 pin 与正式切版为 M4 交付，spec §4.6.2 / ADR-0071）。
	ErrChangeConfigGrayUnsupported = New(http.StatusConflict, "config_gray_unsupported", "本版暂不支持含配置变更项的变更单启动（配置灰度切版为后续交付）")

	// —— 交付数据面（FR-165，spec §4.5/§5.3，见 ADR-0069；code 沿用 agent 面小写下划线约定）——
	// ErrDeliveryBlobNotFound 中转 blob 不存在或未就绪（HEAD/GET 未命中，uploading 视同不存在）。
	ErrDeliveryBlobNotFound = New(http.StatusNotFound, "blob_not_found", "blob 不存在或未就绪")
	// ErrDeliveryBlobHashMismatch 上传内容实算 sha256 与 URL 声明不符，已丢弃（spec §4.5.2）。
	ErrDeliveryBlobHashMismatch = New(http.StatusUnprocessableEntity, "blob_hash_mismatch", "上传内容 sha256 与声明不符，已丢弃")
	// ErrDeliveryBlobForbidden 请求身份不持有该 blob 的传输授权（不属于引用它的活动 / 已审批变更单的源或目标，spec §5.3）。
	ErrDeliveryBlobForbidden = New(http.StatusForbidden, "blob_forbidden", "当前身份不持有该 blob 的传输授权")
	// ErrDeliveryLengthRequired 流式上传必须携带 Content-Length（spec §4.5.2）。
	ErrDeliveryLengthRequired = New(http.StatusLengthRequired, "length_required", "流式上传必须携带 Content-Length")
	// ErrDeliveryUploadBusy 上传并发已达上限（运维设置 delivery.upload-concurrency），agent 稍后重试（ADR-0069）。
	ErrDeliveryUploadBusy = New(http.StatusTooManyRequests, "delivery_upload_busy", "上传并发已达上限，请稍后重试")
	// ErrDeliveryDownloadBusy 下载并发已达上限（运维设置 delivery.download-concurrency），agent 稍后重试（ADR-0069）。
	ErrDeliveryDownloadBusy = New(http.StatusTooManyRequests, "delivery_download_busy", "下载并发已达上限，请稍后重试")
	// ErrDeliveryNotSource 仅本单模板源可执行该动作（拉上传清单 / 上传回执，spec §5.2）。
	ErrDeliveryNotSource = New(http.StatusForbidden, "not_source", "仅本单模板源可执行该操作")
	// ErrDeliveryNotTarget 仅本单目标服可执行该动作（拉差异清单 / 推送与生效回执，spec §5.2）。
	ErrDeliveryNotTarget = New(http.StatusForbidden, "not_target", "仅本单目标服可执行该操作")

	// ErrAssetNotFound 文件资产在该服最新清单中不存在（预览 / diff 目标缺失，FR-164，spec §5.2；code 对齐 devmock）。
	ErrAssetNotFound = New(http.StatusNotFound, "asset_not_found", "该服清单中不存在此文件")
	// ErrAssetAgentOffline 预览 / diff 目标 agent 离线，无法实时读取文件内容（FR-164，spec §4.5；code 对齐 devmock）。
	ErrAssetAgentOffline = New(http.StatusGatewayTimeout, "asset_agent_offline", "agent 离线，无法实时读取文件内容")
	// ErrAssetPreviewTimeout 预览 / diff 等待 agent 回传内容超时（FR-164，spec §4.5；code 对齐 devmock 同族）。
	ErrAssetPreviewTimeout = New(http.StatusGatewayTimeout, "asset_preview_timeout", "目标实例未在限期内返回文件内容")
	// ErrAssetReadFailed agent 读取目标文件失败（清单存在但现取失败，如已删除 / 越权，FR-164）。
	ErrAssetReadFailed = New(http.StatusBadGateway, "asset_read_failed", "目标文件读取失败")
	// ErrAssetSensitivePath 命中敏感路径规则且未填原因，禁止查看内容（FR-164，spec §4.6；code 对齐 devmock，响应体附 sensitive=true）。
	ErrAssetSensitivePath = New(http.StatusForbidden, "asset_sensitive_path", "命中敏感路径规则，查看内容必须填写原因")
	// ErrAssetDiffUnsupported diff 任一侧为二进制或超 512 KiB，不支持 diff（FR-164，spec §4.5；code 对齐 devmock）。
	ErrAssetDiffUnsupported = New(http.StatusBadRequest, "asset_diff_unsupported", "二进制或超过 512 KiB 的文件不支持 diff，请改用哈希比对")
	// ErrEnvNotFound env 展示维度不存在（改名 / 删除 / 设置映射目标缺失，FR-178）。
	ErrEnvNotFound = New(http.StatusNotFound, "ENV_NOT_FOUND", "env 不存在")
	// ErrEnvConflict 同名 env 已存在（创建 / 改名撞名，FR-178）。
	ErrEnvConflict = New(http.StatusConflict, "ENV_CONFLICT", "同名 env 已存在")
	// ErrEnvNamespaceConflict 待映射 namespace 已归属其他 env（一个 namespace 至多属一个 env，FR-178，spec §4.1）。
	// 具体冲突方（namespace 名 + 占用 env 名）经 service 用同码 apperr.New 携带在 message 中，让运维看清冲突方。
	ErrEnvNamespaceConflict = New(http.StatusConflict, "ENV_NAMESPACE_CONFLICT", "存在已归属其他 env 的 namespace")
	// ErrEnvNamespaceNotFound 待映射 namespace 不存在（整体替换映射时校验，FR-178）。
	ErrEnvNamespaceNotFound = New(http.StatusBadRequest, "ENV_NAMESPACE_NOT_FOUND", "待映射 namespace 不存在")
)

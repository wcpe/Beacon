package model

import "time"

// 跨服消息状态机取值（msg_trace.status，VARCHAR 落库 + 应用层校验；spec §4.2 状态机）。
const (
	// MsgStatusAccepted 控制面已受理、入目标服投递队列，待目标 agent 长轮询取走。
	MsgStatusAccepted = "accepted"
	// MsgStatusDispatched 目标 agent 已长轮询取走、携 payload 下发，待回执。
	MsgStatusDispatched = "dispatched"
	// MsgStatusDelivered 目标业务 handler 处理完并回执成功（终态）。
	MsgStatusDelivered = "delivered"
	// MsgStatusFailed 失败（终态）：目标不存在 / 跨域无信任 / 回执失败 / 重投用尽 / 玩家不在线。
	MsgStatusFailed = "failed"
	// MsgStatusExpired 过期（终态）：accepted 停留超 TTL 无人取走 / 队列溢出淘汰最旧。
	MsgStatusExpired = "expired"
)

// 消息寻址目标类型（msg_trace.target_kind，spec §3.3；broadcast 见 FR-180 / ADR-0065）。
const (
	MsgTargetKindServer = "server"
	MsgTargetKindPlayer = "player"
	// MsgTargetKindBroadcast 广播 fan-out：按当前在线服集合投递（恒发送者 namespace，可选 zone 级定向）。
	MsgTargetKindBroadcast = "broadcast"
)

// IsValidMsgTargetKind 校验目标类型取值（结构校验用）。
func IsValidMsgTargetKind(kind string) bool {
	return kind == MsgTargetKindServer || kind == MsgTargetKindPlayer
}

// 消息失败 / 过期原因码（msg_trace.fail_reason，脱敏后文案；spec §4.2/§7）。
const (
	MsgFailPlayerNotOnline  = "player_not_online"     // 按玩家寻址但玩家不在名册
	MsgFailNamespaceNoTrust = "namespace_not_trusted" // 跨域无 capability=message 信任
	MsgFailAckTimeout       = "ack_timeout"           // dispatched 后重投用尽仍无回执
	MsgFailQueueOverflow    = "queue_overflow"        // 每服投递队列溢出淘汰最旧
	MsgFailTTLExpired       = "ttl_expired"           // accepted 停留超 TTL 无人取走
	MsgFailHandlerError     = "handler_error"         // 目标业务 handler 回执失败
	MsgFailNoOnlineTarget   = "no_online_target"      // 广播解析出的在线目标集合为空（FR-180）
)

// MsgTrace 是消息元数据日表 msg_trace_YYYYMMDD 的行模型（FR-149，见 spec §3.3）。
//
// 每条跨服消息一行；message_id 为源 agent 发送时生成的 UUIDv7，其内嵌毫秒时间戳所在 UTC 日即物理表。
// 元数据与 payload 分表（msg_payload），二者同事务写入（payload_stored=true 时）。RPC 响应亦为一条消息，
// 经 correlation_id 关联请求。全部基础数值 / 字符串类型，枚举落 VARCHAR + 应用层校验，hops 落 TEXT（json
// 数组文本），禁 JSON/ENUM 列与方言专有 SQL（守 DB 可移植）。索引用 composite 空名式让 GORM 按当日表名自动命名。
type MsgTrace struct {
	// 源 agent 发送时生成的 UUIDv7（主键，据其内嵌时间定日表）
	MessageID string `gorm:"column:message_id;size:36;primaryKey"`
	// 来源 namespace（权威取自已鉴权身份）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:,composite:src_created,priority:1;index:,composite:resolved_created,priority:1"`
	// 来源 serverId
	SourceServerID string `gorm:"column:source_server_id;size:64;not null;index:,composite:src_created,priority:2"`
	// 业务消息类型（业务插件定义）
	MsgType string `gorm:"column:msg_type;size:64;not null"`
	// server / player
	TargetKind string `gorm:"column:target_kind;size:16;not null"`
	// 定向目标（target_kind=server），未用为空
	TargetServerID string `gorm:"column:target_server_id;size:64;not null;default:''"`
	// 按玩家寻址的玩家 UUID（target_kind=player），未用为空
	TargetPlayer string `gorm:"column:target_player;size:36;not null;default:''"`
	// 广播 zone 级定向的 zone 名（target_kind=broadcast 且指定 zone 时），其余为 NULL（FR-180）
	TargetZone *string `gorm:"column:target_zone;size:64"`
	// 广播 fan-out 目标数（仅广播行非空；一条广播只落一行，防 ×N 写放大，ADR-0065）
	FanoutTotal *int `gorm:"column:fanout_total"`
	// 广播送达计数（仅广播行非空）
	DeliveredCount *int `gorm:"column:delivered_count"`
	// 广播失败计数（仅广播行非空）
	FailedCount *int `gorm:"column:failed_count"`
	// 广播过期计数（仅广播行非空）
	ExpiredCount *int `gorm:"column:expired_count"`
	// 控制面据名册解析出的实际目标服，未解析为空
	ResolvedServerID string `gorm:"column:resolved_server_id;size:64;not null;default:'';index:,composite:resolved_created,priority:2"`
	// 跨域时的目标 namespace，同域为 NULL
	TargetNamespaceID *uint `gorm:"column:target_namespace_id"`
	// 是否跨 namespace（须有 namespace_trust capability=message 才放行）
	CrossNamespace bool `gorm:"column:cross_namespace;not null;default:false"`
	// RPC 关联：响应消息填请求的 message_id，未用为空
	CorrelationID string `gorm:"column:correlation_id;size:36;not null;default:'';index"`
	// accepted / dispatched / delivered / failed / expired
	Status string `gorm:"column:status;size:16;not null;index:,composite:status_created,priority:1"`
	// 失败 / 过期原因（脱敏后文案），成功为空
	FailReason string `gorm:"column:fail_reason;size:255;not null;default:''"`
	// 控制面接收时间（UTC）；进复合索引第 3 位支撑按来源 / 解析目标 / 状态的时间过滤
	CreatedAt time.Time `gorm:"column:created_at;not null;index:,composite:src_created,priority:3;index:,composite:resolved_created,priority:3;index:,composite:status_created,priority:2"`
	// 被目标 agent 长轮询取走时间，未取走为 NULL
	DispatchedAt *time.Time `gorm:"column:dispatched_at"`
	// 目标 agent 回执的送达时间，未送达为 NULL
	DeliveredAt *time.Time `gorm:"column:delivered_at"`
	// created_at → delivered_at 全链路耗时（毫秒），未送达为 NULL
	DurationMs *int64 `gorm:"column:duration_ms"`
	// 中转跳数 = 链路中承担转发职责的节点数（经控制面单跳中转恒为 1，由 hops 实算）
	HopCount int `gorm:"column:hop_count;not null;default:0"`
	// 链路事件 json 数组文本（落 TEXT 不用 JSON 列，守可移植）
	Hops string `gorm:"column:hops;type:text"`
	// payload 字节数
	PayloadSize int `gorm:"column:payload_size;not null;default:0"`
	// payload 是否已落 msg_payload 表
	PayloadStored bool `gorm:"column:payload_stored;not null;default:false"`
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
func (MsgTrace) TableName() string { return "msg_trace" }

// MsgPayload 是消息 payload 日表 msg_payload_YYYYMMDD 的行模型（FR-150，见 spec §3.4）。
//
// 与同日 msg_trace 一一对应（payload_stored=true 时存在），同一 DB 事务内写入两表。分表分离使元数据查询
// 永不触碰 payload 数据页、payload 可采用不同保留期。payload 明文落 TEXT，永不写日志 / 审计 / 列表接口。
type MsgPayload struct {
	// 同 msg_trace.message_id（主键，据其内嵌时间定日表）
	MessageID string `gorm:"column:message_id;size:36;primaryKey"`
	// 原文（业务序列化后的字符串）
	Payload string `gorm:"column:payload;type:text"`
	// payload 摘要（供归档校验与完整性核对）
	SHA256 string `gorm:"column:sha256;size:64;not null"`
	// 字节数
	Size int `gorm:"column:size;not null;default:0"`
	// 写入时间（UTC）
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
func (MsgPayload) TableName() string { return "msg_payload" }

// MessageRecord 是一条消息到达终态时经异步写入通道流动的合并记录（非 GORM 表）：
// 承载 msg_trace 元数据行与可选 msg_payload，供仓库在同一 DB 事务内写两表（spec §3.4 分表同事务）。
// 消息生命周期中的可变状态（accepted→dispatched→终态）由控制面内存中转维护、只在终态一次性落库，
// 故本记录无更新语义、message_id 冲突即忽略（幂等）。
type MessageRecord struct {
	Trace   MsgTrace
	Payload *MsgPayload // payload_stored=false 时为 nil
}

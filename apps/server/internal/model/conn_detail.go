package model

import "time"

// 连接会话状态（conn_detail.status，VARCHAR 落库 + 应用层校验，守可移植；spec §3.2）。
const (
	// ConnStatusOpen 连接已建立、未断开。
	ConnStatusOpen = "open"
	// ConnStatusClosed 连接已断开（close 事件更新同一会话行）。
	ConnStatusClosed = "closed"
)

// 连接断开分类（conn_detail.close_kind，spec §3.2）。
const (
	ConnCloseKindQuit          = "quit"           // 玩家主动退出
	ConnCloseKindKick          = "kick"           // 被踢
	ConnCloseKindTimeout       = "timeout"        // 超时断开
	ConnCloseKindProxyShutdown = "proxy_shutdown" // proxy 宕机 / 重启导致的孤儿会话补 close
	ConnCloseKindError         = "error"          // 异常断开
)

// 连接采集事件类型（agent 批量上报的 kind，spec §4.1；仅 open / close 两种，无逐次换服流水）。
const (
	ConnEventKindOpen  = "open"
	ConnEventKindClose = "close"
)

// IsValidConnEventKind 校验采集事件类型取值（结构校验用）。
func IsValidConnEventKind(kind string) bool {
	return kind == ConnEventKindOpen || kind == ConnEventKindClose
}

// IsValidConnCloseKind 校验断开分类取值（空串视为未提供，合法）。
func IsValidConnCloseKind(kind string) bool {
	switch kind {
	case "", ConnCloseKindQuit, ConnCloseKindKick, ConnCloseKindTimeout,
		ConnCloseKindProxyShutdown, ConnCloseKindError:
		return true
	default:
		return false
	}
}

// ConnDetail 是连接明细日表 conn_detail_YYYYMMDD 的行模型（会话行，FR-145，见 spec §3.2）。
//
// 每条玩家连接一行：open 事件插入行，close 事件更新同一行（按 conn_id 定位）。conn_id 为 proxy agent
// 在 open 时生成的 UUIDv7，其内嵌毫秒时间戳所在 UTC 日即本行物理表——跨日长连接始终落 open 日表。
//
// 全部基础数值 / 字符串类型，禁 JSON/ENUM 列与方言专有 SQL（守 DB 可移植，可切 Postgres）；
// 枚举 status / close_kind 落 VARCHAR + 应用层校验。索引用 composite 空名式让 GORM 按「当日表名」
// 自动生成索引名——避免同字面索引名在 sqlite（索引名全库唯一）跨日建表冲突（与 MetricSampleV2 同口径）。
type ConnDetail struct {
	// proxy agent 在 open 时生成的 UUIDv7（主键，据其内嵌时间定日表）
	ConnID string `gorm:"column:conn_id;size:36;primaryKey"`
	// 所属 namespace（权威取自已鉴权身份，非请求体自报）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:,composite:ns_proxy_opened,priority:1"`
	// 采集方 proxy 的 serverId
	ProxyServerID string `gorm:"column:proxy_server_id;size:64;not null;index:,composite:ns_proxy_opened,priority:2"`
	// 玩家 UUID
	PlayerUUID string `gorm:"column:player_uuid;size:36;not null;index:,composite:player_opened,priority:1"`
	// 登录时玩家名
	PlayerName string `gorm:"column:player_name;size:16;not null"`
	// 客户端地址（IPv4/IPv6）
	ClientIP string `gorm:"column:client_ip;size:45;not null"`
	// MC 协议号
	ProtocolVersion int `gorm:"column:protocol_version;not null;default:0"`
	// 连接建立时间（UTC）；进复合索引第 3 位支撑按 proxy / 玩家 / 状态的时间过滤
	OpenedAt time.Time `gorm:"column:opened_at;not null;index:,composite:ns_proxy_opened,priority:3;index:,composite:player_opened,priority:2;index:,composite:status_opened,priority:2"`
	// 断开时间（UTC），未断开为 NULL
	ClosedAt *time.Time `gorm:"column:closed_at"`
	// close 时计算的会话时长（毫秒），未断开为 NULL
	DurationMs *int64 `gorm:"column:duration_ms"`
	// open / closed（应用层校验）；进复合索引第 1 位支撑对账遗留 open 会话
	Status string `gorm:"column:status;size:16;not null;index:,composite:status_opened,priority:1"`
	// 断开分类（quit/kick/timeout/proxy_shutdown/error），未断开为空
	CloseKind string `gorm:"column:close_kind;size:32;not null;default:''"`
	// 断开原文（超长截断），未断开为空
	CloseReason string `gorm:"column:close_reason;size:255;not null;default:''"`
	// 首个后端子服
	FirstBackendServerID string `gorm:"column:first_backend_server_id;size:64;not null;default:''"`
	// 断开时所在后端子服
	LastBackendServerID string `gorm:"column:last_backend_server_id;size:64;not null;default:''"`
	// 会话内后端切换次数（摘要，不记逐次流水）
	BackendSwitchCount int `gorm:"column:backend_switch_count;not null;default:0"`
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
// 本模型不进 AutoMigrate——日表按 UTC 日期后缀由 store.EnsureDailyTable 按需建。
func (ConnDetail) TableName() string { return "conn_detail" }

// ConnEvent 是一条连接采集事件（open 或 close）在异步写入通道中流动的值形态（非 GORM 表）。
//
// agent 单批上报混合 open / close 事件，控制面异步批量落 conn_detail 日表：open → 插入会话行、
// close → 按 conn_id 更新同一行（见 spec §4.1）。时间以毫秒携带（OpenedAtMs / ClosedAtMs），
// 由仓库映射为 DATETIME(3) 列，避免流转层耦合 time.Time 时区。
type ConnEvent struct {
	Kind               string // open / close
	ConnID             string // UUIDv7，据其内嵌时间定日表
	NamespaceID        uint   // 权威 namespace（鉴权身份注入）
	ProxyServerID      string // 采集方 proxy serverId（权威身份）
	PlayerUUID         string
	PlayerName         string
	ClientIP           string
	ProtocolVersion    int
	OpenedAtMs         int64  // 连接建立时间（毫秒 UTC）
	ClosedAtMs         int64  // 断开时间（毫秒 UTC），open 事件为 0
	CloseKind          string // 仅 close
	CloseReason        string // 仅 close
	FirstBackend       string
	LastBackend        string
	BackendSwitchCount int
}

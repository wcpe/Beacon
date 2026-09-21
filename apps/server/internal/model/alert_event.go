package model

import "time"

// AlertEvent 是告警事件的持久化留痕（FR-89，见 ADR-0041）：与 FR-28 既有告警通道（webhook/站内信）并存，
// 通道职责是「往外推 / 进程内即时」，本表职责是「留痕 + UI 信息流」，跨控制面重启可查回。
// 区别于 audit_log（人对平台做了什么）：本表记系统健康事件（实例状态异常转移等），二者维度正交、不混表。
//
// 全部基础类型，禁 JSON/ENUM 列与方言专有 SQL，经 GORM 抽象（守 DB 可移植，可切 Postgres）：
// type/level 落 VARCHAR + 应用层校验，detail 落 TEXT，自增主键抽象，created_at 由全局 NowFunc 落 UTC。
type AlertEvent struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 事件类型（health-transition 等，落 VARCHAR + 应用层校验）；与 created_at 组成复合索引支撑按类型 + 时间过滤
	Type string `gorm:"column:type;size:32;not null;index:idx_alert_event_type,priority:1;index:idx_alert_event_dedup,priority:4"`
	// 严重级别（info/warning/critical，落 VARCHAR + 应用层校验）
	Level string `gorm:"column:level;size:16;not null;index:idx_alert_event_level"`
	// 涉及实例 serverId（可空，如非实例维度的事件）
	ServerID string `gorm:"column:server_id;size:128;index:idx_alert_event_dedup,priority:1"`
	// 涉及环境编码（可空，如全局事件）
	Namespace string `gorm:"column:namespace;size:64;index:idx_alert_event_namespace;index:idx_alert_event_dedup,priority:2"`
	// 人读摘要文案（如「lobby-1 online → lost」）
	Message string `gorm:"column:message;size:512;not null"`
	// 结构化详情（json 文本，含状态前后 / 地址等上下文）
	Detail string `gorm:"column:detail;type:text"`
	// 发生时间（UTC）；支撑按时间倒序与时间窗过滤
	CreatedAt time.Time `gorm:"index:idx_alert_event_time;index:idx_alert_event_type,priority:2"`

	// 以下为处理工作流字段（FR-157，见 ADR-0064）：把 append-only 历史升级为可确认 / 可处置。
	// 全部落 VARCHAR + 应用层校验，禁 ENUM，AutoMigrate 加列对既有行用 NOT NULL DEFAULT 兼容（守 DB 可移植）。
	// 处理状态（open 待处理 / acknowledged 已确认 / resolved 已处理）；新告警插入即 open（由 AlertEventService.Record 设默认）。
	// 加列 DEFAULT '' 让存量历史行落空串，供启动迁移回填 resolved（区别于应用层显式写 open 的新行）。
	Status string `gorm:"column:status;size:16;not null;default:'';index:idx_alert_event_status"`
	// 处理人（管理台登录身份）；未处理为空串
	HandledBy string `gorm:"column:handled_by;size:128;not null;default:''"`
	// 处理时刻（UTC）；未处理为 NULL（故用指针）
	HandledAt *time.Time `gorm:"column:handled_at"`
	// 处理说明（运维填写的确认 / 处置原因）；未处理为空串
	HandleNote string `gorm:"column:handle_note;size:512;not null;default:''"`

	// 以下为收敛 / 防堆积字段（FR-232）：同类未恢复告警只保留 1 行并计数，避免抖动场景堆上千条。
	// ToStatus 是该告警指向的目标状态（health-transition 的 to 态），作为收敛键的方向维度，避免 lost/offline 并成一行丢方向。
	ToStatus string `gorm:"column:to_status;size:32;not null;default:'';index:idx_alert_event_dedup,priority:3"`
	// OccurrenceCount 是合并计数：未恢复期间重复触发只递增本值，不插新行（首发为 1）。
	OccurrenceCount int `gorm:"column:occurrence_count;not null;default:1"`
	// LastAt 是最近一次触发时刻（首发等于 CreatedAt）；列表据此展示「最后 14:32」。
	LastAt *time.Time `gorm:"column:last_at"`

	// 以下为人工分级覆盖字段（FR-231）：非空表示运维手动改过级别，列表 / 排序以 override 为准并标「已手动调整」。
	// SeverityOverride 非空时即以它为准（覆盖矩阵自动定级）；OverriddenBy/At 记录改级人与时刻（审计另存一份）。
	SeverityOverride string     `gorm:"column:severity_override;size:16;not null;default:''"`
	OverriddenBy     string     `gorm:"column:overridden_by;size:128;not null;default:''"`
	OverriddenAt     *time.Time `gorm:"column:overridden_at"`
}

// TableName 固定表名为 alert_event。
func (AlertEvent) TableName() string { return "alert_event" }

package model

import "time"

// AgentSetting 是控制面下发到 agent 的运行时配置项（FR-277）。
//
// 三层优先级由 **agent 侧**裁决：Web 下发缓存 > 环境变量 > config.yml > 代码默认。
// 本表只是「Web 下发」这一层的真源，控制面不参与优先级计算——agent 断连时继续用本地
// pushed-config.json 缓存，所以本表暂时不可达不会让 agent 退回默认。
//
// 两级作用域（server_id 为空即 namespace 级默认）：
//   - namespace 级：该环境全部 agent 共用的默认值
//   - server 级：单台服的覆盖值，优先于 namespace 级
//
// 为什么不复用 setting 表：那张表的 key 是**控制面自身**的热改白名单（FR-61，ADR-0038），
// 语义、校验、消费方全不同；混在一张表里会让「谁能下发」和「控制面自己怎么跑」两套
// 安全边界互相渗透，故独立建表。
type AgentSetting struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属环境主键；与 server_id + setting_key 组成唯一键
	NamespaceID uint `gorm:"column:namespace_id;not null;uniqueIndex:uk_agent_setting_scope,priority:1;index:idx_agent_setting_namespace"`
	// 目标 serverId。**空字符串表示 namespace 级默认**，非 NULL——唯一索引对 NULL 不去重，
	// 用 NULL 会让同一作用域同一键重复插入而不报冲突。列长度与 server.server_id 对齐（64）。
	ServerID string `gorm:"column:server_id;size:64;not null;default:'';uniqueIndex:uk_agent_setting_scope,priority:2"`
	// 配置键（与 agent 侧 config.yml 的键名逐字一致，如 delivery.config-reload-timeout-ms）。
	// 列名用 setting_key 而非 key——key 是 MySQL 保留字，裸 `WHERE key = ?` 在 MySQL 报 1064
	// 语法错（sqlite 容忍），与 setting 表同一处置（见 setting.go）。
	Key string `gorm:"column:setting_key;size:128;not null;uniqueIndex:uk_agent_setting_scope,priority:3"`
	// 字符串化的配置值。**一律以文本承载**：agent 侧从下发缓存读到的就是字符串，
	// 类型转换与非法值兜底（回退本地配置 + 告警）在 agent 侧已实现（CascadingConfigReader）。
	// 空串在 agent 侧等价于「未下发」（读缓存时 isNotBlank 过滤），故控制面不接受空值写入、
	// 撤销下发请删除该行。
	Value string `gorm:"column:value;size:4096;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）；agent 拉取端点用它推导「这份配置有多新」
	UpdatedAt time.Time
	// 最后修改人（管理台身份 / API 密钥标识）；仅作展示与追责冗余，权威记录在 audit_log
	UpdatedBy string `gorm:"column:updated_by;size:128;not null;default:''"`
}

// TableName 固定表名为 agent_setting。
func (AgentSetting) TableName() string { return "agent_setting" }

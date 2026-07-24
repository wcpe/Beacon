package model

import "time"

// 设置值类型（落 VARCHAR + 应用层反序列化提示，不绑方言；FR-61，见 ADR-0038）。
// 设置以字符串化值 + value_type 存单表，类型约束由应用层校验承担（DB 可移植）。
const (
	SettingValueTypeInt    = "int"    // 整数（秒 / 毫秒 / 字节等）
	SettingValueTypeBool   = "bool"   // 布尔（"true" / "false"）
	SettingValueTypeString = "string" // 字符串（URL / 枚举如 log.level）
)

// Setting 是单条运维设置项（FR-61，见 ADR-0038）：热改项真源由 config.yml 移到 DB store。
// 单 key-value 表（不按域分多表）：运维旋钮零散、增删频繁，单表 CRUD 最省；类型约束由 value_type + 应用层校验承担。
// 无软删（设置是固定白名单 upsert，不删）。
type Setting struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 设置键（白名单内的热改 key，如 health.ttl-sec / log.level）；唯一。
	// 列名用 setting_key 而非 key——key 是 MySQL 保留字，裸 `WHERE key = ?` 在 MySQL 报 1064 语法错（sqlite 容忍），避之。
	Key string `gorm:"column:setting_key;size:128;not null;uniqueIndex:uk_setting_key"`
	// 字符串化的设置值（int/bool/string 统一以文本存，应用层按 value_type 反序列化）
	Value string `gorm:"column:value;size:1024;not null"`
	// 值类型提示：int / bool / string（落 VARCHAR + 应用层校验，不用 ENUM）
	ValueType string `gorm:"column:value_type;size:16;not null"`
	// 乐观锁版本：Upsert 时 CAS version+1，防并发覆盖
	Version int `gorm:"column:version;not null;default:0"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）；每次 Update 刷新
	UpdatedAt time.Time
}

// TableName 固定表名为 setting。
func (Setting) TableName() string { return "setting" }

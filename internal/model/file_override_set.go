package model

import (
	"crypto/md5"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// FileOverrideSet 是三方插件文件覆盖兼容（FR-15）的一组覆盖目标：在文件树托管（通道B）之上，
// 额外承载"目标插件根目录 + 一条受限重载命令"这一事实（见 ADR-0011）。
// 它复用 scope 五元组（namespace/group/scope_level/scope_target）定位覆盖链的一格，
// 成员文件由 file_object.override_set_id 关联（成员仍走通道B 的整文件覆盖语义）。
//
// 控制面只存"目标文件态 + 重载方式"这一事实，不编排、不决策；命令由 agent 本地受限白名单派发。
// mode 固定 file-override（VARCHAR，不开放 jar 替换 / 进程重启等 P3 能力）；
// target_root 与 reload_command 落 VARCHAR（禁 ENUM/JSON 列，保 GORM 可移植）。
type FileOverrideSet struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码（三元组 environment）
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_override_identity,priority:1;index:idx_override_lookup,priority:1"`
	// 大区编码（global 层用占位 __GLOBAL__）
	GroupCode string `gorm:"column:group_code;size:64;not null;uniqueIndex:uk_override_identity,priority:2;index:idx_override_lookup,priority:2"`
	// 覆盖集名称（同 scope 内唯一标识，如目标插件名）
	Name string `gorm:"column:name;size:128;not null"`
	// name 的 md5（小写 hex），仅用于唯一键定长化（避免 utf8mb4 复合唯一键超 MySQL 键长上限）
	NameHash string `gorm:"column:name_hash;size:32;not null;uniqueIndex:uk_override_identity,priority:3"`
	// 覆盖层：global/group/zone/server
	ScopeLevel string `gorm:"column:scope_level;size:16;not null;uniqueIndex:uk_override_identity,priority:4"`
	// 该层目标键：global/group='' ；zone=zone编码；server=serverId
	ScopeTarget string `gorm:"column:scope_target;size:128;not null;default:'';uniqueIndex:uk_override_identity,priority:5"`
	// 目标插件根目录（相对 plugins，如 plugins/AllinCore），落盘根；早校验限定 plugins/<plugin>/ 内
	TargetRoot string `gorm:"column:target_root;size:512;not null"`
	// 一条受限重载命令（Bukkit/Bungee 控制台命令，单条、无元字符）；可空表示不下发命令
	ReloadCommand string `gorm:"column:reload_command;size:512;not null;default:''"`
	// 模式固定 file-override（VARCHAR + 应用层校验，见 ADR-0011 决策 2）
	Mode string `gorm:"column:mode;size:32;not null;default:'file-override'"`
	// 指向 file_override_set_revision.id；0=尚未发布
	CurrentRevision uint `gorm:"column:current_revision;not null;default:0"`
	// 单调递增发布序号（每发布 +1，回滚也 +1）
	Version int64 `gorm:"column:version;not null;default:0"`
	// 是否启用（下线则成员文件不再参与该插件覆盖）
	Enabled bool `gorm:"column:enabled;not null;default:true"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值（见 SoftDeleteSentinel），纳入唯一键允许同标识重建
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_override_identity,priority:6"`
}

// TableName 固定表名为 file_override_set。
func (FileOverrideSet) TableName() string { return "file_override_set" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（非 NULL，使唯一键生效）。
func (s *FileOverrideSet) BeforeCreate(*gorm.DB) error {
	if s.DeletedAt.IsZero() {
		s.DeletedAt = SoftDeleteSentinel
	}
	return nil
}

// BeforeSave 在写入前由 name 计算 name_hash（唯一键用，与 name 一一对应；非安全用途）。
func (s *FileOverrideSet) BeforeSave(*gorm.DB) error {
	sum := md5.Sum([]byte(s.Name))
	s.NameHash = hex.EncodeToString(sum[:])
	return nil
}

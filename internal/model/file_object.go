package model

import (
	"crypto/md5"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// FileObject 是文件树托管（通道B）的一条"整文件 blob"：三元组 + scope 维度 + 相对 path 定位它在覆盖链的一格，
// 与配置中心（通道A，ConfigItem）平行但语义不同——文件按 path 整文件覆盖，不做键级深合并（见 ADR-0010）。
// content/content_md5 冗余在行上供 manifest 与下发热路径直读，current_revision/version 为当前版本指针。
type FileObject struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码（三元组 environment）
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_file_identity,priority:1;index:idx_file_lookup,priority:1;index:idx_file_scope,priority:1"`
	// 大区编码（global 层用占位 __GLOBAL__）
	GroupCode string `gorm:"column:group_code;size:64;not null;uniqueIndex:uk_file_identity,priority:2;index:idx_file_lookup,priority:2;index:idx_file_scope,priority:2"`
	// 文件相对 path（如 ui-components/main.allin）
	Path string `gorm:"column:path;size:512;not null;index:idx_file_lookup,priority:3"`
	// path 的 md5（小写 hex），仅用于唯一键：path varchar(512) 直接入 utf8mb4 复合唯一键会超 MySQL 3072 字节键长上限，
	// 故唯一性改由定长 path_hash 承担；path 本身保留用于查询与展示（path↔path_hash 一一对应，由 BeforeSave 计算）。
	PathHash string `gorm:"column:path_hash;size:32;not null;uniqueIndex:uk_file_identity,priority:3"`
	// 覆盖层：global/group/zone/server
	ScopeLevel string `gorm:"column:scope_level;size:16;not null;uniqueIndex:uk_file_identity,priority:4;index:idx_file_scope,priority:3"`
	// 该层目标键：global/group='' ；zone=zone编码；server=serverId
	ScopeTarget string `gorm:"column:scope_target;size:128;not null;default:'';uniqueIndex:uk_file_identity,priority:5;index:idx_file_scope,priority:4"`
	// 当前生效内容（整文件文本，落 TEXT，由 GORM size 抽象不绑方言）
	Content string `gorm:"column:content;size:1048576;not null"`
	// 当前内容 md5（小写 hex）
	ContentMD5 string `gorm:"column:content_md5;size:32;not null"`
	// 指向 file_revision.id；0=尚未发布
	CurrentRevision uint `gorm:"column:current_revision;not null;default:0"`
	// 单调递增发布序号（每发布 +1，回滚也 +1）
	Version int64 `gorm:"column:version;not null;default:0"`
	// 是否参与有效文件树解析
	Enabled bool `gorm:"column:enabled;not null;default:true"`
	// 整文件覆盖豁免（FR-44，[ADR-0029]）：true 则该文件即便是结构化后缀也不深合并、走整文件覆盖
	// （保注释、不重渲染）；默认 false = 结构化文件跨层深合并、非结构化整文件覆盖兜底。
	WholeFileOverride bool `gorm:"column:whole_file_override;not null;default:false"`
	// 所属覆盖集（FR-15）：>0 表示该文件是某 file_override_set 的成员（按 path 整文件覆盖三方插件目录）；
	// 0 表示普通托管文件（通道B 默认）。成员文件仍走通道B 的整文件覆盖与同步语义，仅多一层归属标记。
	OverrideSetID uint `gorm:"column:override_set_id;not null;default:0;index:idx_file_override_set"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值（见 SoftDeleteSentinel），纳入唯一键允许同标识重建
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_file_identity,priority:6"`
}

// TableName 固定表名为 file_object。
func (FileObject) TableName() string { return "file_object" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（非 NULL，使唯一键生效）。
func (f *FileObject) BeforeCreate(*gorm.DB) error {
	if f.DeletedAt.IsZero() {
		f.DeletedAt = SoftDeleteSentinel
	}
	return nil
}

// BeforeSave 在写入前由 path 计算 path_hash（唯一键用，与 path 一一对应；非安全用途）。
func (f *FileObject) BeforeSave(*gorm.DB) error {
	sum := md5.Sum([]byte(f.Path))
	f.PathHash = hex.EncodeToString(sum[:])
	return nil
}

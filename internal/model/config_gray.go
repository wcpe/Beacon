package model

import (
	"time"

	"gorm.io/gorm"
)

// ConfigGray 是某 config_item 的一条"灰度 / Beta"版本（FR-9，见 ADR-0021）。
// 灰度作用在"版本选择"层：cohort 名单内的 server 解析到本灰度 content，名单外解析到稳定版本，
// 与 scope 覆盖链正交叠加（不新增覆盖层、不改 merge 纯函数）。
// 一个未软删灰度唯一对应一个 config_item；promote 把内容并入稳定版本后软删本灰度，abort 直接软删。
type ConfigGray struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 关联 config_item.id（一个 item 至多一个未软删灰度）
	ConfigItemID uint `gorm:"column:config_item_id;not null;uniqueIndex:uk_gray_item,priority:1;index:idx_gray_item"`
	// 环境编码（供按 ns 批量取活跃灰度，避免逐项查 N+1）
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;index:idx_gray_ns"`
	// 内容格式：yaml/properties/json（与所属 config_item 一致）
	Format string `gorm:"column:format;size:16;not null;default:'yaml'"`
	// 灰度内容（敏感项加密入库，读出解密，与 config_item 同构，FR-20）
	Content string `gorm:"column:content;size:262144;not null"`
	// 灰度内容 md5（小写 hex，按明文算）
	ContentMD5 string `gorm:"column:content_md5;size:32;not null"`
	// cohort：目标 serverId 名单（JSON 数组文本，落 TEXT，可移植可读）
	Cohort string `gorm:"column:cohort;type:text;not null"`
	// 是否敏感项：与所属 config_item.sensitive 镜像，为真则 content 加密入库（FR-20）
	Sensitive bool `gorm:"column:sensitive;not null;default:false"`
	// 发布说明
	Comment string `gorm:"column:comment;size:512"`
	// 发布人
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，纳入唯一键允许同 item promote/abort 后再次灰度
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_gray_item,priority:2"`
}

// TableName 固定表名为 config_gray。
func (ConfigGray) TableName() string { return "config_gray" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（非 NULL，使唯一键生效）。
func (g *ConfigGray) BeforeCreate(*gorm.DB) error {
	if g.DeletedAt.IsZero() {
		g.DeletedAt = SoftDeleteSentinel
	}
	return nil
}

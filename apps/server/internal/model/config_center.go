package model

import "time"

// ConfigFile 是配置中心 V2 的配置文件（FR-160/161，规格 v2-config-center.md §3.2）。
// 以「文件」为一等公民组织配置；namespace 内逻辑名唯一（应用层校验，仅对未删除文件生效）。
// 软删用标准可空 deleted_at（非空 = 已入回收站），不沿用 legacy 哨兵范式。
type ConfigFile struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属 namespace，创建后不可改（跨 namespace 强隔离落点，§4.8）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:idx_config_file_ns_name,priority:1"`
	// namespace 内唯一逻辑名（建议目标相对路径形式，如 plugins/Foo/config.yml）
	Name string `gorm:"column:name;size:255;not null;index:idx_config_file_ns_name,priority:2"`
	// 内容格式：yaml / json / properties（应用层校验，创建后不可改）
	Format string `gorm:"column:format;size:16;not null"`
	// 用途说明
	Description string `gorm:"column:description;size:512"`
	// JSON Schema 文本（Draft 2020-12 子集，§4.4）；空串 = 不做 schema 校验
	SchemaJSON string `gorm:"column:schema_json;type:text"`
	// 敏感键路径列表（json 数组文本，§4.7）；空串 = 无敏感路径
	SensitivePaths string `gorm:"column:sensitive_paths;type:text"`
	// 移入回收站时间（UTC）；NULL = 未删除（§4.9）
	DeletedAt *time.Time `gorm:"column:deleted_at"`
	// 移入回收站的操作者；恢复时与 deleted_at 一并清空
	DeletedBy string `gorm:"column:deleted_by;size:64"`
	// 创建者
	CreatedBy string `gorm:"column:created_by;size:64"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 config_file。
func (ConfigFile) TableName() string { return "config_file" }

// ConfigLayerVersion 是配置文件某作用域链上的一个不可变版本（规格 §3.3）。
// 每个「文件 × 作用域」组合是一条独立追加链：只 INSERT、永不改删单行（purge 连带删除除外）；
// 链内 version_no 最大的一行即当前 head，head 为 is_removal=true 时该层视为无贡献。
type ConfigLayerVersion struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属配置文件 id
	ConfigFileID uint `gorm:"column:config_file_id;not null;uniqueIndex:uk_config_layer_version,priority:1"`
	// 作用域层：namespace / bc_cluster / region / zone / server（应用层校验）
	ScopeLevel string `gorm:"column:scope_level;size:16;not null;uniqueIndex:uk_config_layer_version,priority:2"`
	// 对应层实体 id（namespace 层 = namespace.id，server 层 = server.id，余类推）
	ScopeRefID uint `gorm:"column:scope_ref_id;not null;uniqueIndex:uk_config_layer_version,priority:3"`
	// 链内版本号，从 1 单调递增；唯一索引兜底并发插入
	VersionNo int `gorm:"column:version_no;not null;uniqueIndex:uk_config_layer_version,priority:4"`
	// 归一化后的配置内容（§4.2）；撤销版本为空串。size 走 GORM 抽象映射大文本列（上限 1 MiB）
	Content string `gorm:"column:content;size:1048576"`
	// 归一化内容的 sha256 小写 hex
	ContentHash string `gorm:"column:content_hash;size:64"`
	// true = 该版本表示「此层撤销贡献」
	IsRemoval bool `gorm:"column:is_removal;not null;default:false"`
	// 编辑基线 / 回退来源版本 id（可空）
	BasedOnVersionID *uint `gorm:"column:based_on_version_id"`
	// 本次变更说明
	Remark string `gorm:"column:remark;size:255"`
	// 创建者
	CreatedBy string `gorm:"column:created_by;size:64"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 config_layer_version。
func (ConfigLayerVersion) TableName() string { return "config_layer_version" }

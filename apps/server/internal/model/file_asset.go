package model

import "time"

// FileAsset 是文件资产索引的一行（FR-163/164，规格 v2-file-assets.md §3.1）：
// 一行 = 一台服务器上的一个文件，**只存最新快照**（无历史版本），由 agent 周期扫描后增量 upsert 维护。
//
// 全部基础数值 / 字符串类型，定长哈希列走 GORM size 抽象（不写方言 CHAR / JSON / ENUM），守 DB 可移植（可切 Postgres）。
type FileAsset struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属 namespace（冗余自 server）：隔离过滤与各查询索引的必需前缀列
	NamespaceID uint `gorm:"column:namespace_id;not null;index:idx_file_asset_ns_sha256,priority:1;index:idx_file_asset_ns_ext,priority:1;index:idx_file_asset_ns_path,priority:1"`
	// 归属服务器（引用 server.id，不建 DB 级外键）
	ServerID uint `gorm:"column:server_id;not null;uniqueIndex:uk_file_asset_server_path,priority:1"`
	// 相对服务器工作目录的规范化相对路径，`/` 分隔（如 plugins/Foo/config.yml）
	Path string `gorm:"column:path;size:512;not null;uniqueIndex:uk_file_asset_server_path,priority:2;index:idx_file_asset_ns_path,priority:2"`
	// 小写扩展名（无扩展名为空串）：冗余列，支撑扩展名维度搜索
	Ext string `gorm:"column:ext;size:16;not null;index:idx_file_asset_ns_ext,priority:2"`
	// 文件内容 sha256 小写 hex（定长 64）
	SHA256 string `gorm:"column:sha256;size:64;not null;index:idx_file_asset_ns_sha256,priority:2"`
	// 文件字节数
	Size int64 `gorm:"column:size;not null;default:0"`
	// 文件修改时间，UTC epoch 毫秒（存整数而非 DATETIME，避免方言时区差异）
	MtimeMs int64 `gorm:"column:mtime_ms;not null;default:0"`
	// 扫描期按扩展名启发的文本提示（权威的二进制判定在预览期由 agent 做）
	IsText bool `gorm:"column:is_text;not null;default:false"`
	// 本行来自哪次扫描（UTC）
	ScannedAt time.Time `gorm:"column:scanned_at;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 file_asset。
func (FileAsset) TableName() string { return "file_asset" }

// FileAssetScan 是每服扫描概要（规格 §3.2）：一行 = 一台服务器，随清单上报整体刷新。
// manifest_digest 是增量上报的校准锚点（算法见规格 §4.3），失配即退全量。
type FileAssetScan struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属 namespace（冗余自 server）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:idx_file_asset_scan_ns"`
	// 归属服务器（引用 server.id）：一服一行
	ServerID uint `gorm:"column:server_id;not null;uniqueIndex:uk_file_asset_scan_server"`
	// 当前清单摘要（sha256 小写 hex），增量上报的基线校准锚点
	ManifestDigest string `gorm:"column:manifest_digest;size:64;not null"`
	// 清单文件数
	FileCount int `gorm:"column:file_count;not null;default:0"`
	// 清单总字节数
	TotalSize int64 `gorm:"column:total_size;not null;default:0"`
	// agent 侧超单服文件数上限被截断（页面据此明示「清单不完整」）
	Truncated bool `gorm:"column:truncated;not null;default:false"`
	// 最近一次扫描完成时间（UTC）
	ScannedAt time.Time `gorm:"column:scanned_at;not null"`
	// 最近一次扫描耗时（毫秒）
	ScanDurationMs int `gorm:"column:scan_duration_ms;not null;default:0"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 file_asset_scan。
func (FileAssetScan) TableName() string { return "file_asset_scan" }

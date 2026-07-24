package model

import "time"

// DeliveryBlob 是交付数据面的中转 blob 元数据（FR-165，规格 v2-delivery-orchestration.md §3.5）。
// 内容寻址：sha256 即主身份（本表无自增 id），同 sha256 天然去重——多个变更单、多个文件路径共享同一 blob。
// 磁盘布局 <data-dir>/delivery/blobs/<sha256 前 2 位>/<sha256>；本表只存元数据、不存内容。
type DeliveryBlob struct {
	// 内容 sha256 小写 hex，内容寻址主身份（主键，天然唯一）
	SHA256 string `gorm:"column:sha256;size:64;primaryKey"`
	// 字节数
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0"`
	// 就绪度：uploading / ready
	State string `gorm:"column:state;size:16;not null"`
	// 最近被活动变更单引用时间（清理依据，UTC）
	LastReferencedAt time.Time `gorm:"column:last_referenced_at;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 delivery_blob。
func (DeliveryBlob) TableName() string { return "delivery_blob" }

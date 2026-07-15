package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// DeliveryBlobRepository 提供交付数据面中转 blob 元数据的数据访问（FR-165，见 v2-delivery-orchestration.md §3.5）。
// blob 内容寻址：sha256 即主身份。基础往返（建 / 按 sha256 查 / 分页列表）之上，
// M2 数据面追加：上传占位 upsert / 就绪落账 / 容量求和 / 引用刷新 / 就绪集探测 / 清理筛选与删除。
type DeliveryBlobRepository struct {
	db *gorm.DB
}

// NewDeliveryBlobRepository 构造仓库。
func NewDeliveryBlobRepository(db *gorm.DB) *DeliveryBlobRepository {
	return &DeliveryBlobRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *DeliveryBlobRepository) WithTx(tx *gorm.DB) *DeliveryBlobRepository {
	return &DeliveryBlobRepository{db: tx}
}

// Create 追加一条 blob 元数据。
func (r *DeliveryBlobRepository) Create(blob *model.DeliveryBlob) error {
	return r.db.Create(blob).Error
}

// FindBySHA256 按内容哈希查 blob；不存在返回 (nil, nil)。
func (r *DeliveryBlobRepository) FindBySHA256(sha256 string) (*model.DeliveryBlob, error) {
	var blob model.DeliveryBlob
	err := r.db.Where("sha256 = ?", sha256).First(&blob).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

// UpsertUploading 写入 / 刷新上传占位行：不存在则建 state=uploading（size 记声明值供容量核算），
// 已存在则仅刷新 size 与 last_referenced_at、**不回写 state**——并发场景另一路上传可能已置 ready，
// 不得把就绪 blob 降级回 uploading（终态以 MarkReady 为准）。OnConflict 三方言（MySQL/Postgres/sqlite）均可移植。
func (r *DeliveryBlobRepository) UpsertUploading(sha string, size int64, at time.Time) error {
	blob := model.DeliveryBlob{
		SHA256: sha, SizeBytes: size, State: model.DeliveryBlobStateUploading,
		LastReferencedAt: at, CreatedAt: at,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sha256"}},
		DoUpdates: clause.AssignmentColumns([]string{"size_bytes", "last_referenced_at"}),
	}).Create(&blob).Error
}

// MarkReady 把 blob 落账为就绪：置 state=ready + 实收字节数 + 刷新引用时间（上传完成 / 秒传命中共用）。
func (r *DeliveryBlobRepository) MarkReady(sha string, size int64, at time.Time) error {
	return r.db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).
		Updates(map[string]any{"size_bytes": size, "state": model.DeliveryBlobStateReady, "last_referenced_at": at}).Error
}

// SumBytesExcluding 统计除指定 sha 外全部 blob 的声明 / 实收字节总量（容量预检基数）。
// uploading 占位行按声明大小计入——保守口径，防并发上传合谋超限；失败占位最长 24h 由清理器回收。
func (r *DeliveryBlobRepository) SumBytesExcluding(sha string) (int64, error) {
	var total int64
	err := r.db.Model(&model.DeliveryBlob{}).Where("sha256 <> ?", sha).
		Select("COALESCE(SUM(size_bytes), 0)").Scan(&total).Error
	return total, err
}

// ReadySet 返回给定 sha 集合中已就绪（state=ready）的子集（缺失 blob 探测用；入参为空返回空集）。
func (r *DeliveryBlobRepository) ReadySet(shas []string) (map[string]struct{}, error) {
	ready := make(map[string]struct{}, len(shas))
	if len(shas) == 0 {
		return ready, nil
	}
	var rows []string
	if err := r.db.Model(&model.DeliveryBlob{}).
		Where("sha256 IN ? AND state = ?", shas, model.DeliveryBlobStateReady).
		Pluck("sha256", &rows).Error; err != nil {
		return nil, err
	}
	for _, sha := range rows {
		ready[sha] = struct{}{}
	}
	return ready, nil
}

// TouchAll 刷新给定 sha 集合的 last_referenced_at（活动单引用登记，清理保护；入参为空为 no-op）。
func (r *DeliveryBlobRepository) TouchAll(shas []string, at time.Time) error {
	if len(shas) == 0 {
		return nil
	}
	return r.db.Model(&model.DeliveryBlob{}).Where("sha256 IN ?", shas).
		Update("last_referenced_at", at).Error
}

// ListReadyReferencedBefore 取就绪且最近引用时间早于 cutoff 的 blob（保留期清理候选，引用阻断由调用方再筛）。
func (r *DeliveryBlobRepository) ListReadyReferencedBefore(cutoff time.Time) ([]model.DeliveryBlob, error) {
	var blobs []model.DeliveryBlob
	err := r.db.Where("state = ? AND last_referenced_at < ?", model.DeliveryBlobStateReady, cutoff).
		Order("sha256 asc").Find(&blobs).Error
	return blobs, err
}

// ListUploadingBefore 取上传中且最近活动早于 cutoff 的残留占位行（上传中断 24h 清理，spec §4.5.4）。
func (r *DeliveryBlobRepository) ListUploadingBefore(cutoff time.Time) ([]model.DeliveryBlob, error) {
	var blobs []model.DeliveryBlob
	err := r.db.Where("state = ? AND last_referenced_at < ?", model.DeliveryBlobStateUploading, cutoff).
		Order("sha256 asc").Find(&blobs).Error
	return blobs, err
}

// Delete 按 sha 删除 blob 元数据行（磁盘文件由数据面服务负责，先删行再删文件）。
func (r *DeliveryBlobRepository) Delete(sha string) error {
	return r.db.Where("sha256 = ?", sha).Delete(&model.DeliveryBlob{}).Error
}

// List 分页查询 blob 元数据（创建时间倒序），返回当页记录与总数。
func (r *DeliveryBlobRepository) List(page, size int) ([]model.DeliveryBlob, int64, error) {
	q := r.db.Model(&model.DeliveryBlob{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var blobs []model.DeliveryBlob
	if err := q.Order("created_at desc, sha256 asc").
		Limit(size).Offset((page - 1) * size).
		Find(&blobs).Error; err != nil {
		return nil, 0, err
	}
	return blobs, total, nil
}

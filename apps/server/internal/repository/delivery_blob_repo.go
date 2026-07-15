package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// DeliveryBlobRepository 提供交付数据面中转 blob 元数据的数据访问（FR-165，见 v2-delivery-orchestration.md §3.5）。
// blob 内容寻址：sha256 即主身份。M0 骨架仅承载基础往返（建 / 按 sha256 查 / 分页列表）；
// 去重探测、清理筛选等由 M1+ 数据面服务按需扩展。
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

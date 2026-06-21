package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// FileRevisionRepository 提供 file_revision 表的数据访问（append-only）。
type FileRevisionRepository struct {
	db *gorm.DB
}

// NewFileRevisionRepository 构造仓库。
func NewFileRevisionRepository(db *gorm.DB) *FileRevisionRepository {
	return &FileRevisionRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *FileRevisionRepository) WithTx(tx *gorm.DB) *FileRevisionRepository {
	return &FileRevisionRepository{db: tx}
}

// Create 追加一条版本快照。
func (r *FileRevisionRepository) Create(rev *model.FileRevision) error {
	return r.db.Create(rev).Error
}

// ListByObject 按版本升序列出某文件对象的全部历史版本。
func (r *FileRevisionRepository) ListByObject(objectID uint) ([]model.FileRevision, error) {
	var revs []model.FileRevision
	if err := r.db.Where("file_object_id = ?", objectID).Order("version asc").Find(&revs).Error; err != nil {
		return nil, err
	}
	return revs, nil
}

// FindByObjectAndVersion 取某文件对象的指定版本；不存在返回 (nil, nil)。
func (r *FileRevisionRepository) FindByObjectAndVersion(objectID uint, version int64) (*model.FileRevision, error) {
	var rev model.FileRevision
	err := r.db.Where("file_object_id = ? AND version = ?", objectID, version).First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

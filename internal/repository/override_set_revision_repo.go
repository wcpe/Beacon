package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// FileOverrideSetRevisionRepository 提供 file_override_set_revision 表的数据访问（append-only）。
type FileOverrideSetRevisionRepository struct {
	db *gorm.DB
}

// NewFileOverrideSetRevisionRepository 构造仓库。
func NewFileOverrideSetRevisionRepository(db *gorm.DB) *FileOverrideSetRevisionRepository {
	return &FileOverrideSetRevisionRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *FileOverrideSetRevisionRepository) WithTx(tx *gorm.DB) *FileOverrideSetRevisionRepository {
	return &FileOverrideSetRevisionRepository{db: tx}
}

// Create 追加一条版本快照。
func (r *FileOverrideSetRevisionRepository) Create(rev *model.FileOverrideSetRevision) error {
	return r.db.Create(rev).Error
}

// ListBySet 按版本升序列出某覆盖集的全部历史版本。
func (r *FileOverrideSetRevisionRepository) ListBySet(setID uint) ([]model.FileOverrideSetRevision, error) {
	var revs []model.FileOverrideSetRevision
	if err := r.db.Where("override_set_id = ?", setID).Order("version asc").Find(&revs).Error; err != nil {
		return nil, err
	}
	return revs, nil
}

// FindBySetAndVersion 取某覆盖集的指定版本；不存在返回 (nil, nil)。
func (r *FileOverrideSetRevisionRepository) FindBySetAndVersion(setID uint, version int64) (*model.FileOverrideSetRevision, error) {
	var rev model.FileOverrideSetRevision
	err := r.db.Where("override_set_id = ? AND version = ?", setID, version).First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

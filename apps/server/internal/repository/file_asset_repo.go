package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// FileAssetRef 定位一个文件资产（一台服务器上的一个相对路径），即 file_asset 的唯一键 (server_id, path)。
type FileAssetRef struct {
	ServerID uint
	Path     string
}

// FileAssetRepository 提供文件资产索引 file_asset 表的数据访问（FR-163/164，规格 v2-file-assets.md §3.1）。
type FileAssetRepository struct {
	db *gorm.DB
}

// NewFileAssetRepository 构造仓库。
func NewFileAssetRepository(db *gorm.DB) *FileAssetRepository {
	return &FileAssetRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用）。
func (r *FileAssetRepository) WithTx(tx *gorm.DB) *FileAssetRepository {
	return &FileAssetRepository{db: tx}
}

// FindByServerPath 按唯一键 (server_id, path) 查单行；不存在返回 (nil, nil)。
func (r *FileAssetRepository) FindByServerPath(serverID uint, path string) (*model.FileAsset, error) {
	var asset model.FileAsset
	err := r.db.Where("server_id = ? AND path = ?", serverID, path).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// FindByRefs 按一组 (server_id, path) 批量查行（diff 双侧哈希短路用，规格 §4.5）。
// 只返回命中的行（某侧缺失即无对应行，由调用方判定），每个 OR 分支走唯一键索引。
func (r *FileAssetRepository) FindByRefs(refs []FileAssetRef) ([]model.FileAsset, error) {
	if len(refs) == 0 {
		return []model.FileAsset{}, nil
	}
	query := r.db.Model(&model.FileAsset{})
	for i, ref := range refs {
		if i == 0 {
			query = query.Where("server_id = ? AND path = ?", ref.ServerID, ref.Path)
			continue
		}
		query = query.Or("server_id = ? AND path = ?", ref.ServerID, ref.Path)
	}
	var assets []model.FileAsset
	if err := query.Find(&assets).Error; err != nil {
		return nil, err
	}
	return assets, nil
}

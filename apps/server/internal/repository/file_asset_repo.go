package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

// AssetSearchQuery 是管理面资产搜索的过滤条件（FR-163，规格 §4.4）。
// NamespaceID 必填（强隔离 + 保证走索引）；ServerID=0 表示不按服过滤；其余空串表示不加该条件。
type AssetSearchQuery struct {
	NamespaceID uint
	ServerID    uint // 引用 server.id 数字行 id；0 = 不过滤
	PathPrefix  string
	Name        string
	Ext         string
	SHA256      string
	Offset      int
	Limit       int
}

// Search 按组合条件分页搜索资产行并回总数（规格 §4.4）。
// 各条件左锚定 / 相等尽量走 (namespace_id, *) 复合索引；name 为兜底子串条件（调用方保证与至少一个索引条件组合）。
func (r *FileAssetRepository) Search(q AssetSearchQuery) ([]model.FileAsset, int64, error) {
	query := r.db.Model(&model.FileAsset{}).Where("namespace_id = ?", q.NamespaceID)
	if q.ServerID != 0 {
		query = query.Where("server_id = ?", q.ServerID)
	}
	if q.PathPrefix != "" {
		query = query.Where("path LIKE ? ESCAPE '\\'", escapeLike(q.PathPrefix)+"%")
	}
	if q.Name != "" {
		query = query.Where("path LIKE ? ESCAPE '\\'", "%"+escapeLike(q.Name)+"%")
	}
	if q.Ext != "" {
		query = query.Where("ext = ?", q.Ext)
	}
	if q.SHA256 != "" {
		query = query.Where("sha256 = ?", q.SHA256)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.FileAsset
	if err := query.Order("server_id ASC, path ASC").Offset(q.Offset).Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindByNamespacePathServers 取给定 namespace 下某精确 path 在指定服集合内的全部行（跨服比对用，规格 §4.4）。
// 走 (namespace_id, path) 索引单查后按 server_id 收窄；serverIDs 为空返回空集。
func (r *FileAssetRepository) FindByNamespacePathServers(namespaceID uint, path string, serverIDs []uint) ([]model.FileAsset, error) {
	if len(serverIDs) == 0 {
		return []model.FileAsset{}, nil
	}
	var rows []model.FileAsset
	if err := r.db.Where("namespace_id = ? AND path = ? AND server_id IN ?", namespaceID, path, serverIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByServer 取某服全部资产行（按 path 升序），供上报后重算清单摘要（规格 §4.3）。
func (r *FileAssetRepository) ListByServer(serverID uint) ([]model.FileAsset, error) {
	var rows []model.FileAsset
	if err := r.db.Where("server_id = ?", serverID).Order("path ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// UpsertAssets 按唯一键 (server_id, path) 批量 upsert：命中即更新内容元数据（增量 upsert / 全量替换共用）。
// 显式声明冲突列，避免 MySQL 生成空 `ON DUPLICATE KEY UPDATE` 语法错（见 conn_detail_repo 同款注意）。
// created_at 不进 DoUpdates（保留首次落库时间）；须在外层事务内调用。
func (r *FileAssetRepository) UpsertAssets(rows []model.FileAsset) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"namespace_id", "ext", "sha256", "size", "mtime_ms", "is_text", "scanned_at", "updated_at",
		}),
	}).CreateInBatches(&rows, assetUpsertBatchSize).Error
}

// DeleteByServerPaths 按 (server_id, path) 批量删除消失的文件（增量 delete，规格 §4.3）。
func (r *FileAssetRepository) DeleteByServerPaths(serverID uint, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return r.db.Where("server_id = ? AND path IN ?", serverID, paths).Delete(&model.FileAsset{}).Error
}

// DeleteAllByServer 清空某服全部资产行（全量整体替换第一步，规格 §4.3）；须在外层事务内调用。
func (r *FileAssetRepository) DeleteAllByServer(serverID uint) error {
	return r.db.Where("server_id = ?", serverID).Delete(&model.FileAsset{}).Error
}

// assetUpsertBatchSize 是批量 upsert 的分批大小（单请求条目已限 2000，分批控制单条 SQL 参数量）。
const assetUpsertBatchSize = 500

// escapeLike 转义 LIKE 通配元字符（`\` `%` `_`），配合 `ESCAPE '\'` 使 path 前缀 / 子串按字面匹配。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
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
//
// FR-261：校验落账是否真的作用在一条存在的行上——没落到任何行表示占位行在落账前已被清理器
// 回收（或从未建立），此时**必须报错**而非静默成功：静默会让「元数据无行」与「上传成功」
// 两种事实互不相认，目标下载时才 404、且无任何告警。报错后 agent 可安全重传（重传会重建占位行）。
//
// 判据取「行是否存在」而**不是**「RowsAffected > 0」：MySQL 默认返回的是**真正被修改**的行数
// （值未变即计 0），而 sqlite 返回的是**匹配**的行数（值未变也计 1）。只看 RowsAffected 会在
// MySQL 上把「重复落账、值恰好未变」误报成 `blob_upload_slot_lost`（sqlite 单测绿但真库误报）。
// 故 RowsAffected 为 0 时回查一次行：行在即视为幂等成功，行不在才是真丢槽。
func (r *DeliveryBlobRepository) MarkReady(sha string, size int64, at time.Time) error {
	res := r.db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).
		Updates(map[string]any{"size_bytes": size, "state": model.DeliveryBlobStateReady, "last_referenced_at": at})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	exists, err := r.Exists(sha)
	if err != nil {
		return err
	}
	if !exists {
		return apperr.ErrDeliveryBlobSlotLost
	}
	return nil // 行在但值未变（重复落账）：幂等成功
}

// Exists 判定某 sha 的元数据行是否存在（落账丢槽判别用；避免出现依赖 RowsAffected 语义的方言差异）。
func (r *DeliveryBlobRepository) Exists(sha string) (bool, error) {
	var count int64
	if err := r.db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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

// StatesBySHAs 批量取给定 sha 集合的就绪态（state 列；缺失的 sha 不出现在结果里）。
//
// 供孤儿扫描批量判定使用（FR-261 P1-3）：逐 sha 查一次是 N+1，小文件场景单轮可达万级查询。
// 入参分批（每批 deliveryBlobStateBatchSize）以避免 `IN` 绑定参数过多（MySQL 的 max_allowed_packet /
// 参数上限），并保持 GORM 可移植。入参为空返回空集。
func (r *DeliveryBlobRepository) StatesBySHAs(shas []string) (map[string]string, error) {
	states := make(map[string]string, len(shas))
	for start := 0; start < len(shas); start += deliveryBlobStateBatchSize {
		end := start + deliveryBlobStateBatchSize
		if end > len(shas) {
			end = len(shas)
		}
		var rows []struct {
			SHA256 string
			State  string
		}
		if err := r.db.Model(&model.DeliveryBlob{}).
			Where("sha256 IN ?", shas[start:end]).
			Select("sha256", "state").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			states[row.SHA256] = row.State
		}
	}
	return states, nil
}

// deliveryBlobStateBatchSize 是批量查状态时的单批 sha 上限（防 `IN` 绑定参数过多）。
const deliveryBlobStateBatchSize = 500

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

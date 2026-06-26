package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// AgentCommandRepository 提供 agent_command 表的数据访问（FR-39，见 ADR-0027）。
// 命令真源在库：建为 pending，agent 拉取后 CAS 迁移状态，超时由清理标 expired。
type AgentCommandRepository struct {
	db *gorm.DB
}

// NewAgentCommandRepository 构造仓库。
func NewAgentCommandRepository(db *gorm.DB) *AgentCommandRepository {
	return &AgentCommandRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *AgentCommandRepository) WithTx(tx *gorm.DB) *AgentCommandRepository {
	return &AgentCommandRepository{db: tx}
}

// Create 追加一条命令（状态由调用方置 pending）。
func (r *AgentCommandRepository) Create(cmd *model.AgentCommand) error {
	return r.db.Create(cmd).Error
}

// FindByID 按主键查命令；不存在返回 (nil, nil)。
func (r *AgentCommandRepository) FindByID(id uint) (*model.AgentCommand, error) {
	var c model.AgentCommand
	err := r.db.Where("id = ?", id).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// FindOldestPending 取某目标实例最早一条 pending 命令（供 agent 拉取）；无则 (nil, nil)。
func (r *AgentCommandRepository) FindOldestPending(ns, serverID string) (*model.AgentCommand, error) {
	var c model.AgentCommand
	err := r.db.Where("namespace = ? AND server_id = ? AND status = ?", ns, serverID, model.CommandStatusPending).
		Order("id asc").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateStatus 按期望前态做状态迁移（CAS，幂等）：仅当当前 status=expect 才改为 next。
// 返回是否命中（前态不符 / 不存在则 false）。result 为结果摘要（无敏感内容），空则不动该列。
func (r *AgentCommandRepository) UpdateStatus(id uint, expect, next, result string) (bool, error) {
	updates := map[string]any{"status": next}
	if result != "" {
		updates["result_detail"] = result
	}
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateImprintReady 把命令从 fetched CAS 迁移 ready 并转存拓印内容（FR-46）：仅当当前 status=fetched 才迁移。
// content 即目标文件磁盘原文（瞬态，待确认 / 失败 / 过期清空）。返回是否命中（前态不符 / 不存在则 false）。
func (r *AgentCommandRepository) UpdateImprintReady(id uint, content string) (bool, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, model.CommandStatusFetched).
		Updates(map[string]any{"status": model.CommandStatusReady, "imprint_content": content})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateStatusClearImprint 按期望前态做状态迁移并清空拓印瞬态内容（FR-46 确认落库后）：仅当 status=expect 才迁移。
// result 为结果摘要（无敏感内容），空则不动该列。返回是否命中。
func (r *AgentCommandRepository) UpdateStatusClearImprint(id uint, expect, next, result string) (bool, error) {
	updates := map[string]any{"status": next, "imprint_content": ""}
	if result != "" {
		updates["result_detail"] = result
	}
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindLatestByType 取某目标实例某类型的最新一条命令（按 id 倒序，FR-88 取日志查询用）；无则 (nil, nil)。
func (r *AgentCommandRepository) FindLatestByType(ns, serverID, cmdType string) (*model.AgentCommand, error) {
	var c model.AgentCommand
	err := r.db.Where("namespace = ? AND server_id = ? AND type = ?", ns, serverID, cmdType).
		Order("id desc").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CountActiveByType 统计某目标实例某类型处「进行中」（pending/fetched）的命令条数（FR-88 单活跃限速用）。
func (r *AgentCommandRepository) CountActiveByType(ns, serverID, cmdType string) (int64, error) {
	var n int64
	err := r.db.Model(&model.AgentCommand{}).
		Where("namespace = ? AND server_id = ? AND type = ? AND status IN ?",
			ns, serverID, cmdType, []string{model.CommandStatusPending, model.CommandStatusFetched}).
		Count(&n).Error
	return n, err
}

// UpdateStatusWithLogContent 把命令从 fetched CAS 迁移 done 并存取日志回传内容（FR-88）：仅当当前 status=fetched 才迁移。
// content 即 agent 回传的脱敏日志行 JSON（瞬态，取一次后由过期清理清空）。返回是否命中（前态不符 / 不存在则 false）。
func (r *AgentCommandRepository) UpdateStatusWithLogContent(id uint, content string) (bool, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, model.CommandStatusFetched).
		Updates(map[string]any{"status": model.CommandStatusDone, "log_content": content})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// CountByStatus 按状态分组统计命令条数（跨全部目标汇总，仅观测，FR-82）。
// 一条 GROUP BY 查询（可移植 GORM、无方言）；无某状态则该键缺省（不返回 0 键）。
func (r *AgentCommandRepository) CountByStatus() (map[string]int, error) {
	var rows []struct {
		Status string
		Cnt    int
	}
	err := r.db.Model(&model.AgentCommand{}).
		Select("status, count(*) as cnt").
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Cnt
	}
	return counts, nil
}

// CommandFilter 是命令观测查询的过滤与分页条件（FR-104；零值字段不过滤；时间零值不设界）。
type CommandFilter struct {
	Namespace string
	ServerID  string
	Type      string
	Status    string
	From      time.Time
	To        time.Time
	Page      int // 从 1 起
	Size      int
}

// CommandMeta 是命令对外观测的元数据投影行（FR-104）：**绝不含** imprint_content / log_content（瞬态敏感）
// 与 payload（大文本、含 ingest 目标，观测无需）；仅元数据 + 结果摘要 result_detail（已是不含敏感的摘要）。
type CommandMeta struct {
	ID            uint
	NamespaceCode string `gorm:"column:namespace"`
	ServerID      string `gorm:"column:server_id"`
	Type          string `gorm:"column:type"`
	Status        string `gorm:"column:status"`
	ResultDetail  string `gorm:"column:result_detail"`
	Operator      string `gorm:"column:operator"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// commandMetaColumns 是命令观测投影列（显式列出，确保永不带出 imprint_content / log_content / payload）。
var commandMetaColumns = []string{
	"id", "namespace", "server_id", "type", "status", "result_detail", "operator", "created_at", "updated_at",
}

// applyCommandFilter 把过滤条件叠加到查询上（List 与 ScanForAnalytics 共用过滤口径）。
// 仅占位符 + 标准 SQL，不依赖方言函数，保 Postgres 可移植。
func applyCommandFilter(q *gorm.DB, f CommandFilter) *gorm.DB {
	if f.Namespace != "" {
		q = q.Where("namespace = ?", f.Namespace)
	}
	if f.ServerID != "" {
		q = q.Where("server_id = ?", f.ServerID)
	}
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	return q
}

// List 按过滤条件分页查询命令元数据（创建时间倒序），返回当页投影行与总数（FR-104）。
// 投影仅取 commandMetaColumns，**绝不**带出 imprint_content / log_content / payload。
func (r *AgentCommandRepository) List(f CommandFilter) ([]CommandMeta, int64, error) {
	q := applyCommandFilter(r.db.Model(&model.AgentCommand{}), f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []CommandMeta
	if err := q.Select(commandMetaColumns).
		Order("created_at desc, id desc").
		Limit(f.Size).Offset((f.Page - 1) * f.Size).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CommandAnalyticsRow 是命令聚合用的窗口内投影行（FR-104，仅取聚合所需四列）。
type CommandAnalyticsRow struct {
	CreatedAt time.Time
	Status    string
	Type      string
	ServerID  string `gorm:"column:server_id"`
}

// ScanForAnalytics 取窗口内命令的聚合投影行（created_at/status/type/server_id 四列，按时间升序，FR-104）。
// 只复用 Namespace/From/To 过滤；日分桶与计数交由 service 在 Go 侧做（禁方言日期函数，保可移植）。
func (r *AgentCommandRepository) ScanForAnalytics(f CommandFilter) ([]CommandAnalyticsRow, error) {
	q := r.db.Model(&model.AgentCommand{})
	if f.Namespace != "" {
		q = q.Where("namespace = ?", f.Namespace)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	var rows []CommandAnalyticsRow
	if err := q.Select("created_at", "status", "type", "server_id").
		Order("created_at asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ExpireStale 把创建早于 before、仍处 pending/fetched/ready 的命令标 expired（超时清理）；返回受影响条数。
// ready（FR-46 拓印已抓取未确认）一并过期并清空瞬态拓印内容，避免未确认的磁盘原文长期滞留。
// 同时清空 log_content（FR-88 取日志瞬态）：过期命令的回传日志一并抹除，避免瞬态长期滞留。
func (r *AgentCommandRepository) ExpireStale(before time.Time) (int64, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("status IN ? AND created_at < ?",
			[]string{model.CommandStatusPending, model.CommandStatusFetched, model.CommandStatusReady}, before).
		Updates(map[string]any{"status": model.CommandStatusExpired, "imprint_content": "", "log_content": ""})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

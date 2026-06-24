package repository

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// AuditFilter 是审计查询的过滤与分页条件（零值字段不过滤；时间零值不设界）。
type AuditFilter struct {
	Namespace  string
	Operator   string
	Action     string
	TargetType string
	TargetRef  string
	// detail 列子串关键字检索（LIKE，FR-84）；空则不过滤
	DetailKeyword string
	From          time.Time
	To            time.Time
	Page          int // 从 1 起
	Size          int
}

// likeEscapeChar 是 LIKE 子串检索的转义字符。不用反斜杠：MySQL 在 SQL 字面量里把 '\' 当转义起始，
// 渲染成 ESCAPE '\' 会报语法错（且各方言对反斜杠语义不一）；选 detail 中罕见的 '!' 作转义字符，
// 并把它与 % / _ 一并转义。配合 ESCAPE 占位符（由驱动按方言安全引用），保 MySQL / Postgres 可移植。
const likeEscapeChar = "!"

// likeEscape 转义 LIKE 模式里的特殊字符（转义字符自身 + % + _），把用户输入当字面子串匹配，
// 避免 % / _ 被当作通配符（FR-84）。
func likeEscape(s string) string {
	r := strings.NewReplacer(
		likeEscapeChar, likeEscapeChar+likeEscapeChar,
		"%", likeEscapeChar+"%",
		"_", likeEscapeChar+"_",
	)
	return r.Replace(s)
}

// AuditAnalyticsRow 是审计聚合用的窗口内投影行（仅取聚合所需三列，FR-73）。
type AuditAnalyticsRow struct {
	CreatedAt time.Time
	Result    string
	Action    string
}

// AuditLogRepository 提供 audit_log 表的数据访问（append-only）。
type AuditLogRepository struct {
	db *gorm.DB
}

// NewAuditLogRepository 构造仓库。
func NewAuditLogRepository(db *gorm.DB) *AuditLogRepository {
	return &AuditLogRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *AuditLogRepository) WithTx(tx *gorm.DB) *AuditLogRepository {
	return &AuditLogRepository{db: tx}
}

// Create 追加一条审计记录。
func (r *AuditLogRepository) Create(entry *model.AuditLog) error {
	return r.db.Create(entry).Error
}

// applyFilter 把过滤条件叠加到查询上（List 与 Stream 共用，保证两者过滤口径一致）。
// 仅占位符 + 标准 SQL，不依赖方言函数，保 Postgres 可移植。
func applyFilter(q *gorm.DB, f AuditFilter) *gorm.DB {
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Operator != "" {
		q = q.Where("operator = ?", f.Operator)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.TargetRef != "" {
		q = q.Where("target_ref = ?", f.TargetRef)
	}
	if f.DetailKeyword != "" {
		// detail 子串检索：转义后 LIKE，ESCAPE 用占位符（驱动按方言安全引用），使 % / _ 当字面字符匹配
		q = q.Where("detail LIKE ? ESCAPE ?", "%"+likeEscape(f.DetailKeyword)+"%", likeEscapeChar)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	return q
}

// List 按过滤条件分页查询审计（时间倒序），返回当页记录与总数。
func (r *AuditLogRepository) List(f AuditFilter) ([]model.AuditLog, int64, error) {
	q := applyFilter(r.db.Model(&model.AuditLog{}), f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.AuditLog
	if err := q.Order("created_at desc, id desc").
		Limit(f.Size).Offset((f.Page - 1) * f.Size).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ScanForAnalytics 取窗口内审计的聚合投影行（仅 created_at/result/action 三列、按时间升序）。
// 只复用 Namespace/From/To 过滤，日分桶与计数交由 service 在 Go 侧做（禁方言日期函数，保可移植）。
func (r *AuditLogRepository) ScanForAnalytics(f AuditFilter) ([]AuditAnalyticsRow, error) {
	q := r.db.Model(&model.AuditLog{})
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	var rows []AuditAnalyticsRow
	if err := q.Select("created_at", "result", "action").
		Order("created_at asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Stream 按过滤条件分批（游标）拉取全部命中审计（时间倒序，id 降序稳定），
// 每批回调 fn，供 service 边查边写导出流，避免一次性全量载入内存（FR-84，性能约束 §17）。
// batch <= 0 时取默认批大小；fn 返回错误则中止并透传。
func (r *AuditLogRepository) Stream(f AuditFilter, batch int, fn func([]model.AuditLog) error) error {
	if batch <= 0 {
		batch = 500
	}
	offset := 0
	for {
		var items []model.AuditLog
		q := applyFilter(r.db.Model(&model.AuditLog{}), f)
		if err := q.Order("created_at desc, id desc").
			Limit(batch).Offset(offset).
			Find(&items).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if err := fn(items); err != nil {
			return err
		}
		if len(items) < batch {
			return nil
		}
		offset += batch
	}
}

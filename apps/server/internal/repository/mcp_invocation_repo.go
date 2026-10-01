package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// mcpInvocationInsertBatchSize 是单条批量写的分批大小（上界保护，防单 SQL 参数触达驱动上限，与 conn 同口径）。
const mcpInvocationInsertBatchSize = 200

// MCPInvocationRepository 提供 MCP 工具调用日表（mcp_invocation_YYYYMMDD）的数据访问（FR-240）。
//
// 写入侧只经异步日表写入通道调用 FlushDaily（请求 goroutine 不碰 DB）；
// 查询侧只读、只判存不建表（查询不得隐式产生空日表，spec §3.7）。
type MCPInvocationRepository struct {
	db *gorm.DB
}

// NewMCPInvocationRepository 构造仓库。
func NewMCPInvocationRepository(db *gorm.DB) *MCPInvocationRepository {
	return &MCPInvocationRepository{db: db}
}

// MCPInvocationQuery 是工具调用流水跨日并表查询的过滤与分页参数（spec §3.7 列表端点）。
type MCPInvocationQuery struct {
	ToolName  string
	ClientID  string
	Result    string
	RiskLevel string
	Reason    string
	FromMs    int64
	ToMs      int64
	Offset    int
	Limit     int
}

// FlushDaily 幂等批量落一批工具调用流水到各自当日表，返回被去重（未落）的行数。
//
// 流程（与 ConnDetailRepository.FlushDaily 同构）：① 按 invocation_id 内嵌 UUIDv7 的 UTC 日分组
// （无法解析的 ID 计入去重、跳过）；② 事务外按需建各当日表（DDL 隐式提交，须在事务外，
// 见 store.EnsureDailyTable）；③ 一个事务内逐日 CreateInBatches（OnConflict DoNothing 幂等，重放安全）。
// 任一日写失败整事务回滚，交写入通道重试（幂等，重放安全）。
func (r *MCPInvocationRepository) FlushDaily(rows []model.MCPInvocation) (deduplicated int, err error) {
	if len(rows) == 0 {
		return 0, nil
	}
	byDay := make(map[time.Time][]model.MCPInvocation)
	for _, row := range rows {
		ms, ok := store.TimeMsFromUUIDv7(row.InvocationID)
		if !ok {
			// invocation_id 非 UUIDv7、无法定位物理表：计入去重并跳过（控制面自造 ID，此为兜底）。
			deduplicated++
			continue
		}
		day := utcDayStart(ms)
		byDay[day] = append(byDay[day], row)
	}
	// 先在事务外确保所有目标日表存在（DDL 隐式提交，不能置于下面的事务内）。
	tableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		name, ensureErr := store.EnsureDailyTable(r.db, &model.MCPInvocation{}, day)
		if ensureErr != nil {
			return 0, ensureErr
		}
		tableByDay[day] = name
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, dayRows := range byDay {
			res := tx.Table(tableByDay[day]).
				Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "invocation_id"}},
					DoNothing: true,
				}).
				CreateInBatches(&dayRows, mcpInvocationInsertBatchSize)
			if res.Error != nil {
				return res.Error
			}
			deduplicated += len(dayRows) - int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deduplicated, nil
}

// FindByInvocationID 由 invocation_id 内嵌 UUIDv7 时间直定日表按主键查单行（免时间范围，spec §3.7 详情）。
// 非法 ID / 日表不存在 / 无此行均返回 (nil, nil)——上层一律 404，不区分「非法」与「不存在」（防探测）。
func (r *MCPInvocationRepository) FindByInvocationID(invocationID string) (*model.MCPInvocation, error) {
	ms, ok := store.TimeMsFromUUIDv7(invocationID)
	if !ok {
		return nil, nil
	}
	name := store.DailyTableName(model.MCPInvocation{}.TableName(), utcDayStart(ms))
	if !r.db.Migrator().HasTable(name) {
		return nil, nil
	}
	var row model.MCPInvocation
	res := r.db.Table(name).Where("invocation_id = ?", invocationID).Limit(1).Find(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

// QueryInvocations 跨日并表按游标分页查询工具调用流水（created_at 降序），
// 只查范围内已存在日表、逐表短路凑满即停。返回本页行与是否还有下一页（供上层算 nextCursor）。
//
// 时间窗可缺省（spec §3.7 的 from / to 皆可选），故用 existingDailyTablesListed 而非逐日判存：
// 缺省窗下逐日枚举会从 1970 走到 2100，纯属浪费。
func (r *MCPInvocationRepository) QueryInvocations(q MCPInvocationQuery) ([]model.MCPInvocation, bool, error) {
	tables := existingDailyTablesListed(r.db, model.MCPInvocation{}.TableName(), q.FromMs, q.ToMs)
	return fetchDailyOffsetPage[model.MCPInvocation](
		r.db, tables, "created_at DESC, invocation_id DESC", q.Offset, q.Limit, q.applyFilters,
	)
}

// applyFilters 套用工具调用流水的六维过滤（时间窗 + 四个等值维度 + 原因码）。纯函数便于单测。
func (q MCPInvocationQuery) applyFilters(db *gorm.DB) *gorm.DB {
	db = db.Where("created_at >= ? AND created_at <= ?", msToTime(q.FromMs), msToTime(q.ToMs))
	if q.ToolName != "" {
		db = db.Where("tool_name = ?", q.ToolName)
	}
	if q.ClientID != "" {
		db = db.Where("client_id = ?", q.ClientID)
	}
	if q.Result != "" {
		db = db.Where("result = ?", q.Result)
	}
	if q.RiskLevel != "" {
		db = db.Where("risk_level = ?", q.RiskLevel)
	}
	if q.Reason != "" {
		db = db.Where("reason = ?", q.Reason)
	}
	return db
}

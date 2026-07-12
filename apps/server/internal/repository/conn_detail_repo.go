package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// connInsertBatchSize 是单条批量写的分批大小（上界保护，防单 SQL 参数触达驱动上限）。
const connInsertBatchSize = 200

// ConnDetailRepository 提供连接明细日表（conn_detail_YYYYMMDD）的数据访问（FR-145）：
// 按 conn_id 内嵌 UUIDv7 时间定当日表、跨日批自动拆分；open 插入会话行、close 按 conn_id 更新同一行；
// 另供孤儿 open 会话对账（proxy 重启补 close）与名册重建（进程重启读 open 行）两个数据面读原语。
type ConnDetailRepository struct {
	db *gorm.DB
}

// NewConnDetailRepository 构造仓库。
func NewConnDetailRepository(db *gorm.DB) *ConnDetailRepository {
	return &ConnDetailRepository{db: db}
}

// OpenConn 是一条遗留 open 会话行的最小投影（名册重建用，spec §4.1）。
type OpenConn struct {
	ConnID        string
	NamespaceID   uint
	ProxyServerID string
	PlayerUUID    string
	FirstBackend  string
	LastBackend   string
}

// FlushDaily 幂等批量落一批（可能跨日、open/close 混合）连接事件到各自当日表，返回被去重（未落）的事件数。
//
// 流程：① 按 conn_id 的 UUIDv7 内嵌时间 UTC 日分组（无法解析的 conn_id 计入去重、跳过）；
// ② 事务外按需建各当日表（DDL 隐式提交，须在事务外，见 store.EnsureDailyTable）；
// ③ 一个事务内逐日先插 open（OnConflict{conn_id} DoNothing）、再 upsert close（OnConflict{conn_id}
// DoUpdates 仅覆盖断开相关列——open 行存在即更新为 closed、缺失则整行插入，避免逐条 UPDATE 的 N+1）。
// 任一日写失败整事务回滚，交写入通道重试（幂等，重放安全）。
func (r *ConnDetailRepository) FlushDaily(rows []model.ConnEvent) (deduplicated int, err error) {
	if len(rows) == 0 {
		return 0, nil
	}
	byDay := make(map[time.Time][]model.ConnEvent)
	for _, ev := range rows {
		ms, ok := store.TimeMsFromUUIDv7(ev.ConnID)
		if !ok {
			// conn_id 非 UUIDv7、无法定位物理表：计入去重并跳过（结构校验应在采集面拦下，此为兜底）。
			deduplicated++
			continue
		}
		day := utcDayStart(ms)
		byDay[day] = append(byDay[day], ev)
	}
	// 先在事务外确保所有目标日表存在（DDL 隐式提交，不能置于下面的事务内）。
	tableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		name, ensureErr := store.EnsureDailyTable(r.db, &model.ConnDetail{}, day)
		if ensureErr != nil {
			return 0, ensureErr
		}
		tableByDay[day] = name
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, evs := range byDay {
			opens, closes := splitConnEvents(evs)
			if len(opens) > 0 {
				dup, insErr := insertConnOpens(tx, tableByDay[day], opens)
				if insErr != nil {
					return insErr
				}
				deduplicated += dup
			}
			if len(closes) > 0 {
				if upErr := upsertConnCloses(tx, tableByDay[day], closes); upErr != nil {
					return upErr
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deduplicated, nil
}

// splitConnEvents 把一日内事件拆为 open 会话行与 close 会话行（各自映射为 ConnDetail）。
func splitConnEvents(evs []model.ConnEvent) (opens, closes []model.ConnDetail) {
	opens = make([]model.ConnDetail, 0, len(evs))
	closes = make([]model.ConnDetail, 0, len(evs))
	for _, ev := range evs {
		if ev.Kind == model.ConnEventKindClose {
			closes = append(closes, toClosedRow(ev))
		} else {
			opens = append(opens, toOpenRow(ev))
		}
	}
	return opens, closes
}

// insertConnOpens 幂等批插 open 会话行：conn_id 主键冲突即忽略（重试 / 已存在安全），返回被去重行数。
func insertConnOpens(tx *gorm.DB, tableName string, rows []model.ConnDetail) (int, error) {
	res := tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "conn_id"}}, DoNothing: true}).
		CreateInBatches(&rows, connInsertBatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	deduplicated := len(rows) - int(res.RowsAffected)
	if deduplicated < 0 {
		deduplicated = 0
	}
	return deduplicated, nil
}

// upsertConnCloses 批量落 close：conn_id 冲突（open 行已在）即只更新断开相关列（opened_at/player 等 open 时段
// 字段保留），冲突不存在（close 先于 open 到达 / 跨批乱序）则整行插入为 closed。避免逐条 UPDATE 的 N+1。
//
// 首末后端均在此更新：open 事件在玩家连上代理、尚未进后端时发出、不带后端；首后端与末后端都由 close 事件携带，
// 故 first_backend_server_id 必须进 DoUpdates，否则 close 更新既有 open 行时首后端被静默丢弃、永不落库。
func upsertConnCloses(tx *gorm.DB, tableName string, rows []model.ConnDetail) error {
	return tx.Table(tableName).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "conn_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"status", "closed_at", "duration_ms", "close_kind", "close_reason",
				"first_backend_server_id", "last_backend_server_id", "backend_switch_count",
			}),
		}).
		CreateInBatches(&rows, connInsertBatchSize).Error
}

// toOpenRow 把 open 事件映射为 open 会话行（断开相关列留空 / NULL）。
func toOpenRow(ev model.ConnEvent) model.ConnDetail {
	return model.ConnDetail{
		ConnID:               ev.ConnID,
		NamespaceID:          ev.NamespaceID,
		ProxyServerID:        ev.ProxyServerID,
		PlayerUUID:           ev.PlayerUUID,
		PlayerName:           ev.PlayerName,
		ClientIP:             ev.ClientIP,
		ProtocolVersion:      ev.ProtocolVersion,
		OpenedAt:             time.UnixMilli(ev.OpenedAtMs).UTC(),
		Status:               model.ConnStatusOpen,
		FirstBackendServerID: ev.FirstBackend,
		LastBackendServerID:  ev.LastBackend,
		BackendSwitchCount:   ev.BackendSwitchCount,
	}
}

// toClosedRow 把 close 事件映射为 closed 会话行：断开时间、时长、分类、末端后端与切换次数落齐。
func toClosedRow(ev model.ConnEvent) model.ConnDetail {
	row := toOpenRow(ev)
	row.Status = model.ConnStatusClosed
	closedAt := time.UnixMilli(ev.ClosedAtMs).UTC()
	row.ClosedAt = &closedAt
	if ev.ClosedAtMs >= ev.OpenedAtMs {
		dur := ev.ClosedAtMs - ev.OpenedAtMs
		row.DurationMs = &dur
	}
	row.CloseKind = ev.CloseKind
	row.CloseReason = ev.CloseReason
	return row
}

// CloseOrphans 把某 proxy 在 before 之前建立、仍 status=open 的会话行批量补 close（close_kind=proxy_shutdown）。
//
// proxy 宕机 / 重启后首次上报（新 bootId）触发对账：这些 open 行属已死进程的孤儿会话（spec §3.2/§4.1）。
// before 取「首次见到新 bootId 的时刻」，只闭合此前建立的会话，避免误闭新进程刚建立的鲜活连接。
// 跨保留窗内逐张已存在日表 UPDATE（缺表跳过、查询侧不隐式建表），返回补 close 的行数合计。
func (r *ConnDetailRepository) CloseOrphans(namespaceID uint, proxyServerID string, before time.Time, retentionDays int) (int64, error) {
	var total int64
	now := time.Now().UTC()
	day := utcDayStart(now.UnixMilli())
	for i := 0; i < retentionDays; i++ {
		name := store.DailyTableName(model.ConnDetail{}.TableName(), day)
		day = day.AddDate(0, 0, -1)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		res := r.db.Table(name).
			Where("namespace_id = ? AND proxy_server_id = ? AND status = ? AND opened_at < ?",
				namespaceID, proxyServerID, model.ConnStatusOpen, before).
			Updates(map[string]any{
				"status":     model.ConnStatusClosed,
				"close_kind": model.ConnCloseKindProxyShutdown,
				"closed_at":  now,
			})
		if res.Error != nil {
			return 0, res.Error
		}
		total += res.RowsAffected
	}
	return total, nil
}

// ConnQuery 是连接明细跨日并表查询的过滤与游标分页参数（spec §5.2 列表端点）。
type ConnQuery struct {
	ServerID    string // 匹配 proxy / 首后端 / 末后端任一（对齐 devmock 语义）
	PlayerUUID  string
	Status      string // "" 全部 / open / closed
	CloseKind   string
	NamespaceID uint
	FromMs      int64
	ToMs        int64
	Offset      int
	Limit       int
}

// FindByConnID 由 conn_id 内嵌 UUIDv7 时间直定日表按主键查单行（免时间范围，spec §4.3 精确 ID 直查）。
// 非法 conn_id / 日表不存在 / 无此行均返回 (nil, nil)。
func (r *ConnDetailRepository) FindByConnID(connID string) (*model.ConnDetail, error) {
	ms, ok := store.TimeMsFromUUIDv7(connID)
	if !ok {
		return nil, nil
	}
	name := store.DailyTableName(model.ConnDetail{}.TableName(), utcDayStart(ms))
	if !r.db.Migrator().HasTable(name) {
		return nil, nil
	}
	var row model.ConnDetail
	res := r.db.Table(name).Where("conn_id = ?", connID).Limit(1).Find(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

// QueryConnections 跨日并表按游标分页查询连接明细（opened_at 降序），只查范围内已存在日表、逐表短路凑满即停。
// 返回本页行与是否还有下一页（供上层算 nextCursor）。
func (r *ConnDetailRepository) QueryConnections(q ConnQuery) ([]model.ConnDetail, bool, error) {
	tables := existingDailyTablesInRange(r.db, model.ConnDetail{}.TableName(), q.FromMs, q.ToMs)
	return fetchDailyOffsetPage[model.ConnDetail](
		r.db, tables, "opened_at DESC, conn_id DESC", q.Offset, q.Limit, q.applyConnFilters,
	)
}

// applyConnFilters 套用连接查询的时间窗与过滤（serverId 匹配 proxy/首后端/末后端任一，对齐 devmock）。
func (q ConnQuery) applyConnFilters(db *gorm.DB) *gorm.DB {
	db = db.Where("opened_at >= ? AND opened_at <= ?", msToTime(q.FromMs), msToTime(q.ToMs))
	if q.ServerID != "" {
		db = db.Where("proxy_server_id = ? OR first_backend_server_id = ? OR last_backend_server_id = ?",
			q.ServerID, q.ServerID, q.ServerID)
	}
	if q.PlayerUUID != "" {
		db = db.Where("player_uuid = ?", q.PlayerUUID)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.CloseKind != "" {
		db = db.Where("close_kind = ?", q.CloseKind)
	}
	if q.NamespaceID != 0 {
		db = db.Where("namespace_id = ?", q.NamespaceID)
	}
	return db
}

// ConnStatRow 是连接流时间桶聚合的窄投影（spec §5.2 connections/stats）：仅取聚合所需三列。
type ConnStatRow struct {
	OpenedAt  time.Time  `gorm:"column:opened_at"`
	ClosedAt  *time.Time `gorm:"column:closed_at"`
	CloseKind string     `gorm:"column:close_kind"`
}

// ScanConnStats 取与窗口重叠的连接会话投影（proxy 过滤），供 service 在 Go 侧按时间桶聚合（禁方言日期函数，保可移植）。
// 限定 opened_at ≤ to 且（closed_at 空 或 closed_at ≥ from），圈定与窗口重叠的会话（含窗口前建立仍在线者，供存量估算）。
// 只扫范围内已存在日表；窗口前更早日表内仍在线的会话不计入（估算近似，spec §4.5）。
func (r *ConnDetailRepository) ScanConnStats(proxyServerID string, fromMs, toMs int64) ([]ConnStatRow, error) {
	from := msToTime(fromMs)
	to := msToTime(toMs)
	out := make([]ConnStatRow, 0, 256)
	for _, tbl := range existingDailyTablesInRange(r.db, model.ConnDetail{}.TableName(), fromMs, toMs) {
		q := r.db.Table(tbl).
			Select("opened_at", "closed_at", "close_kind").
			Where("opened_at <= ?", to).
			Where("closed_at IS NULL OR closed_at >= ?", from)
		if proxyServerID != "" {
			q = q.Where("proxy_server_id = ?", proxyServerID)
		}
		var rows []ConnStatRow
		if err := q.Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// ListOpenConnections 逐张保留窗内已存在日表读出 status=open 的会话行投影（名册重建用，spec §4.1）。
// 只读、缺表跳过、不隐式建表；进程重启后据此重建「玩家 → 所在服」内存名册。
func (r *ConnDetailRepository) ListOpenConnections(retentionDays int) ([]OpenConn, error) {
	out := make([]OpenConn, 0, 256)
	day := utcDayStart(time.Now().UTC().UnixMilli())
	for i := 0; i < retentionDays; i++ {
		name := store.DailyTableName(model.ConnDetail{}.TableName(), day)
		day = day.AddDate(0, 0, -1)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var rows []OpenConn
		err := r.db.Table(name).
			Select("conn_id", "namespace_id", "proxy_server_id", "player_uuid",
				"first_backend_server_id AS first_backend", "last_backend_server_id AS last_backend").
			Where("status = ?", model.ConnStatusOpen).
			Find(&rows).Error
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

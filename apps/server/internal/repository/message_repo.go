package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// msgInsertBatchSize 是单条批量写的分批大小（上界保护，防单 SQL 参数触达驱动上限）。
const msgInsertBatchSize = 200

// MessageRepository 提供消息日表（msg_trace_YYYYMMDD / msg_payload_YYYYMMDD）的数据访问（FR-149/150）：
// 按 message_id 内嵌 UUIDv7 时间定当日表、跨日批自动拆分；元数据与 payload 同一事务写两表、message_id
// 冲突即忽略（消息终态一次性落库、无更新语义、重放幂等）。
type MessageRepository struct {
	db        *gorm.DB
	archiveDB *gorm.DB // 归档库连接，供冷查询并表（FR-152）；nil 表示不可达 / 未配置
}

// NewMessageRepository 构造仓库。
func NewMessageRepository(db *gorm.DB) *MessageRepository {
	return &MessageRepository{db: db}
}

// SetArchiveDB 注入归档库连接供冷查询（includeArchived）并表（FR-152，见 ADR-0066）。
func (r *MessageRepository) SetArchiveDB(archiveDB *gorm.DB) { r.archiveDB = archiveDB }

// HasArchive 归档库连接是否就绪（冷查询可用性）。
func (r *MessageRepository) HasArchive() bool { return r.archiveDB != nil }

// FlushDaily 幂等批量把一批（可能跨日）终态消息记录落各自当日表，返回被去重（未落）的记录数。
//
// 流程：① 按 message_id 的 UUIDv7 内嵌时间 UTC 日分组（无法解析的计入去重、跳过）；
// ② 事务外按需建各当日 msg_trace 与 msg_payload 两张表（DDL 隐式提交，须在事务外）；
// ③ 一个事务内逐日先批插 trace（OnConflict{message_id} DoNothing）、再批插 payload（同冲突策略），
// 保证同一消息的元数据与 payload 同事务落库。任一日写失败整事务回滚，交写入通道重试（幂等）。
func (r *MessageRepository) FlushDaily(records []model.MessageRecord) (deduplicated int, err error) {
	if len(records) == 0 {
		return 0, nil
	}
	byDay := make(map[time.Time][]model.MessageRecord)
	for _, rec := range records {
		ms, ok := store.TimeMsFromUUIDv7(rec.Trace.MessageID)
		if !ok {
			deduplicated++
			continue
		}
		day := utcDayStart(ms)
		byDay[day] = append(byDay[day], rec)
	}
	// 事务外确保两类日表存在（DDL 隐式提交，不能置于事务内）。
	traceTableByDay := make(map[time.Time]string, len(byDay))
	payloadTableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		traceName, e := store.EnsureDailyTable(r.db, &model.MsgTrace{}, day)
		if e != nil {
			return 0, e
		}
		payloadName, e := store.EnsureDailyTable(r.db, &model.MsgPayload{}, day)
		if e != nil {
			return 0, e
		}
		traceTableByDay[day] = traceName
		payloadTableByDay[day] = payloadName
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, recs := range byDay {
			traces, payloads := splitMessageRecords(recs)
			dup, insErr := insertMsgTraces(tx, traceTableByDay[day], traces)
			if insErr != nil {
				return insErr
			}
			deduplicated += dup
			if len(payloads) > 0 {
				if pErr := insertMsgPayloads(tx, payloadTableByDay[day], payloads); pErr != nil {
					return pErr
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

// splitMessageRecords 把合并记录拆为 trace 行与（存在的）payload 行两批。
func splitMessageRecords(recs []model.MessageRecord) (traces []model.MsgTrace, payloads []model.MsgPayload) {
	traces = make([]model.MsgTrace, 0, len(recs))
	payloads = make([]model.MsgPayload, 0, len(recs))
	for _, rec := range recs {
		traces = append(traces, rec.Trace)
		if rec.Payload != nil {
			payloads = append(payloads, *rec.Payload)
		}
	}
	return traces, payloads
}

// insertMsgTraces 幂等批插元数据行：message_id 主键冲突即忽略，返回被去重行数。
func insertMsgTraces(tx *gorm.DB, tableName string, rows []model.MsgTrace) (int, error) {
	res := tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).
		CreateInBatches(&rows, msgInsertBatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	deduplicated := len(rows) - int(res.RowsAffected)
	if deduplicated < 0 {
		deduplicated = 0
	}
	return deduplicated, nil
}

// insertMsgPayloads 幂等批插 payload 行：message_id 主键冲突即忽略（与 trace 同事务）。
func insertMsgPayloads(tx *gorm.DB, tableName string, rows []model.MsgPayload) error {
	return tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).
		CreateInBatches(&rows, msgInsertBatchSize).Error
}

// msgStatsScanCap 是异常链路聚合单次扫描上限（安全阀，防超大窗口无界拉行）：
// 聚合本走短窗（默认 1h、上限 168h），按 created_at 倒序取最近 msgStatsScanCap 行足以覆盖热点异常边。
const msgStatsScanCap = 100000

// MessageQuery 是消息元数据跨日并表查询的过滤与游标分页参数（spec §5.2 列表端点）。
type MessageQuery struct {
	ServerID       string // 匹配来源 / 解析目标 / 定向目标任一（对齐 devmock 语义）
	PlayerUUID     string // 匹配按玩家寻址的 target_player
	Status         string
	MsgType        string
	TargetKind     string // server / player / broadcast（FR-180 additive 过滤），空不过滤
	CrossNamespace *bool  // nil 不过滤
	NamespaceID    uint
	FromMs         int64
	ToMs           int64
	Offset         int
	Limit          int
}

// FindByMessageID 由 message_id 内嵌 UUIDv7 时间直定 msg_trace 日表查单行（免时间范围，spec §4.3）。
// 非法 message_id / 日表不存在 / 无此行均返回 (nil, nil)。
func (r *MessageRepository) FindByMessageID(messageID string) (*model.MsgTrace, error) {
	ms, ok := store.TimeMsFromUUIDv7(messageID)
	if !ok {
		return nil, nil
	}
	return r.findTraceInDay(store.DailyTableName(model.MsgTrace{}.TableName(), utcDayStart(ms)), "message_id = ?", messageID)
}

// findTraceInDay 在某日表按条件取单行 MsgTrace；表不存在或无命中返回 (nil, nil)。
func (r *MessageRepository) findTraceInDay(tableName, cond string, args ...any) (*model.MsgTrace, error) {
	if !r.db.Migrator().HasTable(tableName) {
		return nil, nil
	}
	var row model.MsgTrace
	res := r.db.Table(tableName).Where(cond, args...).Limit(1).Find(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

// FindByCorrelationID 由 correlationId 内嵌时间所在日（及次日，容跨午夜往返）直查：
// 取 correlation_id 或 message_id 命中该 id 的行（RPC 往返请求 + 响应两条，spec §3.3/§4.3）。
func (r *MessageRepository) FindByCorrelationID(correlationID string) ([]model.MsgTrace, error) {
	ms, ok := store.TimeMsFromUUIDv7(correlationID)
	if !ok {
		return nil, nil
	}
	day := utcDayStart(ms)
	out := make([]model.MsgTrace, 0, 2)
	for _, d := range []time.Time{day, day.AddDate(0, 0, 1)} {
		name := store.DailyTableName(model.MsgTrace{}.TableName(), d)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var rows []model.MsgTrace
		if err := r.db.Table(name).
			Where("correlation_id = ? OR message_id = ?", correlationID, correlationID).
			Order("created_at DESC, message_id DESC").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// QueryMessages 跨日并表按游标分页查询消息元数据（created_at 降序），只查范围内已存在日表、逐表短路凑满即停。
func (r *MessageRepository) QueryMessages(q MessageQuery) ([]model.MsgTrace, bool, error) {
	tables := existingDailyTablesInRange(r.db, model.MsgTrace{}.TableName(), q.FromMs, q.ToMs)
	return fetchDailyOffsetPage[model.MsgTrace](
		r.db, tables, "created_at DESC, message_id DESC", q.Offset, q.Limit, q.applyMsgFilters,
	)
}

// QueryMessagesCold 冷查询并表（FR-152，spec §4.4）：对热 + 归档连接执行同构查询（同过滤 / 同
// created_at DESC, message_id DESC / keyset 边界），应用层有序归并、按 message_id 去重保热侧、取前 limit。
func (r *MessageRepository) QueryMessagesCold(q MessageQuery, cursorToken string, limit int) ([]model.MsgTrace, string, error) {
	cursor := decodeColdCursor(cursorToken)
	want := limit + 1
	const order = "created_at DESC, message_id DESC"
	apply := func(db *gorm.DB) *gorm.DB {
		db = q.applyMsgFilters(db)
		if !cursor.isZero() {
			ct := msToTime(cursor.TimeMs)
			db = db.Where("created_at < ? OR (created_at = ? AND message_id < ?)", ct, ct, cursor.ID)
		}
		return db
	}
	base := model.MsgTrace{}.TableName()
	hot, err := fetchColdSide[model.MsgTrace](r.db, existingDailyTablesInRange(r.db, base, q.FromMs, q.ToMs), order, want, apply)
	if err != nil {
		return nil, "", err
	}
	arc, err := fetchColdSide[model.MsgTrace](r.archiveDB, existingDailyTablesInRange(r.archiveDB, base, q.FromMs, q.ToMs), order, want, apply)
	if err != nil {
		return nil, "", err
	}
	page, next, _ := mergeColdPage(hot, arc, limit, msgColdKey, coldLessStringDesc)
	return page, next.encode(), nil
}

// msgColdKey 取消息行的冷查询归并键（created_at 毫秒 + message_id）。
func msgColdKey(row model.MsgTrace) coldCursor {
	return coldCursor{TimeMs: row.CreatedAt.UnixMilli(), ID: row.MessageID}
}

// applyMsgFilters 套用消息查询的时间窗与过滤（serverId 匹配来源/解析目标/定向目标任一，对齐 devmock）。
func (q MessageQuery) applyMsgFilters(db *gorm.DB) *gorm.DB {
	db = db.Where("created_at >= ? AND created_at <= ?", msToTime(q.FromMs), msToTime(q.ToMs))
	if q.ServerID != "" {
		db = db.Where("source_server_id = ? OR resolved_server_id = ? OR target_server_id = ?",
			q.ServerID, q.ServerID, q.ServerID)
	}
	if q.PlayerUUID != "" {
		db = db.Where("target_player = ?", q.PlayerUUID)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.MsgType != "" {
		db = db.Where("msg_type = ?", q.MsgType)
	}
	if q.TargetKind != "" {
		db = db.Where("target_kind = ?", q.TargetKind)
	}
	if q.CrossNamespace != nil {
		db = db.Where("cross_namespace = ?", *q.CrossNamespace)
	}
	if q.NamespaceID != 0 {
		db = db.Where("namespace_id = ?", q.NamespaceID)
	}
	return db
}

// FindCorrelated 查一条消息在 RPC 往返中的关联消息（spec §3.3/§5.2 详情 correlated）。
// correlationId 为空 → 无关联。否则在候选日表找对手（排除自身）：
// request（message_id=correlationId）或 response（correlation_id=自身 message_id）。
func (r *MessageRepository) FindCorrelated(messageID, correlationID string) (*model.MsgTrace, error) {
	if correlationID == "" {
		return nil, nil
	}
	for _, day := range correlatedCandidateDays(messageID, correlationID) {
		name := store.DailyTableName(model.MsgTrace{}.TableName(), day)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var row model.MsgTrace
		res := r.db.Table(name).
			Where("(message_id = ? OR correlation_id = ?) AND message_id <> ?", correlationID, messageID, messageID).
			Order("created_at ASC, message_id ASC").Limit(1).Find(&row)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			return &row, nil
		}
	}
	return nil, nil
}

// correlatedCandidateDays 汇总关联查找的候选 UTC 日（去重）：自身 message_id 当日与次日（response 跨午夜落次日）、
// correlationId 当日（request 在其自身当日）。
func correlatedCandidateDays(messageID, correlationID string) []time.Time {
	seen := make(map[int64]struct{}, 3)
	days := make([]time.Time, 0, 3)
	add := func(ms int64, ok bool, alsoNext bool) {
		if !ok {
			return
		}
		candidates := []time.Time{utcDayStart(ms)}
		if alsoNext {
			candidates = append(candidates, utcDayStart(ms).AddDate(0, 0, 1))
		}
		for _, d := range candidates {
			key := d.UnixMilli()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			days = append(days, d)
		}
	}
	selfMs, selfOK := store.TimeMsFromUUIDv7(messageID)
	corrMs, corrOK := store.TimeMsFromUUIDv7(correlationID)
	add(selfMs, selfOK, true)
	add(corrMs, corrOK, false)
	return days
}

// FindPayload 由 message_id 内嵌时间直定 msg_payload 日表查 payload 行（payload 查看流程，spec §4.4）。
// 非法 id / 日表不存在 / 无此行返回 (nil, nil)——上层据此判 payload 未落库。
func (r *MessageRepository) FindPayload(messageID string) (*model.MsgPayload, error) {
	ms, ok := store.TimeMsFromUUIDv7(messageID)
	if !ok {
		return nil, nil
	}
	name := store.DailyTableName(model.MsgPayload{}.TableName(), utcDayStart(ms))
	if !r.db.Migrator().HasTable(name) {
		return nil, nil
	}
	var row model.MsgPayload
	res := r.db.Table(name).Where("message_id = ?", messageID).Limit(1).Find(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

// MsgStatRow 是异常链路聚合的窄投影（spec §5.2 messages/stats）：仅取聚合所需列，不触碰 payload。
type MsgStatRow struct {
	MessageID        string `gorm:"column:message_id"`
	MsgType          string `gorm:"column:msg_type"`
	TargetKind       string `gorm:"column:target_kind"`
	SourceServerID   string `gorm:"column:source_server_id"`
	ResolvedServerID string `gorm:"column:resolved_server_id"`
	Status           string `gorm:"column:status"`
	FailReason       string `gorm:"column:fail_reason"`
	DurationMs       *int64 `gorm:"column:duration_ms"`
}

// ScanMessageStats 取窗口内消息的聚合投影（created_at 倒序、上限 msgStatsScanCap），供 service 在 Go 侧按边/类型聚合。
// 只扫范围内已存在日表；不返回 payload 相关内容（异常链路数据源，spec §4.5）。
func (r *MessageRepository) ScanMessageStats(fromMs, toMs int64) ([]MsgStatRow, error) {
	from := msToTime(fromMs)
	to := msToTime(toMs)
	out := make([]MsgStatRow, 0, 512)
	for _, tbl := range existingDailyTablesInRange(r.db, model.MsgTrace{}.TableName(), fromMs, toMs) {
		remaining := msgStatsScanCap - len(out)
		if remaining <= 0 {
			break
		}
		var rows []MsgStatRow
		if err := r.db.Table(tbl).
			Select("message_id", "msg_type", "target_kind", "source_server_id", "resolved_server_id",
				"status", "fail_reason", "duration_ms").
			Where("created_at >= ? AND created_at <= ?", from, to).
			Order("created_at DESC, message_id DESC").
			Limit(remaining).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

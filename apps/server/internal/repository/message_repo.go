package repository

import (
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// msgInsertBatchSize 是单条批量写的分批大小（上界保护，防单 SQL 参数触达驱动上限）。
const msgInsertBatchSize = 200

// msgIDTimeTrustWindowMs 是「message_id 内嵌时间可信」的最大偏差（24h，见 v2-connection-message-storage.md §3.1）：
// 偏差在窗内 → 按 ID 内嵌时间定日表（保证「由 ID 即可定位物理表」）；超出窗口 / ID 非规范 UUIDv7
// （如 agent 误用 UUIDv4 随机 ID）→ 改由控制面接收时刻定日表并记 WARN，绝不按随机位推导日表。
const msgIDTimeTrustWindowMs int64 = 24 * 60 * 60 * 1000

// msgLookupFallbackTables 是按 ID 直查「首选日表未命中」时的有界回退探测张数上限（硬约束，防全表扫描）。
const msgLookupFallbackTables = 8

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

// WithTx 返回绑定到事务的仓库副本。
func (r *MessageRepository) WithTx(tx *gorm.DB) *MessageRepository {
	return &MessageRepository{db: tx, archiveDB: r.archiveDB}
}

// SetArchiveDB 注入归档库连接供冷查询（includeArchived）并表（FR-152，见 ADR-0066）。
func (r *MessageRepository) SetArchiveDB(archiveDB *gorm.DB) { r.archiveDB = archiveDB }

// HasArchive 归档库连接是否就绪（冷查询可用性）。
func (r *MessageRepository) HasArchive() bool { return r.archiveDB != nil }

// FlushDaily 幂等批量把一批（可能跨日）终态消息记录落各自日表，返回被去重（未落）的记录数。
//
// 流程：① 按可信时间定日分组——message_id 是规范 UUIDv7 且内嵌时间与 created_at 偏差 ≤24h 时按 ID 时间，
// 否则按控制面接收时刻（并记 WARN：ID 不可信不让终态行消失在随机位推导出的垃圾日表里，见 resolveMsgDay）；
// ② 事务外按需建各当日 msg_trace 与 msg_payload 两张表（DDL 隐式提交，须在事务外）；
// ③ 一个事务内逐日先批插 trace（OnConflict{message_id} DoNothing）、再批插 payload（同冲突策略），
// 保证同一消息的元数据与 payload 同事务落库。任一日写失败整事务回滚，交写入通道重试（幂等）。
//
// 不变量：任何一条记录都会落到某张「按日期可被查询窗口枚举到」的日表，不存在「ID 不可解析即跳过」的静默丢弃路径。
func (r *MessageRepository) FlushDaily(records []model.MessageRecord) (deduplicated int, err error) {
	if len(records) == 0 {
		return 0, nil
	}
	byDay := make(map[time.Time][]model.MessageRecord)
	untrusted := make([]string, 0, 4)
	for _, rec := range records {
		day, trusted := resolveMsgDay(rec.Trace.MessageID, rec.Trace.CreatedAt)
		if !trusted {
			untrusted = append(untrusted, rec.Trace.MessageID)
		}
		byDay[day] = append(byDay[day], rec)
	}
	if len(untrusted) > 0 {
		// 错误不静默（ADR-0057）：ID 时间不可信属调用侧生成缺陷 / 时钟异常，必须可见；数据仍按接收时刻落表。
		slog.Warn("消息 message_id 内嵌时间不可信，终态行改按控制面接收时刻定日表",
			"条数", len(untrusted), "messageIds", untrusted)
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

// resolveMsgDay 决定一条消息终态行的目标日表，返回 (UTC 日零点, ID 时间是否可信)。
//
// 可信口径（v2-connection-message-storage.md §3.1）：message_id 是规范 UUIDv7（版本 7 + 变体 10）
// 且其内嵌时间与 created_at（控制面接收时刻）偏差 ≤ msgIDTimeTrustWindowMs → 按 ID 时间定日，
// 保持「由 ID 即可定位物理表」恒成立；否则按 created_at 定日并返回 trusted=false（调用方记 WARN）。
//
// 为什么是「改按接收时刻」而不是「拒绝写入」：拒绝写入会让发送方已被告知 accepted 的消息
// 没有任何追踪行（真机实测的「发出去就消失」），可追踪性优先；ID 时间的异常本身由 WARN 暴露。
func resolveMsgDay(messageID string, createdAt time.Time) (time.Time, bool) {
	if ms, ok := store.TrustedTimeMsFromUUIDv7(messageID); ok {
		if createdAt.IsZero() || absInt64(ms-createdAt.UTC().UnixMilli()) <= msgIDTimeTrustWindowMs {
			return utcDayStart(ms), true
		}
		return utcDayStart(createdAt.UTC().UnixMilli()), false
	}
	if createdAt.IsZero() {
		// 退化分支（正常装配下 created_at 恒由中转按控制面时钟回填）：以当前时刻兜底，绝不丢弃该行。
		return utcDayStart(time.Now().UTC().UnixMilli()), false
	}
	return utcDayStart(createdAt.UTC().UnixMilli()), false
}

// absInt64 取 int64 绝对值。
func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// findTraceInRecentTables 在最近 msgLookupFallbackTables 张已存在日表里按 message_id 有界探测单行。
// 仅用于「ID 内嵌时间不可信导致首选日表推不出来 / 推错」的场景：这类行的终态落表由控制面接收时刻决定。
// 表不存在或无命中返回 (nil, nil)。
func (r *MessageRepository) findTraceInRecentTables(messageID string) (*model.MsgTrace, error) {
	for _, tableName := range recentDailyTables(r.db, model.MsgTrace{}.TableName(), msgLookupFallbackTables) {
		row, err := r.findTraceInDay(tableName, "message_id = ?", messageID)
		if err != nil {
			return nil, err
		}
		if row != nil {
			return row, nil
		}
	}
	return nil, nil
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
	NamespaceIDs   []uint
	Scoped         bool
	FromMs         int64
	ToMs           int64
	Offset         int
	Limit          int
}

// FindByMessageID 由 message_id 直定 msg_trace 日表查单行（免时间范围，spec §4.3）。
//
// 首选日表用 ID 内嵌时间（仅当它是规范 UUIDv7）；未命中时在最近日表里有界回退一次——终态行在
// 「ID 时间不可信」时按控制面接收时刻落表（见 resolveMsgDay），回退保证发送方拿 ID 仍能查到最终去向。
// 非法 message_id / 日表不存在 / 确实无此消息均返回 (nil, nil)。
func (r *MessageRepository) FindByMessageID(messageID string) (*model.MsgTrace, error) {
	if ms, ok := store.TrustedTimeMsFromUUIDv7(messageID); ok {
		row, err := r.findTraceInDay(store.DailyTableName(model.MsgTrace{}.TableName(), utcDayStart(ms)), "message_id = ?", messageID)
		if err != nil || row != nil {
			return row, err
		}
		// 可信 ID 却在自身日表未命中：该行由接收时刻定表（ID 时间与接收时刻偏差超窗），回退并记 WARN。
		row, err = r.findTraceInRecentTables(messageID)
		if row != nil {
			slog.Warn("消息按 ID 直查回退到接收时刻日表命中（ID 内嵌时间与接收时刻偏差超窗，或历史错表数据）", "messageId", messageID)
		}
		return row, err
	}
	// 非规范 UUIDv7：日表由接收时刻决定，直接有界回退（写侧落库时已记 WARN）。
	return r.findTraceInRecentTables(messageID)
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
// 与按 ID 直查同口径：首选日表未命中时在最近日表有界回退（ID 内嵌时间不可信的行按接收时刻定表）。
func (r *MessageRepository) FindByCorrelationID(correlationID string) ([]model.MsgTrace, error) {
	out := make([]model.MsgTrace, 0, 2)
	if ms, ok := store.TimeMsFromUUIDv7(correlationID); ok {
		day := utcDayStart(ms)
		for _, d := range []time.Time{day, day.AddDate(0, 0, 1)} {
			rows, err := r.findCorrelatedInDay(store.DailyTableName(model.MsgTrace{}.TableName(), d), correlationID)
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	for _, tableName := range recentDailyTables(r.db, model.MsgTrace{}.TableName(), msgLookupFallbackTables) {
		rows, err := r.findCorrelatedInDay(tableName, correlationID)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	if len(out) > 0 {
		slog.Warn("消息按 correlationId 直查回退到最近日表命中（ID 内嵌时间不可信，或历史错表数据）", "correlationId", correlationID)
	}
	return out, nil
}

// findCorrelatedInDay 在某日表按 correlationId 取命中的往返消息行；表不存在返回空（不报错）。
func (r *MessageRepository) findCorrelatedInDay(tableName, correlationID string) ([]model.MsgTrace, error) {
	if !r.db.Migrator().HasTable(tableName) {
		return nil, nil
	}
	var rows []model.MsgTrace
	if err := r.db.Table(tableName).
		Where("correlation_id = ? OR message_id = ?", correlationID, correlationID).
		Order("created_at DESC, message_id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
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
	if q.Scoped {
		if len(q.NamespaceIDs) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("namespace_id IN ?", q.NamespaceIDs)
	}
	if q.NamespaceID != 0 {
		db = db.Where("namespace_id = ?", q.NamespaceID)
	}
	return db
}

// FindCorrelated 查一条消息在 RPC 往返中的关联消息（spec §3.3/§5.2 详情 correlated）。
// correlationId 为空 → 无关联。否则在候选日表找对手（排除自身）：
// request（message_id=correlationId）或 response（correlation_id=自身 message_id）。
// 与按 ID 直查同口径：候选日表未命中时（往返两侧 ID 时间不可信，行按接收时刻定表）按最近日表有界回退。
func (r *MessageRepository) FindCorrelated(messageID, correlationID string) (*model.MsgTrace, error) {
	if correlationID == "" {
		return nil, nil
	}
	cond := "(message_id = ? OR correlation_id = ?) AND message_id <> ?"
	args := []any{correlationID, messageID, messageID}
	seen := make(map[string]struct{}, 6)
	tables := make([]string, 0, 6)
	addTable := func(name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		tables = append(tables, name)
	}
	for _, day := range correlatedCandidateDays(messageID, correlationID) {
		addTable(store.DailyTableName(model.MsgTrace{}.TableName(), day))
	}
	// 仅当往返两侧 ID 中存在内嵌时间不可信者时才回退探测最近日表（这类行按接收时刻定表，由 ID 推不出来）；
	// 两侧都是规范 UUIDv7 时候选日表未命中即确实无关联，不做无谓探测。
	_, corrTrusted := store.TrustedTimeMsFromUUIDv7(correlationID)
	_, selfTrusted := store.TrustedTimeMsFromUUIDv7(messageID)
	if !corrTrusted || !selfTrusted {
		for _, name := range recentDailyTables(r.db, model.MsgTrace{}.TableName(), msgLookupFallbackTables) {
			addTable(name)
		}
	}
	for _, name := range tables {
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var row model.MsgTrace
		res := r.db.Table(name).
			Where(cond, args...).
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

// FindPayload 由 message_id 直定 msg_payload 日表查 payload 行（payload 查看流程，spec §4.4）。
// 与 FindByMessageID 同口径：首选 ID 时间当日，未命中（ID 时间不可信时按接收时刻落表）则在最近日表有界回退。
// 非法 id / 日表不存在 / 确实无此行返回 (nil, nil)——上层据此判 payload 未落库。
func (r *MessageRepository) FindPayload(messageID string) (*model.MsgPayload, error) {
	if ms, ok := store.TrustedTimeMsFromUUIDv7(messageID); ok {
		row, err := r.findPayloadInDay(store.DailyTableName(model.MsgPayload{}.TableName(), utcDayStart(ms)), messageID)
		if err != nil || row != nil {
			return row, err
		}
	}
	for _, tableName := range recentDailyTables(r.db, model.MsgPayload{}.TableName(), msgLookupFallbackTables) {
		row, err := r.findPayloadInDay(tableName, messageID)
		if err != nil {
			return nil, err
		}
		if row != nil {
			slog.Warn("消息 payload 按 ID 直查回退到接收时刻日表命中（ID 内嵌时间不可信，或历史错表数据）", "messageId", messageID, "表", tableName)
			return row, nil
		}
	}
	return nil, nil
}

// findPayloadInDay 在某日表按 message_id 取单行 payload；表不存在或无命中返回 (nil, nil)。
func (r *MessageRepository) findPayloadInDay(tableName, messageID string) (*model.MsgPayload, error) {
	if !r.db.Migrator().HasTable(tableName) {
		return nil, nil
	}
	var row model.MsgPayload
	res := r.db.Table(tableName).Where("message_id = ?", messageID).Limit(1).Find(&row)
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
	return r.ScanMessageStatsScoped(nil, false, fromMs, toMs)
}

// ScanMessageStatsScoped 以冻结 namespace 集合读取消息聚合投影。
func (r *MessageRepository) ScanMessageStatsScoped(namespaceIDs []uint, scoped bool, fromMs, toMs int64) ([]MsgStatRow, error) {
	from := msToTime(fromMs)
	to := msToTime(toMs)
	out := make([]MsgStatRow, 0, 512)
	for _, tbl := range existingDailyTablesInRange(r.db, model.MsgTrace{}.TableName(), fromMs, toMs) {
		remaining := msgStatsScanCap - len(out)
		if remaining <= 0 {
			break
		}
		if scoped && len(namespaceIDs) == 0 {
			return out, nil
		}
		query := r.db.Table(tbl).
			Select("message_id", "msg_type", "target_kind", "source_server_id", "resolved_server_id",
				"status", "fail_reason", "duration_ms").
			Where("created_at >= ? AND created_at <= ?", from, to).
			Order("created_at DESC, message_id DESC").
			Limit(remaining)
		if scoped {
			query = query.Where("namespace_id IN ?", namespaceIDs)
		}
		var rows []MsgStatRow
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

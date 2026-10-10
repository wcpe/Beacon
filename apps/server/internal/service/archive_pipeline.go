package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 归档流水线内部信号错误。
var (
	// errArchiveCancelled 在批次边界侦测到取消请求，交由 runJob 收尾为 cancelled（非失败）。
	errArchiveCancelled = errors.New("归档任务已被请求取消")
	// errArchiveVerifyFailed 校验门未通过（行数 / 抽样哈希不一致），item 判 failed、绝不删热库。
	errArchiveVerifyFailed = errors.New("归档校验未通过，热库数据未删除")
)

// archiveItemRunner 执行单个工作项的搬运流水线（copying → verifying → deleting → done）。
// 两库无分布式事务：靠 OnConflict 幂等 + cursor 单调 + 校验门补偿，任意时刻崩溃重跑收敛一致终态。
type archiveItemRunner struct {
	hot           *gorm.DB
	archive       *gorm.DB
	dom           archiveDomain
	mode          string // dry_run / execute
	batchRows     int
	batchInterval time.Duration
	sampleSize    int
	// longCtx 是数据面语句的期限上下文（见 archiveLongQueryTimeout）：hot / archive 两库都已绑定它，
	// 使本域的长查询不被 store 的 call 预算误杀。**新增数据面语句请一律用 r.hot / r.archive**，
	// 不要绕回未绑定的 *gorm.DB——那会重新落回 5s 预算里（P1-2 的成因）。
	longCtx context.Context
	// saveItem 持久化工作项当前状态（阶段 / 游标 / 行数 / 校验结果）。
	saveItem func(*model.ArchiveJobItem) error
	// cancelled 批次边界检查是否被请求取消（读任务当前状态）。
	cancelled func() bool
}

// bindLongBudget 把数据面期限绑定到两个连接上（见 archiveLongQueryTimeout），并回传取消函数给调用方管理。
//
// 为什么不用 `WithContext` 的返回值覆盖字段就够：`WithContext` 返回的是**克隆**，原值不受影响；
// 本方法正是要改 r 的两个字段本身，故必须赋值回字段（对照 db.go 里 bootDB 的独立变量写法，
// 那里是「不能让取消影响被返回的 db」，这里是「必须让绑定生效在后续所有语句上」）。
//
// 为什么返回 cancel 而不是内部存着：期限的寿命属于调用方（runJob 的整单范围），
// runner 只是使用者；让调用方持有 cancel 才能保证任务收尾时必然释放计时器。
func (r *archiveItemRunner) bindLongBudget(ctx context.Context) context.CancelFunc {
	ctx, cancel := context.WithTimeout(ctx, archiveLongQueryTimeout)
	r.longCtx = ctx
	r.hot = r.hot.WithContext(ctx)
	r.archive = r.archive.WithContext(ctx)
	return cancel
}

// run 推进工作项到终态（done）或返回错误：dry_run 只统计；execute 按当前阶段续跑（断点续跑）。
func (r *archiveItemRunner) run(item *model.ArchiveJobItem) error {
	if r.mode == model.ArchiveModeDryRun {
		return r.runDryRun(item)
	}
	// 按入口阶段续跑：fallthrough 顺序推进后续阶段（resume 时从 verifying / deleting 起亦覆盖）。
	// failed 项（重试续跑）从 copying 重来——copy 幂等（OnConflict）+ cursor 单调，安全收敛。
	switch item.Phase {
	case model.ArchiveItemPending, model.ArchiveItemCopying, model.ArchiveItemFailed, "":
		if err := r.runCopy(item); err != nil {
			return err
		}
		fallthrough
	case model.ArchiveItemVerifying:
		if err := r.runVerify(item); err != nil {
			return err
		}
		fallthrough
	case model.ArchiveItemDeleting:
		if err := r.runDelete(item); err != nil {
			return err
		}
	}
	item.Phase = model.ArchiveItemDone
	return r.saveItem(item)
}

// runDryRun 只统计预计归档行数后置 done：零写归档、零删热库（spec §4.3）。
func (r *archiveItemRunner) runDryRun(item *model.ArchiveJobItem) error {
	n, err := r.countRows(r.hot, item, nil)
	if err != nil {
		return err
	}
	item.RowsExpected = n
	item.Phase = model.ArchiveItemDone
	return r.saveItem(item)
}

// runCopy 分批搬运：主键升序每批读热库、幂等写归档（OnConflict DoNothing），批提交后写回 cursor、批间限流。
func (r *archiveItemRunner) runCopy(item *model.ArchiveJobItem) error {
	item.Phase = model.ArchiveItemCopying
	if err := r.saveItem(item); err != nil {
		return err
	}
	// 建同名同构归档表（幂等，DDL 在事务外）。
	if err := ensureArchiveTable(r.archive, item.TargetTable, r.dom.newModel()); err != nil {
		return err
	}
	for {
		if r.cancelled() {
			return errArchiveCancelled
		}
		rows, lastPK, err := r.readBatch(item)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		if err := r.writeArchiveBatch(item.TargetTable, rows); err != nil {
			return err
		}
		// 归档侧写成功后才推进 cursor（崩溃重跑至多重搬本批、OnConflict 去重幂等）。
		item.Cursor = lastPK
		item.RowsCopied += int64(len(rows))
		if err := r.saveItem(item); err != nil {
			return err
		}
		if len(rows) < r.batchRows {
			break // 末批
		}
		r.sleep()
	}
	return nil
}

// runVerify 删除前置门：行数校验 + sha256 抽样校验；任一不一致则 verify_passed=false、返回校验失败错误。
func (r *archiveItemRunner) runVerify(item *model.ArchiveJobItem) error {
	item.Phase = model.ArchiveItemVerifying
	if err := r.saveItem(item); err != nil {
		return err
	}
	// 单表形态：归档库累积历次归档行，须以本次热侧最小主键为下界隔离本任务区间（daily 无累积、下界为 nil）。
	var lower any
	if r.dom.form == archiveFormSingle {
		low, ok, err := r.minHotPK(item)
		if err != nil {
			return err
		}
		if ok {
			lower = low
		}
	}
	hotCount, err := r.countRows(r.hot, item, nil)
	if err != nil {
		return err
	}
	archiveCount, err := r.countRows(r.archive, item, lower)
	if err != nil {
		return err
	}
	item.VerifyRowsHot = &hotCount
	item.VerifyRowsArchive = &archiveCount

	seed := stableArchiveSeed(item.TargetTable, item.RangeTo)
	item.VerifySampleSeed = &seed
	pks, err := r.orderedPKs(r.hot, item, lower)
	if err != nil {
		return err
	}
	sample := pickArchiveSample(pks, r.sampleSize, seed)
	size := len(sample)
	item.VerifySampleSize = &size
	hotHash, err := r.hashRows(r.hot, item.TargetTable, sample)
	if err != nil {
		return err
	}
	archiveHash, err := r.hashRows(r.archive, item.TargetTable, sample)
	if err != nil {
		return err
	}
	item.VerifyHashHot = hotHash
	item.VerifyHashArchive = archiveHash

	passed := hotCount == archiveCount && hotHash == archiveHash
	item.VerifyPassed = &passed
	if err := r.saveItem(item); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("%w：行数 热=%d 归档=%d，哈希 热=%s 归档=%s",
			errArchiveVerifyFailed, hotCount, archiveCount, hotHash, archiveHash)
	}
	return nil
}

// runDelete 校验通过后删热库：日表整表 DropTable；单表按主键批 SELECT + DELETE IN 循环（禁 DELETE LIMIT）。
func (r *archiveItemRunner) runDelete(item *model.ArchiveJobItem) error {
	item.Phase = model.ArchiveItemDeleting
	if err := r.saveItem(item); err != nil {
		return err
	}
	if r.dom.form == archiveFormDaily {
		if err := r.hot.Migrator().DropTable(item.TargetTable); err != nil {
			return err
		}
		if item.VerifyRowsHot != nil {
			item.RowsDeleted = *item.VerifyRowsHot
		}
		return r.saveItem(item)
	}
	// 附加行过滤域（extraWhere 非空）走加固路径：过滤谓词可随业务状态变化，不能假定「当前匹配的行都已搬运」。
	if r.dom.extraWhere != "" {
		return r.runFilteredDelete(item)
	}
	// 单表区间：先 SELECT 一批主键，再 DELETE WHERE pk IN(...)，循环至空。
	for {
		if r.cancelled() {
			return errArchiveCancelled
		}
		ids, err := r.deleteBatchIDs(item, nil)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		res := r.hot.Exec("DELETE FROM "+item.TargetTable+" WHERE "+r.dom.pkColumn+" IN ?", ids)
		if res.Error != nil {
			return res.Error
		}
		item.RowsDeleted += res.RowsAffected
		if err := r.saveItem(item); err != nil {
			return err
		}
		if len(ids) < r.batchRows {
			break
		}
		r.sleep()
	}
	return nil
}

// runFilteredDelete 是附加行过滤域（extraWhere 非空）的单表删除路径。
//
// 与默认路径的差别只有一处，但这一处是数据安全的关键：默认路径依赖「选中即删」推进批次，
// 而本路径的过滤谓词**可随业务状态变化**——如 alert_event 的 status 会在归档任务运行期间
// 被人工处置 / 自动消解（孤儿清理器、实例与环境归档），于是「copy 阶段不匹配、delete 阶段匹配」
// 的行会出现：默认路径会把它们直接删掉，而它们**从未搬到归档库**（热库删掉、冷库没有 = 静默丢失）。
//
// 因此本路径：① 以主键游标单调推进（选出但不删的行不会让循环原地打转）；
// ② 只删「归档侧已确认存在」的主键——删除集合恒 ⊆ 已搬运集合，漏搬的行留在热库由下一轮任务处理。
func (r *archiveItemRunner) runFilteredDelete(item *model.ArchiveJobItem) error {
	var afterPK any
	skipped := int64(0)
	for {
		if r.cancelled() {
			return errArchiveCancelled
		}
		ids, err := r.deleteBatchIDs(item, afterPK)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		selected := len(ids)
		afterPK = ids[selected-1] // 无论本批是否真的删除，游标都推进
		kept, err := r.archivedPKs(item, ids)
		if err != nil {
			return err
		}
		skipped += int64(selected - len(kept))
		if len(kept) > 0 {
			res := r.hot.Exec("DELETE FROM "+item.TargetTable+" WHERE "+r.dom.pkColumn+" IN ?", kept)
			if res.Error != nil {
				return res.Error
			}
			item.RowsDeleted += res.RowsAffected
			if err := r.saveItem(item); err != nil {
				return err
			}
		}
		if selected < r.batchRows {
			break
		}
		r.sleep()
	}
	if skipped > 0 {
		// 未删的行是「运行期间才满足过滤条件、本任务未搬运」的行；留在热库待下一轮归档，不影响数据完整性。
		slog.Warn("归档删除跳过未在归档库确认存在的行", "域", r.dom.name, "表", item.TargetTable, "跳过行数", skipped)
	}
	return nil
}

// archivedPKs 取候选主键中「归档侧已存在」的子集（归档表不存在即空集 → 一个也不删）。
func (r *archiveItemRunner) archivedPKs(item *model.ArchiveJobItem, ids []any) ([]any, error) {
	if !r.archive.Migrator().HasTable(item.TargetTable) {
		return nil, nil
	}
	q := r.archive.Table(item.TargetTable).Where(r.dom.pkColumn+" IN ?", ids)
	return pluckPKs(q, r.dom.pkColumn, r.dom.pkKind)
}

// readBatch 主键升序读一批热库行（cursor 断点 + 单表区间过滤），返回行 map 切片与本批最大主键字符串。
func (r *archiveItemRunner) readBatch(item *model.ArchiveJobItem) ([]map[string]any, string, error) {
	q := r.applyRange(r.hot.Table(item.TargetTable), item)
	if item.Cursor != "" {
		q = q.Where(r.dom.pkColumn+" > ?", parsePK(r.dom.pkKind, item.Cursor))
	}
	var rows []map[string]any
	if err := q.Order(r.dom.pkColumn + " ASC").Limit(r.batchRows).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return rows, item.Cursor, nil
	}
	return rows, pkToString(rows[len(rows)-1][r.dom.pkColumn]), nil
}

// writeArchiveBatch 幂等批量写归档表（可移植；禁 INSERT IGNORE / REPLACE）。
//
// 冲突主键时把主键赋值自身（no-op 不改数据）：MySQL 生成 `ON DUPLICATE KEY UPDATE pk=VALUES(pk)`、
// sqlite 生成 `ON CONFLICT(pk) DO UPDATE SET pk=excluded.pk`，两方言均合法且幂等。
// 不用裸 `DoNothing: true`——因 rows 是 []map（无 struct schema）+ Table() 动态表名，MySQL 方言
// 取不到主键列会生成空 `ON DUPLICATE KEY UPDATE ` 语法错（1064）；sqlite 的 `DO NOTHING` 不需列故只在 sqlite 通过。
func (r *archiveItemRunner) writeArchiveBatch(tableName string, rows []map[string]any) error {
	return r.archive.Table(tableName).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: r.dom.pkColumn}},
			DoUpdates: clause.AssignmentColumns([]string{r.dom.pkColumn}),
		}).
		Create(&rows).Error
}

// deleteBatchIDs 取单表区间内主键升序的一批（至多 batchRows 个），供 DELETE IN 使用；
// afterPK 非空时只取主键更大者（附加过滤域的单调推进游标；默认路径传 nil，行为不变）。
func (r *archiveItemRunner) deleteBatchIDs(item *model.ArchiveJobItem, afterPK any) ([]any, error) {
	q := r.applyRange(r.hot.Table(item.TargetTable), item).
		Order(r.dom.pkColumn + " ASC").Limit(r.batchRows)
	if afterPK != nil {
		q = q.Where(r.dom.pkColumn+" > ?", afterPK)
	}
	return pluckPKs(q, r.dom.pkColumn, r.dom.pkKind)
}

// countRows 统计区间内行数（lower 非空时附加主键下界，隔离单表归档的本任务区间）。
func (r *archiveItemRunner) countRows(db *gorm.DB, item *model.ArchiveJobItem, lower any) (int64, error) {
	q := r.applyRange(db.Table(item.TargetTable), item)
	if lower != nil {
		q = q.Where(r.dom.pkColumn+" >= ?", lower)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// minHotPK 取热库区间内最小主键（单表下界）；区间空返回 (nil,false,nil)。
func (r *archiveItemRunner) minHotPK(item *model.ArchiveJobItem) (any, bool, error) {
	q := r.applyRange(r.hot.Table(item.TargetTable), item)
	row := q.Select("MIN(" + r.dom.pkColumn + ")").Row()
	if r.dom.pkKind == archivePKInt {
		var v sql.NullInt64
		if err := row.Scan(&v); err != nil {
			return nil, false, err
		}
		if !v.Valid {
			return nil, false, nil
		}
		return v.Int64, true, nil
	}
	var v sql.NullString
	if err := row.Scan(&v); err != nil {
		return nil, false, err
	}
	if !v.Valid {
		return nil, false, nil
	}
	return v.String, true, nil
}

// orderedPKs 取区间内主键升序全集（供确定性抽样）；读主键列（背景 verify、量有界于抽样需要）。
func (r *archiveItemRunner) orderedPKs(db *gorm.DB, item *model.ArchiveJobItem, lower any) ([]any, error) {
	q := r.applyRange(db.Table(item.TargetTable), item)
	if lower != nil {
		q = q.Where(r.dom.pkColumn+" >= ?", lower)
	}
	q = q.Order(r.dom.pkColumn + " ASC")
	return pluckPKs(q, r.dom.pkColumn, r.dom.pkKind)
}

// hashRows 对给定主键集在 db 侧的行做规范序列化后算 sha256（列名字典序、行按主键升序）。
func (r *archiveItemRunner) hashRows(db *gorm.DB, tableName string, samplePKs []any) (string, error) {
	h := sha256.New()
	if len(samplePKs) == 0 {
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	var rows []map[string]any
	if err := db.Table(tableName).
		Where(r.dom.pkColumn+" IN ?", samplePKs).
		Order(r.dom.pkColumn + " ASC").
		Find(&rows).Error; err != nil {
		return "", err
	}
	for i := range rows {
		h.Write([]byte(canonicalizeRow(rows[i])))
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// applyRange 收口本域的一切行选择条件：附加行过滤（extraWhere）+ 单表形态的发生时间上界
// （daily 无区间过滤，整表为单元）。搬运 SELECT、删除 SELECT、计行、下界、抽样主键集全部经此，
// 域内绝不出现「某条路径漏了 extraWhere」的错配（见 archive_domains.go 的字段注释）。
func (r *archiveItemRunner) applyRange(q *gorm.DB, item *model.ArchiveJobItem) *gorm.DB {
	q = applyDomainFilter(q, r.dom)
	if r.dom.form == archiveFormSingle && item.RangeTo != nil {
		q = q.Where(r.dom.timeColumn+" < ?", *item.RangeTo)
	}
	return q
}

// sleep 批间限流（batchInterval 为 0 时不歇）。
func (r *archiveItemRunner) sleep() {
	if r.batchInterval > 0 {
		time.Sleep(r.batchInterval)
	}
}

// pluckPKs 取一列主键值为 []any（按类型 Pluck 到 int64 / string，规避 mysql 字符串列的 []byte）。
func pluckPKs(q *gorm.DB, pkColumn, pkKind string) ([]any, error) {
	if pkKind == archivePKInt {
		var ids []int64
		if err := q.Pluck(pkColumn, &ids).Error; err != nil {
			return nil, err
		}
		out := make([]any, len(ids))
		for i, v := range ids {
			out[i] = v
		}
		return out, nil
	}
	var ids []string
	if err := q.Pluck(pkColumn, &ids).Error; err != nil {
		return nil, err
	}
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out, nil
}

// pickArchiveSample 用种子在主键升序全集上确定性均匀取样至多 size 个（跨步 + 种子位移，索引严格递增去重）。
func pickArchiveSample(pks []any, size int, seed int64) []any {
	n := len(pks)
	if n == 0 || size <= 0 {
		return nil
	}
	if n <= size {
		return pks
	}
	stride := n / size
	shift := int(uint64(seed) % uint64(stride))
	out := make([]any, 0, size)
	for k := 0; k < size; k++ {
		idx := k*stride + shift
		if idx >= n {
			idx = n - 1
		}
		out = append(out, pks[idx])
	}
	return out
}

// canonicalizeRow 规范序列化一行：列名字典序，col=值 以 0x1f 连接（两侧同引擎同存储，表示一致 → 哈希一致）。
func canonicalizeRow(row map[string]any) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := make([]byte, 0, 256)
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, 0x1f)
		}
		buf = append(buf, k...)
		buf = append(buf, '=')
		buf = append(buf, canonicalValue(row[k])...)
	}
	return string(buf)
}

// canonicalValue 把单元格值转为确定性字符串（时间统一 UTC RFC3339Nano、字节转字符串、nil 占位）。
func canonicalValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "\x00null"
	case []byte:
		return string(t)
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// pkToString 把主键值转为可存 cursor 的字符串（int64 / []byte / string 等）。
func pkToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parsePK 把 cursor 字符串按主键类型转回比较值（int → int64；string → 原串）。
func parsePK(pkKind, cursor string) any {
	if pkKind == archivePKInt {
		n, _ := strconv.ParseInt(cursor, 10, 64)
		return n
	}
	return cursor
}

// stableArchiveSeed 由表名 + 区间上界派生确定性抽样种子（可复算；重试即使未持久化亦一致）。
func stableArchiveSeed(tableName string, rangeTo *time.Time) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tableName))
	if rangeTo != nil {
		_, _ = h.Write([]byte(rangeTo.UTC().Format(time.RFC3339Nano)))
	}
	v := int64(h.Sum64() & 0x7fffffffffffffff)
	if v == 0 {
		v = 1
	}
	return v
}

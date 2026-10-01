package repository

import (
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// utcDaysBetween 返回 [fromMs, toMs] 毫秒区间覆盖的 UTC 日零点序列（含端点日，升序），
// 供日表查询侧按日期枚举候选表名（跨日并表）。from > to 返回空。
func utcDaysBetween(fromMs, toMs int64) []time.Time {
	if fromMs > toMs {
		return nil
	}
	days := make([]time.Time, 0, 4)
	for day := utcDayStart(fromMs); !day.After(utcDayStart(toMs)); day = day.AddDate(0, 0, 1) {
		days = append(days, day)
	}
	return days
}

// msToTime 把毫秒时间戳转为 UTC time.Time，供 DATETIME 列比较（日表按 ms 枚举、行内按 time 过滤）。
func msToTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// existingDailyTablesInRange 枚举 [fromMs,toMs] 覆盖的 UTC 日中**已存在**的日表名（新→旧）。
// 查询侧只判存不建表（HasTable），避免只读查询隐式产生空日表（spec §3.1 归档衔接）。
func existingDailyTablesInRange(db *gorm.DB, base string, fromMs, toMs int64) []string {
	first := utcDayStart(fromMs)
	tables := make([]string, 0, 8)
	for day := utcDayStart(toMs); !day.Before(first); day = day.AddDate(0, 0, -1) {
		name := store.DailyTableName(base, day)
		if db.Migrator().HasTable(name) {
			tables = append(tables, name)
		}
	}
	return tables
}

// existingDailyTablesListed 枚举 [fromMs,toMs] 覆盖的 UTC 日中**已存在**的日表名（新→旧），
// 但走「列出全部表再按前缀 + 日期后缀过滤」而不是逐日 HasTable。
//
// 为什么需要它：时间窗可缺省（如 MCP 工具调用流水的列表端点）时，逐日枚举会从 1970 走到 2100
// ——近 5 万次判存，纯属浪费。列出表是**一次**元数据查询，再按后缀过滤，语义与
// existingDailyTablesInRange 完全一致（同样只判存不建表，绝不隐式产生空日表）。
func existingDailyTablesListed(db *gorm.DB, base string, fromMs, toMs int64) []string {
	if fromMs > toMs {
		return nil
	}
	all, err := db.Migrator().GetTables()
	if err != nil {
		return nil
	}
	first, last := utcDayStart(fromMs), utcDayStart(toMs)
	type ref struct {
		name string
		day  time.Time
	}
	refs := make([]ref, 0, 8)
	for _, name := range all {
		day, ok := dailyDayOf(base, name)
		if !ok || day.Before(first) || day.After(last) {
			continue
		}
		refs = append(refs, ref{name: name, day: day})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].day.After(refs[j].day) })
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.name)
	}
	return names
}

// dailyDayOf 从日表名解析其 UTC 日（base_YYYYMMDD）；非本 base 的日表或后缀非法返回 (zero, false)。
func dailyDayOf(base, table string) (time.Time, bool) {
	prefix := base + "_"
	if !strings.HasPrefix(table, prefix) {
		return time.Time{}, false
	}
	suffix := table[len(prefix):]
	if len(suffix) != 8 {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("20060102", suffix, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// fetchDailyOffsetPage 跨日表按数值 offset 游标分页取一页（newest→oldest），多取一行 peek 判是否还有下一页。
//
// 短路语义（spec §4.3）：逐表倒序，凑满一页（含 peek 的 +1 行）即停、不预扫剩余日表；
// 仅在需要跳过整表时才对该表 Count（offset 已消费到 0 后不再 Count）。order 为已定的
// 「时间降序, 主键降序」子句，apply 给每张表叠加过滤（不含排序/分页）。返回 (本页行, 是否还有下一页, err)。
func fetchDailyOffsetPage[T any](
	db *gorm.DB, tables []string, order string, offset, limit int, apply func(*gorm.DB) *gorm.DB,
) ([]T, bool, error) {
	remainingOffset := offset
	out := make([]T, 0, limit+1)
	for _, tbl := range tables {
		if len(out) > limit {
			break // 已多取到一行，确认还有下一页，停止再扫更旧日表
		}
		// offset 尚未消费完：先数本表行数决定整表跳过还是从表内 offset 起取。
		if remainingOffset > 0 {
			var cnt int64
			if err := apply(db.Table(tbl)).Count(&cnt).Error; err != nil {
				return nil, false, err
			}
			if int64(remainingOffset) >= cnt {
				remainingOffset -= int(cnt)
				continue
			}
		}
		need := (limit + 1) - len(out)
		var chunk []T
		if err := apply(db.Table(tbl)).
			Order(order).Offset(remainingOffset).Limit(need).
			Find(&chunk).Error; err != nil {
			return nil, false, err
		}
		out = append(out, chunk...)
		remainingOffset = 0
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

package service

import (
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 归档域主键类型（决定游标比较 / 排序 / 序列化；FR-151，spec §3.1）。
const (
	archivePKInt    = "int"    // 自增整数主键
	archivePKString = "string" // UUID 字符串主键
)

// 归档域表形态（spec §3.1）：日期后缀表整表为单元 / 单表按区间为单元。
const (
	archiveFormDaily  = "daily"
	archiveFormSingle = "single"
)

// archiveDomain 是单个参与归档的数据域描述（§3.1 域注册表；表结构与命名以属主规格为权威）。
type archiveDomain struct {
	// domain 枚举标识
	name string
	// 基表名（日表=前缀、单表=表名本身）
	baseTable string
	// daily / single
	form string
	// 主键列名
	pkColumn string
	// int / string
	pkKind string
	// 单表的发生时间列（daily 为空）
	timeColumn string
	// 保留期设置键（settings_metadata）
	retentionKey string
	// 建归档表用的空模型指针（提供 GORM 迁移的列定义）
	newModel func() any
	// extraWhere 是附加行过滤（可选，空串=不过滤）：只归档该域中**适合归档**的行，
	// 语义为「本域的业务待办必须留在热库」。当前仅 alert_event 使用（`status = 'resolved'`：
	// 未处理告警是运维待办，搬走会让待办从热库消失）。
	//
	// 安全约定：值必须是**代码内常量**、绝不接受任何外部输入（无用户可控拼接 → 无注入面）。
	// 该条件必须施加在**全部行选择路径**上（搬运 SELECT、热库删除 SELECT、校验 / 预估计数、
	// 区间下界、抽样主键集），否则会出现「按 A 集合搬运、按 B 集合删除」的静默数据丢失。
	// 应用点集中见 applyDomainFilter；域内计数见 domainRowCount 与 expandItems。
	extraWhere string
}

// archiveDomains 是 §3.1 的域注册表（9 域，稳定顺序）：7 张日期后缀表 + 2 张单表（audit / alert_event）。
var archiveDomains = []archiveDomain{
	{
		name: "metric_sample", baseTable: "metric_sample", form: archiveFormDaily,
		pkColumn: "id", pkKind: archivePKInt, retentionKey: SettingArchiveRetentionMetricSample,
		newModel: func() any { return &model.MetricSampleV2{} },
	},
	{
		name: "health_snapshot", baseTable: "health_snapshot", form: archiveFormDaily,
		pkColumn: "id", pkKind: archivePKInt, retentionKey: SettingArchiveRetentionHealthSnapshot,
		newModel: func() any { return &model.HealthSnapshot{} },
	},
	{
		name: "sched_decision", baseTable: "sched_decision", form: archiveFormDaily,
		pkColumn: "id", pkKind: archivePKInt, retentionKey: SettingArchiveRetentionSchedDecision,
		newModel: func() any { return &model.SchedDecisionV2{} },
	},
	{
		name: "conn_detail", baseTable: "conn_detail", form: archiveFormDaily,
		pkColumn: "conn_id", pkKind: archivePKString, retentionKey: SettingArchiveRetentionConnDetail,
		newModel: func() any { return &model.ConnDetail{} },
	},
	{
		name: "msg_trace", baseTable: "msg_trace", form: archiveFormDaily,
		pkColumn: "message_id", pkKind: archivePKString, retentionKey: SettingArchiveRetentionMsgTrace,
		newModel: func() any { return &model.MsgTrace{} },
	},
	{
		name: "msg_payload", baseTable: "msg_payload", form: archiveFormDaily,
		pkColumn: "message_id", pkKind: archivePKString, retentionKey: SettingArchiveRetentionMsgPayload,
		newModel: func() any { return &model.MsgPayload{} },
	},
	{
		// MCP 工具调用流水（FR-240）：日表形态、UUIDv7 字符串主键，天然无半表状态，无需新机制。
		name: "mcp_invocation", baseTable: "mcp_invocation", form: archiveFormDaily,
		pkColumn: "invocation_id", pkKind: archivePKString, retentionKey: SettingArchiveRetentionMCPInvocation,
		newModel: func() any { return &model.MCPInvocation{} },
	},
	{
		name: "audit", baseTable: "audit_log", form: archiveFormSingle,
		pkColumn: "id", pkKind: archivePKInt, timeColumn: "created_at",
		retentionKey: SettingArchiveRetentionAudit,
		newModel:     func() any { return &model.AuditLog{} },
	},
	{
		// 告警事件表此前只增不减（FR-89 留痕表），纳入归档域后由保留期收敛体量；追加在末尾，不动既有顺序语义。
		name: "alert_event", baseTable: "alert_event", form: archiveFormSingle,
		pkColumn: "id", pkKind: archivePKInt, timeColumn: "created_at",
		retentionKey: SettingArchiveRetentionAlertEvent,
		newModel:     func() any { return &model.AlertEvent{} },
		// 只归档已处理的行：未处理（open / acknowledged）告警是运维待办，必须留在热库让运维看到。
		extraWhere: "status = 'resolved'",
	},
}

// applyDomainFilter 施加域的附加行过滤（extraWhere，空串为恒等）：**所有**行选择路径都必须经此收口
// （搬选取数 / 删除选取 / 计行 / 下界 / 抽样），保证「搬运集合 ≡ 删除集合」。
func applyDomainFilter(q *gorm.DB, dom archiveDomain) *gorm.DB {
	if dom.extraWhere != "" {
		q = q.Where(dom.extraWhere)
	}
	return q
}

// archiveDomainByName 按名查域描述；未知返回 (zero, false)。
func archiveDomainByName(name string) (archiveDomain, bool) {
	for _, d := range archiveDomains {
		if d.name == name {
			return d, true
		}
	}
	return archiveDomain{}, false
}

// isValidArchiveDomain 校验 domain 是否在注册表内。
func isValidArchiveDomain(name string) bool {
	_, ok := archiveDomainByName(name)
	return ok
}

// dailyTableRef 是一张到期日表的引用（表名 + 其 UTC 日）。
type dailyTableRef struct {
	name string
	day  time.Time
}

// cutoffFor 按 now 与保留天数算 cutoff（当日 UTC 0 点 − 保留天数，落在日边界；spec §3.1）。
// 日表：归档日期严格早于 cutoff 的表；单表：删除发生时间 < cutoff 的行。
func cutoffFor(now time.Time, retentionDays int) time.Time {
	u := now.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -retentionDays)
}

// parseDailySuffix 从表名解析日期后缀（base_YYYYMMDD）；非本 base 的日表或后缀非法返回 (zero, false)。
func parseDailySuffix(base, table string) (time.Time, bool) {
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

// expiredDailyTables 枚举 db 中属于 base、且日期严格早于 cutoff 的已存在日表（按日升序）。
// 用 GORM Migrator.GetTables 列表（portable，非方言专有 SQL），按前缀 + 日期后缀过滤。
func expiredDailyTables(db *gorm.DB, base string, cutoff time.Time) ([]dailyTableRef, error) {
	all, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	refs := make([]dailyTableRef, 0, 8)
	for _, t := range all {
		if day, ok := parseDailySuffix(base, t); ok && day.Before(cutoff) {
			refs = append(refs, dailyTableRef{name: t, day: day})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].day.Before(refs[j].day) })
	return refs, nil
}

// allDailyTables 枚举 db 中属于 base 的全部已存在日表名（供 overview 汇总体量）。
func allDailyTables(db *gorm.DB, base string) ([]string, error) {
	all, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, 8)
	for _, t := range all {
		if _, ok := parseDailySuffix(base, t); ok {
			names = append(names, t)
		}
	}
	return names, nil
}

// ensureArchiveTable 按需在归档连接建同名同构表（幂等）：不存在则用 model 定义 CreateTable，
// 并发 / 已存在竞态下再判存一次容错。与 store.EnsureDailyTable 同精神，但按显式表名（日表 / 单表统一）。
// DDL 在 MySQL 触发隐式提交，故必须在事务外调用。
func ensureArchiveTable(db *gorm.DB, tableName string, m any) error {
	if db.Migrator().HasTable(tableName) {
		return nil
	}
	if err := db.Table(tableName).Migrator().CreateTable(m); err != nil {
		if !db.Migrator().HasTable(tableName) {
			return err
		}
	}
	return nil
}

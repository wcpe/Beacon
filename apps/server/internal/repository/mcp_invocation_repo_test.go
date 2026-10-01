package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// invocationAt 构造一行流水：主键用 store.NewUUIDv7 生成（内嵌毫秒即日表路由依据），
// createdAt 与之内嵌毫秒同源——与 middleware 的产出形态一致。
func invocationAt(ms int64, clientID, tool string, mutate func(*model.MCPInvocation)) model.MCPInvocation {
	row := model.MCPInvocation{
		InvocationID: store.NewUUIDv7(ms),
		ClientID:     clientID,
		Profile:      model.MCPClientProfileAutomation,
		ToolName:     tool,
		RiskLevel:    model.MCPInvocationRiskLow,
		Result:       model.MCPInvocationResultOK,
		ArgBytes:     2,
		DurationMs:   1,
		TraceID:      "0123456789abcdef",
		ClientIP:     "10.0.0.7",
		CreatedAt:    time.UnixMilli(ms).UTC(),
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

// TestMCPInvocationFlushDailySplitsDays 校验按主键内嵌 UUIDv7 的 UTC 日拆表：跨日批各落各自当日表。
func TestMCPInvocationFlushDailySplitsDays(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_split")
	repo := NewMCPInvocationRepository(db)
	today := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	todayTable := store.DailyTableName("mcp_invocation", today)
	yesterdayTable := store.DailyTableName("mcp_invocation", yesterday)

	batch := []model.MCPInvocation{
		invocationAt(today.UnixMilli(), "c1", "beacon.audit.events.list", nil),
		invocationAt(today.Add(time.Second).UnixMilli(), "c1", "beacon.metrics.summary.get", nil),
		invocationAt(yesterday.UnixMilli(), "c2", "beacon.audit.events.list", nil),
	}
	dedup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if dedup != 0 {
		t.Fatalf("首次写入不应有去重，实际 %d", dedup)
	}
	if got := countDaily(t, db, todayTable); got != 2 {
		t.Fatalf("今日表行数=%d，期望 2", got)
	}
	if got := countDaily(t, db, yesterdayTable); got != 1 {
		t.Fatalf("昨日表行数=%d，期望 1", got)
	}
}

// TestMCPInvocationFlushDailyIdempotentReplay 校验重放同批幂等（OnConflict DoNothing）：
// 重放被计数去重、行数不增，且不入全局 AutoMigrate 之外的表。
func TestMCPInvocationFlushDailyIdempotentReplay(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_replay")
	repo := NewMCPInvocationRepository(db)
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	table := store.DailyTableName("mcp_invocation", day)

	batch := []model.MCPInvocation{
		invocationAt(day.UnixMilli(), "c1", "beacon.audit.events.list", nil),
		invocationAt(day.Add(time.Second).UnixMilli(), "c1", "beacon.audit.events.list", nil),
	}
	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	dedup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("重放写入失败: %v", err)
	}
	if dedup != 2 {
		t.Fatalf("重放去重数=%d，期望 2", dedup)
	}
	if got := countDaily(t, db, table); got != 2 {
		t.Fatalf("重放后行数=%d，期望仍为 2", got)
	}
}

// TestMCPInvocationFlushDailySkipsUnparsableID 校验无法解析日的主键被跳过并计入去重（兜底，不写错表）。
func TestMCPInvocationFlushDailySkipsUnparsableID(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_badid")
	repo := NewMCPInvocationRepository(db)
	dedup, err := repo.FlushDaily([]model.MCPInvocation{{InvocationID: "not-a-uuid", ToolName: "x"}})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if dedup != 1 {
		t.Fatalf("非法主键应计入去重 1，实际 %d", dedup)
	}
}

// TestMCPInvocationFindByInvocationID 校验按主键直定日表：命中返回同行；非法 ID / 缺日表 → (nil, nil)。
func TestMCPInvocationFindByInvocationID(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_find")
	repo := NewMCPInvocationRepository(db)
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	row := invocationAt(day.UnixMilli(), "c1", "beacon.files.create",
		func(r *model.MCPInvocation) {
			r.Result = model.MCPInvocationResultRejected
			r.Reason = model.MCPInvocationReasonProductionMode
			r.RiskLevel = model.MCPInvocationRiskCritical
			r.ArgKeys = "content:1180,path"
			r.TargetDigest = "path=/plugins/x.jar"
			r.ErrorSummary = "生产模式已禁用 critical 风险等级工具：beacon.files.create"
		})
	if _, err := repo.FlushDaily([]model.MCPInvocation{row}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := repo.FindByInvocationID(row.InvocationID)
	if err != nil {
		t.Fatalf("直查失败: %v", err)
	}
	if got == nil {
		t.Fatalf("命中行不应为 nil")
	}
	if got.InvocationID != row.InvocationID || got.ArgKeys != row.ArgKeys || got.ArgBytes != row.ArgBytes ||
		got.Result != row.Result || got.Reason != row.Reason || got.TargetDigest != row.TargetDigest ||
		got.ErrorSummary != row.ErrorSummary || got.RiskLevel != row.RiskLevel {
		t.Fatalf("直查行与写入行不一致:\n写入 %+v\n读回 %+v", row, *got)
	}

	// 非法 ID（非 UUIDv7 文本）→ (nil, nil)。
	if row, err := repo.FindByInvocationID("garbage"); err != nil || row != nil {
		t.Fatalf("非法 ID 应返回 (nil,nil)，实际 (%v,%v)", row, err)
	}
	// 已存在日表但无此行 → (nil, nil)。
	if row, err := repo.FindByInvocationID(store.NewUUIDv7(day.UnixMilli())); err != nil || row != nil {
		t.Fatalf("无此行应返回 (nil,nil)，实际 (%v,%v)", row, err)
	}
	// 日表不存在（很久以前的日）→ (nil, nil)，且**不得**隐式建表。
	absentDay := day.AddDate(0, 0, -30)
	absentTable := store.DailyTableName("mcp_invocation", absentDay)
	if row, err := repo.FindByInvocationID(store.NewUUIDv7(absentDay.UnixMilli())); err != nil || row != nil {
		t.Fatalf("缺日表应返回 (nil,nil)，实际 (%v,%v)", row, err)
	}
	if db.Migrator().HasTable(absentTable) {
		t.Fatalf("查询侧不得隐式建日表: %s", absentTable)
	}
}

// TestMCPInvocationQueryCrossDayAndFilters 校验跨日可见 + 六维过滤 + 游标分页（§5 第 6/7 条）。
func TestMCPInvocationQueryCrossDayAndFilters(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_query")
	repo := NewMCPInvocationRepository(db)
	today := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	seed := []model.MCPInvocation{
		invocationAt(yesterday.UnixMilli(), "c1", "beacon.audit.events.list", nil),
		invocationAt(today.UnixMilli(), "c1", "beacon.audit.events.list", nil),
		invocationAt(today.Add(time.Minute).UnixMilli(), "c2", "beacon.files.create",
			func(r *model.MCPInvocation) {
				r.RiskLevel = model.MCPInvocationRiskHigh
				r.Result = model.MCPInvocationResultRejected
				r.Reason = model.MCPInvocationReasonHandlerRejected
			}),
		invocationAt(today.Add(2*time.Minute).UnixMilli(), "c2", "beacon.system.update.apply",
			func(r *model.MCPInvocation) {
				r.RiskLevel = model.MCPInvocationRiskCritical
				r.Result = model.MCPInvocationResultRejected
				r.Reason = model.MCPInvocationReasonProductionMode
			}),
		invocationAt(today.Add(3*time.Minute).UnixMilli(), "c3", "beacon.unregistered.probe",
			func(r *model.MCPInvocation) {
				r.RiskLevel = model.MCPInvocationRiskUnknown
				r.Result = model.MCPInvocationResultRejected
				r.Reason = model.MCPInvocationReasonUnknownTool
			}),
		invocationAt(today.Add(4*time.Minute).UnixMilli(), "c3", "beacon.files.create",
			func(r *model.MCPInvocation) {
				r.RiskLevel = model.MCPInvocationRiskHigh
				r.Result = model.MCPInvocationResultFail
				r.Reason = model.MCPInvocationReasonHandlerError
			}),
	}
	if _, err := repo.FlushDaily(seed); err != nil {
		t.Fatalf("写入种子失败: %v", err)
	}

	fullRange := MCPInvocationQuery{
		FromMs: yesterday.Add(-time.Hour).UnixMilli(),
		ToMs:   today.Add(time.Hour).UnixMilli(),
		Limit:  10,
	}

	// §5 第 7 条：不带时间窗（覆盖全范围）的列表两日都在。
	all, hasMore, err := repo.QueryInvocations(fullRange)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(all) != len(seed) || hasMore {
		t.Fatalf("跨日列表行数=%d（hasMore=%v），期望 %d 行且无下一页", len(all), hasMore, len(seed))
	}
	// created_at 降序。
	for i := 1; i < len(all); i++ {
		if all[i-1].CreatedAt.Before(all[i].CreatedAt) {
			t.Fatalf("列表未按 created_at 降序: %v < %v", all[i-1].CreatedAt, all[i].CreatedAt)
		}
	}
	// 带昨日时间窗只返回昨日行。
	onlyYesterday, _, err := repo.QueryInvocations(MCPInvocationQuery{
		FromMs: yesterday.Add(-time.Hour).UnixMilli(),
		ToMs:   yesterday.Add(time.Hour).UnixMilli(),
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("昨日窗口查询失败: %v", err)
	}
	if len(onlyYesterday) != 1 || onlyYesterday[0].ClientID != "c1" ||
		onlyYesterday[0].CreatedAt.UTC().Day() != yesterday.Day() {
		t.Fatalf("昨日时间窗应只返回昨日 1 行，实际 %+v", onlyYesterday)
	}

	// 逐维等值过滤与预置行精确一致。
	cases := []struct {
		name  string
		query MCPInvocationQuery
		want  int
	}{
		{"按工具", MCPInvocationQuery{ToolName: "beacon.files.create"}, 2},
		{"未登记工具名可查", MCPInvocationQuery{ToolName: "beacon.unregistered.probe"}, 1},
		{"按 clientId", MCPInvocationQuery{ClientID: "c2"}, 2},
		{"按结果", MCPInvocationQuery{Result: model.MCPInvocationResultRejected}, 3},
		{"按结果（fail）", MCPInvocationQuery{Result: model.MCPInvocationResultFail}, 1},
		{"按风险等级", MCPInvocationQuery{RiskLevel: model.MCPInvocationRiskCritical}, 1},
		{"按原因", MCPInvocationQuery{Reason: model.MCPInvocationReasonUnknownTool}, 1},
		{"多维多值组合", MCPInvocationQuery{ClientID: "c2", Result: model.MCPInvocationResultRejected, Reason: model.MCPInvocationReasonProductionMode}, 1},
		{"无命中", MCPInvocationQuery{ClientID: "nobody"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.query
			q.FromMs, q.ToMs, q.Limit = fullRange.FromMs, fullRange.ToMs, 10
			rows, _, err := repo.QueryInvocations(q)
			if err != nil {
				t.Fatalf("过滤查询失败: %v", err)
			}
			if len(rows) != tc.want {
				t.Fatalf("命中行数=%d，期望 %d: %+v", len(rows), tc.want, rows)
			}
		})
	}

	// 游标分页：limit 越小越可续，逐页无重复无遗漏。
	seen := map[string]struct{}{}
	for offset := 0; offset < len(seed); offset += 2 {
		page, more, err := repo.QueryInvocations(MCPInvocationQuery{
			FromMs: fullRange.FromMs, ToMs: fullRange.ToMs, Offset: offset, Limit: 2,
		})
		if err != nil {
			t.Fatalf("分页查询失败: %v", err)
		}
		if len(page) > 2 {
			t.Fatalf("单页行数=%d 超 limit", len(page))
		}
		for _, row := range page {
			if _, dup := seen[row.InvocationID]; dup {
				t.Fatalf("分页出现重复行: %s", row.InvocationID)
			}
			seen[row.InvocationID] = struct{}{}
		}
		if offset+2 >= len(seed) && more {
			t.Fatalf("已到末页但仍报还有下一页")
		}
	}
	if len(seen) != len(seed) {
		t.Fatalf("分页覆盖 %d 行，期望 %d 行（不得遗漏）", len(seen), len(seed))
	}
}

// TestMCPInvocationQueryOnlyExistingTables 校验查询侧只判存不建表（spec §3.7）：
// 空库上查询不得产生任何 mcp_invocation_* 日表。
func TestMCPInvocationQueryOnlyExistingTables(t *testing.T) {
	db := openRepoSQLite(t, "mcp_invocation_noimplicit")
	repo := NewMCPInvocationRepository(db)
	now := time.Now().UTC()
	rows, hasMore, err := repo.QueryInvocations(MCPInvocationQuery{
		FromMs: now.AddDate(0, 0, -3).UnixMilli(), ToMs: now.UnixMilli(), Limit: 10,
	})
	if err != nil {
		t.Fatalf("空库查询不应报错: %v", err)
	}
	if len(rows) != 0 || hasMore {
		t.Fatalf("空库应返回空页，实际 %d 行 hasMore=%v", len(rows), hasMore)
	}
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("列举表失败: %v", err)
	}
	for _, name := range tables {
		if _, ok := parseDailySuffixForTest("mcp_invocation", name); ok {
			t.Fatalf("查询侧隐式建了日表: %s", name)
		}
	}
}

// parseDailySuffixForTest 判定表名是否属目标的日表（前缀 + 8 位日期后缀）。
func parseDailySuffixForTest(base, table string) (string, bool) {
	prefix := base + "_"
	if len(table) != len(prefix)+8 || table[:len(prefix)] != prefix {
		return "", false
	}
	return table, true
}

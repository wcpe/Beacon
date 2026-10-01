package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// mcpInvocationServiceSQLite 打开一个内存库并构造流水服务（writer 可选注入）。
func mcpInvocationServiceSQLite(t *testing.T, name string) (*gorm.DB, *MCPInvocationService, *repository.MCPInvocationRepository) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	repo := repository.NewMCPInvocationRepository(db)
	return db, NewMCPInvocationService(nil, repo), repo
}

// mcpInvocationRowAt 构造一行流水（内嵌毫秒与 createdAt 同源）。
func mcpInvocationRowAt(ms int64, clientID, tool string, mutate func(*model.MCPInvocation)) model.MCPInvocation {
	row := model.MCPInvocation{
		InvocationID: store.NewUUIDv7(ms), ClientID: clientID, Profile: model.MCPClientProfileAutomation,
		ToolName: tool, RiskLevel: model.MCPInvocationRiskLow, Result: model.MCPInvocationResultOK,
		CreatedAt: time.UnixMilli(ms).UTC(),
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

// TestMCPInvocationRecordDropsWithoutWriter 验证写入通道未就绪（Writer 为 nil / 路由未注册）时：
// Record 不 panic、不阻塞、只累计丢弃计数——请求路径绝不因流水失败而受影响（§3.5 / §5 第 9 条）。
func TestMCPInvocationRecordDropsWithoutWriter(t *testing.T) {
	cases := []struct {
		name   string
		writer *AsyncDailyWriter
	}{
		{"Writer 为 nil（未装配）", nil},
		{"通道存在但路由未注册", NewAsyncDailyWriter()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewMCPInvocationService(tc.writer, nil)
			start := time.Now()
			svc.Record(model.MCPInvocation{ToolName: "beacon.audit.events.list", ClientID: "c1"})
			if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
				t.Fatalf("丢弃路径耗时 %v，不应阻塞主路径（< 10ms）", elapsed)
			}
			if got := svc.Dropped(); got != 1 {
				t.Fatalf("累计丢弃数=%d，期望 1", got)
			}
		})
	}
}

// TestMCPInvocationRecordDropsOnFullQueue 验证队列塞满时丢弃该行并计数，且仍是微秒级非阻塞路径。
func TestMCPInvocationRecordDropsOnFullQueue(t *testing.T) {
	writer := NewAsyncDailyWriter()
	writer.queueCapacity = 1
	// 注册一个从不消费的假 flusher：不 Start，队列自然积压到满。
	RegisterFlusher(writer, RouteKindMCPInvocation, func([]model.MCPInvocation) (int, error) { return 0, nil })
	svc := NewMCPInvocationService(writer, nil)

	if !EnqueueRows(writer, RouteKindMCPInvocation, []model.MCPInvocation{{ToolName: "fill"}}) {
		t.Fatalf("首次入队应成功（占用唯一队列位）")
	}
	start := time.Now()
	svc.Record(model.MCPInvocation{ToolName: "beacon.audit.events.list"})
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("队列满路径耗时 %v，不应阻塞主路径（< 10ms）", elapsed)
	}
	if got := svc.Dropped(); got != 1 {
		t.Fatalf("队列满应计一次丢弃，实际 %d", got)
	}
}

// TestMCPInvocationRecordDeliversToRepo 验证正常路径：Record 经真实写入通道最终落当日日表。
//
// 先记后查的时序由 flusher 的信号量兜住；写入协程在断言完成后显式 cancel 并等其退出，
// 避免测试结束时后台协程仍在用已关闭的连接（那只会产生噪声日志、掩盖真实失败）。
func TestMCPInvocationRecordDeliversToRepo(t *testing.T) {
	db, _, repo := mcpInvocationServiceSQLite(t, "mcp_invocation_record")
	writer := NewAsyncDailyWriter()
	// 攒批超时调小，让单行也能很快刷盘（工程参数，测试可调）。
	writer.flushInterval = 20 * time.Millisecond
	flushed := make(chan struct{}, 4)
	RegisterFlusher(writer, RouteKindMCPInvocation, func(rows []model.MCPInvocation) (int, error) {
		n, err := repo.FlushDaily(rows)
		if err == nil {
			select {
			case flushed <- struct{}{}:
			default:
			}
		}
		return n, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)
	defer func() {
		cancel()
		// 等写入协程观察到取消并退出，再让 t.Cleanup 关库。
		time.Sleep(50 * time.Millisecond)
	}()
	svc := NewMCPInvocationService(writer, repo)

	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	svc.Record(mcpInvocationRowAt(day.UnixMilli(), "c1", "beacon.audit.events.list", nil))

	select {
	case <-flushed:
	case <-time.After(3 * time.Second):
		t.Fatalf("流水未在超时内刷盘（丢弃数 %d）", svc.Dropped())
	}
	table := store.DailyTableName("mcp_invocation", day)
	if got := countRowsIn(t, db, table); got != 1 {
		t.Fatalf("当日日表行数=%d，期望 1", got)
	}
	if got := svc.Dropped(); got != 0 {
		t.Fatalf("正常路径不应丢弃，实际 %d", got)
	}
}

// countRowsIn 统计指定表行数（表不存在视为 0，供「尚未建表」的轮询场景）。
func countRowsIn(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	if !db.Migrator().HasTable(table) {
		return 0
	}
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s 行数失败: %v", table, err)
	}
	return n
}

// TestMCPInvocationListValidation 逐条核对列表查询的参数校验（spec §3.7）：
// 非法枚举值 400 INVALID_PARAM；from > to 400。
func TestMCPInvocationListValidation(t *testing.T) {
	_, svc, _ := mcpInvocationServiceSQLite(t, "mcp_invocation_validate")
	base := MCPInvocationListParams{}

	cases := []struct {
		name    string
		mutate  func(*MCPInvocationListParams)
		wantErr bool
	}{
		{"全部空参数合法", func(*MCPInvocationListParams) {}, false},
		{"result 合法值 ok", func(p *MCPInvocationListParams) { p.Result = model.MCPInvocationResultOK }, false},
		{"result 合法值 rejected", func(p *MCPInvocationListParams) { p.Result = model.MCPInvocationResultRejected }, false},
		{"result 非法值", func(p *MCPInvocationListParams) { p.Result = "success" }, true},
		{"riskLevel 合法值 unknown", func(p *MCPInvocationListParams) { p.RiskLevel = model.MCPInvocationRiskUnknown }, false},
		{"riskLevel 非法值", func(p *MCPInvocationListParams) { p.RiskLevel = "medium" }, true},
		{"reason 合法值 production_mode", func(p *MCPInvocationListParams) { p.Reason = model.MCPInvocationReasonProductionMode }, false},
		{"reason 非法值", func(p *MCPInvocationListParams) { p.Reason = "whatever" }, true},
		{"from > to", func(p *MCPInvocationListParams) { p.FromMs, p.ToMs = 2000, 1000 }, true},
		{"from == to 合法", func(p *MCPInvocationListParams) { p.FromMs, p.ToMs = 1000, 1000 }, false},
		{"只给 from 合法", func(p *MCPInvocationListParams) { p.FromMs = 1000 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			_, err := svc.List(p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应拒绝该参数组合")
				}
				ae, ok := err.(*apperr.Error)
				if !ok || ae.Code != apperr.ErrInvalidParam.Code {
					t.Fatalf("应返回 %s，实际 %v", apperr.ErrInvalidParam.Code, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("合法参数不应报错: %v", err)
			}
		})
	}
}

// TestMCPInvocationListLimitClamping 校验 limit 的服务端规整：≤0 取默认 20、>100 收 100（spec §3.7）。
func TestMCPInvocationListLimitClamping(t *testing.T) {
	db, svc, repo := mcpInvocationServiceSQLite(t, "mcp_invocation_limit")
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	seed := make([]model.MCPInvocation, 0, 120)
	for i := 0; i < 120; i++ {
		seed = append(seed, mcpInvocationRowAt(day.Add(time.Duration(i)*time.Second).UnixMilli(), "c1", "beacon.audit.events.list", nil))
	}
	if _, err := repo.FlushDaily(seed); err != nil {
		t.Fatalf("写入种子失败: %v", err)
	}
	_ = db

	cases := []struct {
		name      string
		limit     int
		wantItems int
		wantNext  bool
	}{
		{"limit=0 取默认 20", 0, 20, true},
		{"limit 负数取默认 20", -5, 20, true},
		{"limit=100 合法上限", 100, 100, true},
		{"limit=500 收敛到 100", 500, 100, true},
		{"limit=7 按需返回", 7, 7, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.List(MCPInvocationListParams{Limit: tc.limit})
			if err != nil {
				t.Fatalf("列表查询失败: %v", err)
			}
			if len(page.Items) != tc.wantItems {
				t.Fatalf("本页行数=%d，期望 %d", len(page.Items), tc.wantItems)
			}
			if (page.NextCursor != "") != tc.wantNext {
				t.Fatalf("nextCursor=%q，期望非空=%v", page.NextCursor, tc.wantNext)
			}
		})
	}
}

// TestMCPInvocationListCursorPagingNoGapNoDup 校验游标分页逐页无重复无遗漏（§5 第 6 条）。
func TestMCPInvocationListCursorPagingNoGapNoDup(t *testing.T) {
	_, svc, repo := mcpInvocationServiceSQLite(t, "mcp_invocation_cursor")
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	seed := make([]model.MCPInvocation, 0, 9)
	for i := 0; i < 9; i++ {
		seed = append(seed, mcpInvocationRowAt(day.Add(time.Duration(i)*time.Second).UnixMilli(), "c1", "beacon.audit.events.list", nil))
	}
	if _, err := repo.FlushDaily(seed); err != nil {
		t.Fatalf("写入种子失败: %v", err)
	}

	cursor, seen, pages := "", map[string]struct{}{}, 0
	for {
		page, err := svc.List(MCPInvocationListParams{Limit: 2, Cursor: intOf(cursor)})
		if err != nil {
			t.Fatalf("分页查询失败: %v", err)
		}
		for _, row := range page.Items {
			if _, dup := seen[row.InvocationID]; dup {
				t.Fatalf("分页出现重复行: %s", row.InvocationID)
			}
			seen[row.InvocationID] = struct{}{}
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		if pages > 10 {
			t.Fatalf("分页未收敛（nextCursor 一直非空）")
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(seed) {
		t.Fatalf("分页覆盖 %d 行，期望 %d 行（不得遗漏）", len(seen), len(seed))
	}
}

// intOf 把游标令牌转为 offset（空串 = 首页）。
func intOf(cursor string) int {
	if cursor == "" {
		return 0
	}
	n := 0
	for _, c := range cursor {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// TestMCPInvocationDetail 校验详情：命中返回同行；非法 ID / 缺日表一律 mcp_invocation_not_found（§5 第 8 条）。
func TestMCPInvocationDetail(t *testing.T) {
	_, svc, repo := mcpInvocationServiceSQLite(t, "mcp_invocation_detail")
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	row := mcpInvocationRowAt(day.UnixMilli(), "c1", "beacon.files.create", func(r *model.MCPInvocation) {
		r.Result = model.MCPInvocationResultRejected
		r.Reason = model.MCPInvocationReasonProductionMode
		r.RiskLevel = model.MCPInvocationRiskCritical
		r.ArgKeys = "content:1180,path"
		r.TargetDigest = "path=/plugins/x.jar"
	})
	if _, err := repo.FlushDaily([]model.MCPInvocation{row}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := svc.Detail(row.InvocationID)
	if err != nil {
		t.Fatalf("详情查询失败: %v", err)
	}
	if got.InvocationID != row.InvocationID || got.ArgKeys != row.ArgKeys || got.TargetDigest != row.TargetDigest {
		t.Fatalf("详情行与写入行不一致:\n写入 %+v\n读回 %+v", row, got)
	}

	cases := []struct {
		name string
		id   string
	}{
		{"空 ID", ""},
		{"乱造 ID", "not-a-uuid"},
		{"超长 ID", "0123456789abcdef0123456789abcdef0123"},
		{"合法 UUIDv7 但无此行", store.NewUUIDv7(day.UnixMilli())},
		{"已归档日（日表不存在）", store.NewUUIDv7(day.AddDate(0, 0, -400).UnixMilli())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Detail(tc.id)
			ae, ok := err.(*apperr.Error)
			if !ok || ae.Code != apperr.ErrMCPInvocationNotFound.Code {
				t.Fatalf("应返回 %s（404），实际 %v", apperr.ErrMCPInvocationNotFound.Code, err)
			}
		})
	}
}

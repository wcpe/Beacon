package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newCommandObserveTestService 背靠内存 sqlite 构造 CommandObserveService，并返回底层 DB 以便播种命令行。
func newCommandObserveTestService(t *testing.T) (*CommandObserveService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatalf("迁移 agent_command 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM agent_command").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return NewCommandObserveService(repository.NewAgentCommandRepository(db)), db
}

// seedCommand 追加一条命令（指定 namespace/serverId/type/status/createdAt），瞬态敏感字段填值以验观测不带出。
func seedCommand(t *testing.T, db *gorm.DB, ns, serverID, cmdType, status string, at time.Time) {
	t.Helper()
	if err := db.Create(&model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID, Type: cmdType, Status: status,
		Payload: `{"scope":"group"}`, ResultDetail: "summary",
		ImprintContent: "SECRET-IMPRINT", LogContent: "SECRET-LOG",
		Operator: "admin", CreatedAt: at,
	}).Error; err != nil {
		t.Fatalf("写命令失败: %v", err)
	}
}

// TestObserveListRejectsInvalidEnum type/status 非法枚举返参数错误（handler 转 400）。
func TestObserveListRejectsInvalidEnum(t *testing.T) {
	svc, _ := newCommandObserveTestService(t)
	if _, _, err := svc.List(repository.CommandFilter{Type: "bogus-type"}); err != apperr.ErrInvalidParam {
		t.Fatalf("非法 type 应返 ErrInvalidParam，实际 %v", err)
	}
	if _, _, err := svc.List(repository.CommandFilter{Status: "bogus-status"}); err != apperr.ErrInvalidParam {
		t.Fatalf("非法 status 应返 ErrInvalidParam，实际 %v", err)
	}
	// 合法枚举（空表无命中）不报错。
	if _, _, err := svc.List(repository.CommandFilter{Type: model.CommandTypeTailLogs, Status: model.CommandStatusPending}); err != nil {
		t.Fatalf("合法枚举不应报错，实际 %v", err)
	}
}

// TestObserveListPagingDefaults page/size 缺省与上限规整。
func TestObserveListPagingDefaults(t *testing.T) {
	svc, db := newCommandObserveTestService(t)
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		seedCommand(t, db, "prod", "lobby-1", model.CommandTypeIngestPlugins, model.CommandStatusDone, now.Add(time.Duration(-i)*time.Minute))
	}
	// size=0 → 缺省 20；page=0 → 第 1 页。
	items, total, err := svc.List(repository.CommandFilter{Namespace: "prod"})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 25 || len(items) != defaultCommandPageSize {
		t.Fatalf("缺省应 total=25 页大小 20，实际 total=%d len=%d", total, len(items))
	}
}

// TestObserveAnalyticsAggregates 验证窗口内聚合：total / byStatus / byType / byServer / byDay（含下发·完成·失败分桶）。
func TestObserveAnalyticsAggregates(t *testing.T) {
	svc, db := newCommandObserveTestService(t)
	d1 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	// d1：ingest done + tail-logs failed（lobby-1）
	seedCommand(t, db, "prod", "lobby-1", model.CommandTypeIngestPlugins, model.CommandStatusDone, d1)
	seedCommand(t, db, "prod", "lobby-1", model.CommandTypeTailLogs, model.CommandStatusFailed, d1)
	// d2：ingest done（lobby-2）+ resync expired（lobby-1）+ ingest pending（lobby-2）
	seedCommand(t, db, "prod", "lobby-2", model.CommandTypeIngestPlugins, model.CommandStatusDone, d2)
	seedCommand(t, db, "prod", "lobby-1", model.CommandTypeResyncConfig, model.CommandStatusExpired, d2)
	seedCommand(t, db, "prod", "lobby-2", model.CommandTypeIngestPlugins, model.CommandStatusPending, d2)

	res, err := svc.Analytics(repository.CommandFilter{
		From: time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 5 {
		t.Fatalf("total 应 5，实际 %d", res.Total)
	}
	// byType 降序：ingest-plugins(3) 在前。
	if res.ByType[0].Type != model.CommandTypeIngestPlugins || res.ByType[0].Count != 3 {
		t.Fatalf("byType 首项应 ingest-plugins=3，实际 %+v", res.ByType)
	}
	// byStatus：done=2、failed=1、expired=1、pending=1。
	gotStatus := map[string]int{}
	for _, c := range res.ByStatus {
		gotStatus[c.Status] = c.Count
	}
	if gotStatus[model.CommandStatusDone] != 2 || gotStatus[model.CommandStatusFailed] != 1 ||
		gotStatus[model.CommandStatusExpired] != 1 || gotStatus[model.CommandStatusPending] != 1 {
		t.Fatalf("byStatus 计数错误：%+v", gotStatus)
	}
	// byServer 降序：lobby-1(3) 在 lobby-2(2) 前。
	if res.ByServer[0].ServerID != "lobby-1" || res.ByServer[0].Count != 3 {
		t.Fatalf("byServer 首项应 lobby-1=3，实际 %+v", res.ByServer)
	}
	// byDay 升序：d1 issued=2 done=1 failed=1；d2 issued=3 done=1 failed=1（expired 计 failed）。
	if len(res.ByDay) != 2 {
		t.Fatalf("byDay 应 2 桶，实际 %+v", res.ByDay)
	}
	if res.ByDay[0] != (CommandDayCount{Date: "2026-06-01", Issued: 2, Done: 1, Failed: 1}) {
		t.Fatalf("byDay[0] 错误：%+v", res.ByDay[0])
	}
	if res.ByDay[1] != (CommandDayCount{Date: "2026-06-02", Issued: 3, Done: 1, Failed: 1}) {
		t.Fatalf("byDay[1] 错误：%+v", res.ByDay[1])
	}
}

// TestObserveAnalyticsEmptyWindow 空窗口：total=0、各切片为非 nil 空切片、不 panic。
func TestObserveAnalyticsEmptyWindow(t *testing.T) {
	svc, _ := newCommandObserveTestService(t)
	res, err := svc.Analytics(repository.CommandFilter{
		From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("空窗口 total 应 0，实际 %d", res.Total)
	}
	if res.ByStatus == nil || res.ByType == nil || res.ByServer == nil || res.ByDay == nil {
		t.Fatalf("空结果各切片应非 nil（序列化 []）")
	}
}

// TestObserveAnalyticsWindowCap 窗口 > 92 天返参数错误；恰 92 天放行。
func TestObserveAnalyticsWindowCap(t *testing.T) {
	svc, _ := newCommandObserveTestService(t)
	to := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	if _, err := svc.Analytics(repository.CommandFilter{From: to.Add(-93 * 24 * time.Hour), To: to}); err != apperr.ErrInvalidParam {
		t.Fatalf("窗口 >92 天应返 ErrInvalidParam，实际 %v", err)
	}
	if _, err := svc.Analytics(repository.CommandFilter{From: to.Add(-92 * 24 * time.Hour), To: to}); err != nil {
		t.Fatalf("窗口 =92 天应放行，实际 %v", err)
	}
}

// TestObserveServerTopN byServer 仅取 top-N（10），第 11 个服务器被截断。
func TestObserveServerTopN(t *testing.T) {
	svc, db := newCommandObserveTestService(t)
	at := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	// 12 个不同服务器，计数递减（s00 最多）以保证 top-N 有确定顺序。
	for i := 0; i < 12; i++ {
		count := 12 - i
		server := string(rune('a'+i)) + "-srv"
		for j := 0; j < count; j++ {
			seedCommand(t, db, "prod", server, model.CommandTypeIngestPlugins, model.CommandStatusDone, at)
		}
	}
	res, err := svc.Analytics(repository.CommandFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(res.ByServer) != commandServerTopN {
		t.Fatalf("byServer 应截断为 top-%d，实际 %d", commandServerTopN, len(res.ByServer))
	}
	if res.ByServer[0].Count != 12 {
		t.Fatalf("byServer 首项应 count=12，实际 %+v", res.ByServer[0])
	}
}

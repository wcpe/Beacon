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

// newAuditAnalyticsTestService 背靠内存 sqlite 构造 AuditService，并返回底层 DB 以便播种审计行。
func newAuditAnalyticsTestService(t *testing.T) (*AuditService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("迁移 audit_log 失败: %v", err)
	}
	// 清表，避免共享内存库残留串扰。
	if err := db.Exec("DELETE FROM audit_log").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	// 关闭连接，否则共享内存库（cache=shared）因本连接常驻而不销毁，污染同包其它用例。
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return NewAuditService(repository.NewAuditLogRepository(db)), db
}

// seedAudit 追加一条审计（指定 namespace/action/result/createdAt）。
func seedAudit(t *testing.T, db *gorm.DB, namespace, action, result string, at time.Time) {
	t.Helper()
	if err := db.Create(&model.AuditLog{
		NamespaceCode: namespace, Operator: "admin", Action: action,
		TargetType: model.TargetTypeConfig, TargetRef: "ref",
		Result: result, CreatedAt: at,
	}).Error; err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAnalyticsAggregatesAndOrders 验证窗口内聚合：total/ok/fail、byAction 降序、byDay 升序（按 UTC 日）。
func TestAnalyticsAggregatesAndOrders(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	d1 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	// publish×3（其中 1 失败）跨 d1/d2，assign×2 在 d2/d3 → total=5、ok=4、fail=1。
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, d1)
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, d2)
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultFail, d2)
	seedAudit(t, db, "prod", model.ActionZoneAssign, model.ResultOK, d2)
	seedAudit(t, db, "prod", model.ActionZoneAssign, model.ResultOK, d3)

	res, err := svc.Analytics(repository.AuditFilter{
		From: time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 5 || res.OKCount != 4 || res.FailCount != 1 {
		t.Fatalf("计数错误：total=%d ok=%d fail=%d（期望 5/4/1）", res.Total, res.OKCount, res.FailCount)
	}
	// byAction 降序：publish(3) 在 assign(2) 前。
	if len(res.ByAction) != 2 || res.ByAction[0].Action != model.ActionConfigPublish || res.ByAction[0].Count != 3 {
		t.Fatalf("byAction 首项应为 publish=3，实际 %+v", res.ByAction)
	}
	if res.ByAction[1].Action != model.ActionZoneAssign || res.ByAction[1].Count != 2 {
		t.Fatalf("byAction 次项应为 assign=2，实际 %+v", res.ByAction)
	}
	// byDay 升序，逐日计数：d1=1、d2=3、d3=1。
	wantDays := []AuditDayCount{
		{Date: "2026-06-01", Count: 1},
		{Date: "2026-06-02", Count: 3},
		{Date: "2026-06-03", Count: 1},
	}
	if len(res.ByDay) != len(wantDays) {
		t.Fatalf("byDay 应 %d 桶，实际 %+v", len(wantDays), res.ByDay)
	}
	for i, w := range wantDays {
		if res.ByDay[i] != w {
			t.Fatalf("byDay[%d] 应 %+v，实际 %+v", i, w, res.ByDay[i])
		}
	}
}

// TestAnalyticsNamespaceFilter 验证 namespace 过滤生效（仅聚合该环境的行）。
func TestAnalyticsNamespaceFilter(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	at := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, at)
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, at)
	seedAudit(t, db, "staging", model.ActionConfigPublish, model.ResultOK, at)

	res, err := svc.Analytics(repository.AuditFilter{
		Namespace: "prod",
		From:      at.Add(-time.Hour),
		To:        at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("namespace=prod 应聚合 2 条，实际 %d", res.Total)
	}
}

// TestAnalyticsDayBucketUTC 验证日分桶按 UTC：UTC 跨日的同一本地日两条落不同桶。
func TestAnalyticsDayBucketUTC(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	// 23:30Z 与次日 00:30Z 相隔 1 小时，但 UTC 日不同 → 2 个桶各 1。
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, time.Date(2026, 6, 1, 23, 30, 0, 0, time.UTC))
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, time.Date(2026, 6, 2, 0, 30, 0, 0, time.UTC))

	res, err := svc.Analytics(repository.AuditFilter{
		From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(res.ByDay) != 2 {
		t.Fatalf("跨 UTC 日应分 2 桶，实际 %+v", res.ByDay)
	}
	if res.ByDay[0].Date != "2026-06-01" || res.ByDay[1].Date != "2026-06-02" {
		t.Fatalf("UTC 日键错误：%+v", res.ByDay)
	}
}

// TestAnalyticsEmptyWindow 验证空窗口：total=0、ok/fail=0、byAction/byDay 为非 nil 空切片（序列化为 []）、不 panic。
func TestAnalyticsEmptyWindow(t *testing.T) {
	svc, _ := newAuditAnalyticsTestService(t)
	res, err := svc.Analytics(repository.AuditFilter{
		From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 0 || res.OKCount != 0 || res.FailCount != 0 {
		t.Fatalf("空窗口计数应全 0，实际 total=%d ok=%d fail=%d", res.Total, res.OKCount, res.FailCount)
	}
	if res.ByAction == nil || res.ByDay == nil {
		t.Fatalf("空结果数组应为非 nil 空切片，实际 byAction=%v byDay=%v", res.ByAction, res.ByDay)
	}
	if len(res.ByAction) != 0 || len(res.ByDay) != 0 {
		t.Fatalf("空窗口数组应为空，实际 byAction=%+v byDay=%+v", res.ByAction, res.ByDay)
	}
}

// TestAnalyticsWindowCap 验证窗口 > 92 天返参数错误（handler 转 400）。
func TestAnalyticsWindowCap(t *testing.T) {
	svc, _ := newAuditAnalyticsTestService(t)
	to := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	_, err := svc.Analytics(repository.AuditFilter{
		From: to.Add(-93 * 24 * time.Hour),
		To:   to,
	})
	if err != apperr.ErrInvalidParam {
		t.Fatalf("窗口 >92 天应返 ErrInvalidParam，实际 %v", err)
	}
	// 恰好 92 天应放行。
	if _, err := svc.Analytics(repository.AuditFilter{From: to.Add(-92 * 24 * time.Hour), To: to}); err != nil {
		t.Fatalf("窗口 =92 天应放行，实际 %v", err)
	}
}

// TestAnalyticsDefaultWindow 验证缺省窗口：to 缺省为 now、from 缺省为 to-30 天。
func TestAnalyticsDefaultWindow(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	now := time.Now().UTC()
	// 近 5 天的一条应被纳入，40 天前的一条应在缺省 30 天窗口外。
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, now.Add(-5*24*time.Hour))
	seedAudit(t, db, "prod", model.ActionConfigPublish, model.ResultOK, now.Add(-40*24*time.Hour))

	res, err := svc.Analytics(repository.AuditFilter{}) // from/to 全缺省
	if err != nil {
		t.Fatalf("缺省窗口聚合失败: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("缺省 30 天窗口应仅纳入近 5 天 1 条，实际 %d", res.Total)
	}
	if res.To.Before(now) {
		t.Fatalf("缺省 to 应 >= 调用时刻，实际 %v < %v", res.To, now)
	}
	if res.From.After(res.To.Add(-30*24*time.Hour + time.Second)) {
		t.Fatalf("缺省 from 应约为 to-30天，实际 from=%v to=%v", res.From, res.To)
	}
}

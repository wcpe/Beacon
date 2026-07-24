package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// openSchedQuerySQLite 打开内存 sqlite 供查询服务测试。
func openSchedQuerySQLite(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	return db
}

// newSchedQueryService 构造挂 sqlite 仓库的查询服务。
func newSchedQueryService(t *testing.T, name string) (*SchedDecisionQueryService, *repository.SchedDecisionV2Repository) {
	t.Helper()
	repo := repository.NewSchedDecisionV2Repository(openSchedQuerySQLite(t, name))
	return NewSchedDecisionQueryService(repo), repo
}

// TestSchedQueryListValidation 列表参数校验：from/to 必填有序、范围 ≤60 天、result 枚举。
func TestSchedQueryListValidation(t *testing.T) {
	svc, _ := newSchedQueryService(t, "sched_query_validate")
	now := time.Now().UTC().UnixMilli()
	cases := map[string]ListSchedDecisionsParams{
		"缺 from":      {ToMs: now},
		"缺 to":        {FromMs: now},
		"from 晚于 to":  {FromMs: now, ToMs: now - 1},
		"范围超 60 天":    {FromMs: now - 61*24*3600*1000, ToMs: now},
		"result 非法枚举": {FromMs: now - 1000, ToMs: now, Result: "pending"},
	}
	for name, p := range cases {
		if _, _, err := svc.List(p); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("%s 应 ErrInvalidParam，实际 %v", name, err)
		}
	}
	// 合法参数（空库）→ 空结果不报错。
	rows, total, err := svc.List(ListSchedDecisionsParams{FromMs: now - 1000, ToMs: now})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("合法空查询应无错空结果，实际 total=%d err=%v", total, err)
	}
}

// TestSchedQueryDetailValidationAndNotFound 详情校验与保留窗内未命中 404。
func TestSchedQueryDetailValidationAndNotFound(t *testing.T) {
	svc, _ := newSchedQueryService(t, "sched_query_detail")
	if _, err := svc.Detail(""); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("空 traceId 应 400，实际 %v", err)
	}
	if _, err := svc.Detail("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("超 36 字符应 400，实际 %v", err)
	}
	if _, err := svc.Detail("nope"); !errors.Is(err, apperr.ErrSchedDecisionNotFound) {
		t.Fatalf("未命中应 decision_not_found，实际 %v", err)
	}
}

// TestSchedQuerySummaryWindowParsing window 解析：缺省 1h、支持 24h、非法 / 非正 / 超保留窗 400。
func TestSchedQuerySummaryWindowParsing(t *testing.T) {
	svc, _ := newSchedQueryService(t, "sched_query_window")
	for _, window := range []string{"", "1h", "24h", "90m"} {
		res, err := svc.Summary(window)
		if err != nil {
			t.Fatalf("window=%q 应合法，实际 %v", window, err)
		}
		wantWindow := window
		if wantWindow == "" {
			wantWindow = "1h"
		}
		if res.Window != wantWindow {
			t.Fatalf("回显 window 应 %q，实际 %q", wantWindow, res.Window)
		}
	}
	for _, window := range []string{"abc", "-1h", "0s", "1500h"} {
		if _, err := svc.Summary(window); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("window=%q 应 400，实际 %v", window, err)
		}
	}
}

// TestSchedQuerySummaryAggregation 概览换算：成功率 / 降级占比保留 1 位小数、失败原因按计数降序。
func TestSchedQuerySummaryAggregation(t *testing.T) {
	svc, repo := newSchedQueryService(t, "sched_query_agg")
	base := time.Now().UTC().Add(-10 * time.Minute)
	mk := func(trace, failReason, source string, offset int) model.SchedDecisionV2 {
		return model.SchedDecisionV2{
			TraceID: trace, TsMs: base.Add(time.Duration(offset) * time.Second).UnixMilli(),
			NamespaceID: 1, RequesterServerID: "req-1", ZoneName: "area-1",
			Strategy: SchedStrategyHighestScore, Source: source, Excluded: "[]",
			FailReason: failReason, ChosenScore: -1,
		}
	}
	rows := []model.SchedDecisionV2{
		mk("sum-1", "", SchedSourceControlPlane, 0),
		mk("sum-2", "", SchedSourceControlPlane, 1),
		mk("sum-3", "no_candidate", SchedSourceControlPlane, 2),
		mk("sum-4", "no_candidate", SchedSourceControlPlane, 3),
		mk("sum-5", "zone_not_found", SchedSourceControlPlane, 4),
		mk("sum-6", "", SchedSourceLocalFallback, 5),
	}
	if _, err := repo.FlushDaily(rows); err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	res, err := svc.Summary("1h")
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if res.Total != 6 || res.SuccessCount != 3 {
		t.Fatalf("总数/成功数应 6/3，实际 %d/%d", res.Total, res.SuccessCount)
	}
	if res.SuccessRatePercent != 50.0 {
		t.Fatalf("成功率应 50.0，实际 %v", res.SuccessRatePercent)
	}
	if res.LocalFallbackPercent != 16.7 {
		t.Fatalf("降级占比应 16.7（1/6 保留 1 位小数），实际 %v", res.LocalFallbackPercent)
	}
	if len(res.FailReasonTop) != 2 ||
		res.FailReasonTop[0] != (SchedFailReasonCount{Reason: "no_candidate", Count: 2}) ||
		res.FailReasonTop[1] != (SchedFailReasonCount{Reason: "zone_not_found", Count: 1}) {
		t.Fatalf("失败原因 Top 不符: %+v", res.FailReasonTop)
	}
}

// TestSchedQuerySummaryEmptyWindow 空窗口：成功率按 100、降级占比按 0（对齐 devmock 口径）。
func TestSchedQuerySummaryEmptyWindow(t *testing.T) {
	svc, _ := newSchedQueryService(t, "sched_query_empty")
	res, err := svc.Summary("1h")
	if err != nil {
		t.Fatalf("空窗聚合失败: %v", err)
	}
	if res.Total != 0 || res.SuccessRatePercent != 100 || res.LocalFallbackPercent != 0 {
		t.Fatalf("空窗缺省不符: %+v", res)
	}
	if len(res.FailReasonTop) != 0 {
		t.Fatalf("空窗失败原因应为空列表，实际 %+v", res.FailReasonTop)
	}
}

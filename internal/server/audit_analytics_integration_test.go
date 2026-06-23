//go:build integration

package server_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/testsupport"
)

// seedAnalyticsAudit 经真实 DB 仓库直插一条审计（指定 namespace/action/result/createdAt），
// 以便构造跨多日、多 action、ok/fail 混合的窗口供 analytics 端点聚合验证。
func seedAnalyticsAudit(t *testing.T, repo *repository.AuditLogRepository, namespace, action, result string, at time.Time) {
	t.Helper()
	if err := repo.Create(&model.AuditLog{
		NamespaceCode: namespace, Operator: "admin", Action: action,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: result, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditAnalyticsREST 集成（真 MySQL 验可移植）：跨多日、多 action、ok/fail 混合审计 →
// GET /audits/analytics 聚合 total/okCount/failCount、byAction 降序、byDay 升序；
// namespace 过滤生效；窗口 >92 天 →400；空窗口数组为 [] 非 null。
func TestAuditAnalyticsREST(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// 取同一测试库（beacon_server）的句柄直插带 created_at 的审计行。
	db := testsupport.OpenTestDB(t, "server")
	repo := repository.NewAuditLogRepository(db)

	d1 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	// prod：publish×3（1 失败）跨 d1/d2、assign×2 在 d2/d3 → total=5、ok=4、fail=1。
	seedAnalyticsAudit(t, repo, "prod", model.ActionConfigPublish, model.ResultOK, d1)
	seedAnalyticsAudit(t, repo, "prod", model.ActionConfigPublish, model.ResultOK, d2)
	seedAnalyticsAudit(t, repo, "prod", model.ActionConfigPublish, model.ResultFail, d2)
	seedAnalyticsAudit(t, repo, "prod", model.ActionZoneAssign, model.ResultOK, d2)
	seedAnalyticsAudit(t, repo, "prod", model.ActionZoneAssign, model.ResultOK, d3)
	// staging：另环境 1 条，验 namespace 过滤不串。
	seedAnalyticsAudit(t, repo, "staging", model.ActionConfigPublish, model.ResultOK, d2)

	window := "from=2026-05-25T00:00:00Z&to=2026-06-24T00:00:00Z"

	// ① namespace=prod 窗口内聚合
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits/analytics?namespace=prod&"+window, nil)
	if code != http.StatusOK {
		t.Fatalf("分析端点应 200，实际 %d：%v", code, body)
	}
	if total, _ := body["total"].(float64); total != 5 {
		t.Fatalf("total 应 5，实际 %v", body["total"])
	}
	if ok, _ := body["okCount"].(float64); ok != 4 {
		t.Fatalf("okCount 应 4，实际 %v", body["okCount"])
	}
	if fail, _ := body["failCount"].(float64); fail != 1 {
		t.Fatalf("failCount 应 1，实际 %v", body["failCount"])
	}
	// byAction 降序：publish(3) 在 assign(2) 前
	byAction, _ := body["byAction"].([]any)
	if len(byAction) != 2 {
		t.Fatalf("byAction 应 2 项，实际 %v", body["byAction"])
	}
	first, _ := byAction[0].(map[string]any)
	if first["action"] != model.ActionConfigPublish || first["count"].(float64) != 3 {
		t.Fatalf("byAction 首项应为 publish=3，实际 %v", first)
	}
	second, _ := byAction[1].(map[string]any)
	if second["action"] != model.ActionZoneAssign || second["count"].(float64) != 2 {
		t.Fatalf("byAction 次项应为 assign=2，实际 %v", second)
	}
	// byDay 升序，逐日计数 d1=1、d2=3、d3=1
	byDay, _ := body["byDay"].([]any)
	wantDays := []struct {
		date  string
		count float64
	}{{"2026-06-01", 1}, {"2026-06-02", 3}, {"2026-06-03", 1}}
	if len(byDay) != len(wantDays) {
		t.Fatalf("byDay 应 %d 桶，实际 %v", len(wantDays), body["byDay"])
	}
	for i, w := range wantDays {
		got, _ := byDay[i].(map[string]any)
		if got["date"] != w.date || got["count"].(float64) != w.count {
			t.Fatalf("byDay[%d] 应 %s=%v，实际 %v", i, w.date, w.count, got)
		}
	}

	// ② namespace 过滤生效：staging 仅 1 条
	code, sbody := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits/analytics?namespace=staging&"+window, nil)
	if code != http.StatusOK {
		t.Fatalf("staging 分析应 200，实际 %d", code)
	}
	if total, _ := sbody["total"].(float64); total != 1 {
		t.Fatalf("staging total 应 1，实际 %v", sbody["total"])
	}

	// ③ 窗口 >92 天 → 400
	code, ebody := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v1/audits/analytics?from=2026-01-01T00:00:00Z&to=2026-06-24T00:00:00Z", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("窗口 >92 天应 400，实际 %d：%v", code, ebody)
	}

	// ④ 空窗口：byAction/byDay 序列化为 [] 非 null、total=0
	code, embody := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v1/audits/analytics?namespace=prod&from=2026-04-01T00:00:00Z&to=2026-04-10T00:00:00Z", nil)
	if code != http.StatusOK {
		t.Fatalf("空窗口应 200，实际 %d", code)
	}
	if total, _ := embody["total"].(float64); total != 0 {
		t.Fatalf("空窗口 total 应 0，实际 %v", embody["total"])
	}
	if ba, ok := embody["byAction"].([]any); !ok || ba == nil {
		t.Fatalf("空窗口 byAction 应为 [] 非 null，实际 %v", embody["byAction"])
	}
	if bd, ok := embody["byDay"].([]any); !ok || bd == nil {
		t.Fatalf("空窗口 byDay 应为 [] 非 null，实际 %v", embody["byDay"])
	}
}

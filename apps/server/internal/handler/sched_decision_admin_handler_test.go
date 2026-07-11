package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// schedDecisionItemKeys 是 contracts SchedDecisionItem 的全部键（逐键契约断言用）。
var schedDecisionItemKeys = []string{
	"traceId", "tsMs", "namespaceId", "crossNamespace", "requesterServerId",
	"plugin", "purpose", "zoneName", "strategy", "source", "weightsRev",
	"candidateCount", "excludedCount", "chosenServerId", "chosenScore", "failReason", "durationMs",
}

// newSchedAdminRouter 构造挂 sqlite 真仓库的管理面路由（chi 提供 {traceId} 路径参数）。
func newSchedAdminRouter(t *testing.T, name string) (chi.Router, *gorm.DB) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	h := NewSchedDecisionAdminHandler(service.NewSchedDecisionQueryService(repository.NewSchedDecisionV2Repository(db)))
	r := chi.NewRouter()
	r.Get("/admin/v2/sched-decisions", h.List)
	r.Get("/admin/v2/sched-decisions/summary", h.Summary)
	r.Get("/admin/v2/sched-decisions/{traceId}", h.Detail)
	return r, db
}

// seedSchedAdminRows 造两行近时决策：一行 control_plane 成功、一行 local_fallback 失败（字段可空形态齐全）。
func seedSchedAdminRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	repo := repository.NewSchedDecisionV2Repository(db)
	base := time.Now().UTC().Add(-5 * time.Minute)
	rows := []model.SchedDecisionV2{
		{
			TraceID: "adm-ok", TsMs: base.UnixMilli(), NamespaceID: 1, RequesterServerID: "req-1",
			Plugin: "Lodestone", Purpose: "lobby-transfer", ZoneName: "area-1",
			Strategy: model.SchedStrategyHighestScore, Source: model.SchedSourceControlPlane,
			WeightsRev: 3, CandidateCount: 3,
			Excluded:       `[{"serverId":"s-x","reason":"draining"}]`,
			ChosenServerID: "s-a", ChosenScore: 92, DurationMs: 1,
		},
		{
			TraceID: "adm-fb", TsMs: base.Add(time.Minute).UnixMilli(), NamespaceID: 1,
			RequesterServerID: "req-1", ZoneName: "area-1",
			Strategy: model.SchedStrategyHighestScore, Source: model.SchedSourceLocalFallback,
			CandidateCount: 0, Excluded: "[]", ChosenScore: -1, FailReason: "no_candidate",
		},
	}
	if _, err := repo.FlushDaily(rows); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
}

// getJSON 对路由发一次 GET 并解析 json 响应。
func getJSON(t *testing.T, r chi.Router, target string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应非 json: %v（%s）", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

// schedAdminListURL 拼近两日窗口的列表 URL。
func schedAdminListURL(extra string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("/admin/v2/sched-decisions?from=%d&to=%d%s",
		now.AddDate(0, 0, -1).UnixMilli(), now.UnixMilli(), extra)
}

// TestSchedAdminListContract 列表：Paged{items,total} + 列表项 17 键逐键对齐 contracts，
// 可空字段（plugin/purpose/chosen/failReason）与降级行 weightsRev 的 null 语义正确。
func TestSchedAdminListContract(t *testing.T) {
	r, db := newSchedAdminRouter(t, "sched_admin_list")
	seedSchedAdminRows(t, db)

	code, body := getJSON(t, r, schedAdminListURL(""))
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "items", "total")
	if body["total"] != float64(2) {
		t.Fatalf("total 应 2，实际 %v", body["total"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items 应 2 条，实际 %d", len(items))
	}
	// ts_ms 降序：降级行（后写）在前。
	fallback, _ := items[0].(map[string]any)
	success, _ := items[1].(map[string]any)
	assertKeys(t, fallback, schedDecisionItemKeys...)
	assertKeys(t, success, schedDecisionItemKeys...)

	if success["traceId"] != "adm-ok" || success["plugin"] != "Lodestone" ||
		success["weightsRev"] != float64(3) || success["chosenServerId"] != "s-a" ||
		success["chosenScore"] != float64(92) || success["failReason"] != nil ||
		success["excludedCount"] != float64(1) || success["source"] != "control_plane" {
		t.Fatalf("成功行映射不符: %v", success)
	}
	if fallback["traceId"] != "adm-fb" || fallback["weightsRev"] != nil ||
		fallback["plugin"] != nil || fallback["purpose"] != nil ||
		fallback["chosenServerId"] != nil || fallback["failReason"] != "no_candidate" ||
		fallback["chosenScore"] != float64(-1) || fallback["source"] != "local_fallback" {
		t.Fatalf("降级行 null 语义不符: %v", fallback)
	}
}

// TestSchedAdminListParamErrors from/to 必填与 result 非法均 400。
func TestSchedAdminListParamErrors(t *testing.T) {
	r, _ := newSchedAdminRouter(t, "sched_admin_params")
	for name, target := range map[string]string{
		"缺 from/to": "/admin/v2/sched-decisions",
		"from 非数字":  "/admin/v2/sched-decisions?from=abc&to=123",
		"result 非法": schedAdminListURL("&result=pending"),
	} {
		if code, _ := getJSON(t, r, target); code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", name, code)
		}
	}
}

// TestSchedAdminDetailContract 详情：17 键 + excluded 数组；未命中 404 decision_not_found。
func TestSchedAdminDetailContract(t *testing.T) {
	r, db := newSchedAdminRouter(t, "sched_admin_detail")
	seedSchedAdminRows(t, db)

	code, body := getJSON(t, r, "/admin/v2/sched-decisions/adm-ok")
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, append([]string{"excluded"}, schedDecisionItemKeys...)...)
	excluded, _ := body["excluded"].([]any)
	if len(excluded) != 1 {
		t.Fatalf("excluded 应 1 条，实际 %v", body["excluded"])
	}
	entry, _ := excluded[0].(map[string]any)
	assertKeys(t, entry, "serverId", "reason")
	if entry["serverId"] != "s-x" || entry["reason"] != "draining" {
		t.Fatalf("排除明细不符: %v", entry)
	}

	code, body = getJSON(t, r, "/admin/v2/sched-decisions/no-such-trace")
	if code != http.StatusNotFound || body["code"] != "decision_not_found" {
		t.Fatalf("未命中应 404 decision_not_found，实际 %d %v", code, body)
	}
}

// TestSchedAdminSummaryContract 概览：六键对齐 contracts SchedDecisionSummary；window 非法 400。
func TestSchedAdminSummaryContract(t *testing.T) {
	r, db := newSchedAdminRouter(t, "sched_admin_summary")
	seedSchedAdminRows(t, db)

	code, body := getJSON(t, r, "/admin/v2/sched-decisions/summary?window=1h")
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "window", "total", "successCount", "successRatePercent",
		"failReasonTop", "localFallbackPercent")
	if body["window"] != "1h" || body["total"] != float64(2) || body["successCount"] != float64(1) ||
		body["successRatePercent"] != float64(50) || body["localFallbackPercent"] != float64(50) {
		t.Fatalf("概览数值不符: %v", body)
	}
	top, _ := body["failReasonTop"].([]any)
	if len(top) != 1 {
		t.Fatalf("failReasonTop 应 1 项，实际 %v", body["failReasonTop"])
	}
	entry, _ := top[0].(map[string]any)
	assertKeys(t, entry, "reason", "count")

	if code, _ := getJSON(t, r, "/admin/v2/sched-decisions/summary?window=abc"); code != http.StatusBadRequest {
		t.Fatalf("window 非法应 400，实际 %d", code)
	}
}

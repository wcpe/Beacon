package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// TestParseRFC3339 验证审计时间范围解析：空与非法返回零值（不设界），合法按 RFC3339 解析（含时区偏移归一）。
func TestParseRFC3339(t *testing.T) {
	if got := parseRFC3339(""); !got.IsZero() {
		t.Fatalf("空字符串应返回零值，实际 %v", got)
	}
	if got := parseRFC3339("not-a-time"); !got.IsZero() {
		t.Fatalf("非法格式应返回零值，实际 %v", got)
	}
	if got := parseRFC3339("2026-01-02"); !got.IsZero() {
		t.Fatalf("非 RFC3339（缺时间与时区）应返回零值，实际 %v", got)
	}

	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := parseRFC3339("2026-01-02T03:04:05Z"); !got.Equal(want) {
		t.Fatalf("Z 后缀解析错误：got %v want %v", got, want)
	}
	// 含偏移 +08:00 等价于 UTC 的 2026-01-01T19:04:05Z
	if got := parseRFC3339("2026-01-02T03:04:05+08:00"); !got.Equal(want.Add(-8 * time.Hour)) {
		t.Fatalf("含时区偏移解析错误：got %v", got.UTC())
	}
}

// newAuditResultRouter 构造挂内存库的审计路由（列表 + 导出），返回路由与仓库供造数。
func newAuditResultRouter(t *testing.T, name string) (chi.Router, *repository.AuditLogRepository) {
	t.Helper()
	db := openHandlerSQLite(t, name)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("迁移 audit_log 失败: %v", err)
	}
	repo := repository.NewAuditLogRepository(db)
	h := NewAuditHandler(service.NewAuditService(repo), newTestSettings(t, db))
	r := chi.NewRouter()
	r.Get("/admin/v1/audits", h.List)
	r.Get("/admin/v1/audits/export", h.Export)
	return r, repo
}

// seedAuditResult 造一条指定结果的审计（成功/失败筛选测试用）。
func seedAuditResult(t *testing.T, repo *repository.AuditLogRepository, result string, at time.Time) {
	t.Helper()
	if err := repo.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: "alice", Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: result, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditResultParamValidation 验证 result 查询参数白名单：仅空 / ok / fail 合法，
// 其余（success / failed / error / 大小写变体等）返回 INVALID_PARAM；公共提取函数同时供
// List、冷查询、导出、聚合使用，且不破坏观测范围注入。
func TestAuditResultParamValidation(t *testing.T) {
	for _, want := range []string{"", model.ResultOK, model.ResultFail} {
		filter, err := auditExportFilter(url.Values{"result": []string{want}})
		if err != nil {
			t.Fatalf("result=%q 应合法，实际错误 %v", want, err)
		}
		if filter.Result != want {
			t.Fatalf("result=%q 未解析进 filter：%q", want, filter.Result)
		}
	}

	for _, bad := range []string{"success", "failed", "error", "OK", "Fail", "true", "all", "ok,fail"} {
		if _, err := auditExportFilter(url.Values{"result": []string{bad}}); err != apperr.ErrInvalidParam {
			t.Fatalf("result=%q 应返回 INVALID_PARAM，实际 %v", bad, err)
		}
	}

	// auditScopeFilter（List / 冷查询 / 导出 / 聚合共用的入口）同样校验，并保留观测范围注入语义
	scoped := service.ObservationScope{NamespaceCodes: []string{"prod"}}
	filter, err := auditScopeFilter(url.Values{"result": []string{model.ResultFail}}, scoped)
	if err != nil {
		t.Fatalf("合法 scoped 提取失败: %v", err)
	}
	if filter.Result != model.ResultFail || !filter.Scoped || len(filter.NamespaceCodes) != 1 {
		t.Fatalf("scoped 提取结果错误：%+v", filter)
	}
	if _, err := auditScopeFilter(url.Values{"result": []string{"success"}}, scoped); err != apperr.ErrInvalidParam {
		t.Fatalf("scoped 非法 result 应返回 INVALID_PARAM，实际 %v", err)
	}
	// 全量范围（All）不置 Scoped
	all, err := auditScopeFilter(url.Values{}, service.ObservationScope{All: true})
	if err != nil {
		t.Fatalf("全量范围提取失败: %v", err)
	}
	if all.Scoped {
		t.Fatalf("All 范围不应置 Scoped：%+v", all)
	}
}

// TestAuditListEndpointResultFilter 验证列表端点按 result 筛选：result=ok / result=fail 只回对应记录，
// 非法 result 返回 400 INVALID_PARAM。
func TestAuditListEndpointResultFilter(t *testing.T) {
	r, repo := newAuditResultRouter(t, "audit_result_list")
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedAuditResult(t, repo, model.ResultOK, base)
	seedAuditResult(t, repo, model.ResultOK, base.Add(time.Minute))
	seedAuditResult(t, repo, model.ResultFail, base.Add(2*time.Minute))

	for _, want := range []string{model.ResultOK, model.ResultFail} {
		code, body := getJSON(t, r, "/admin/v1/audits?result="+want)
		if code != http.StatusOK {
			t.Fatalf("result=%s 状态=%d；响应=%v", want, code, body)
		}
		wantTotal := 2.0
		if want == model.ResultFail {
			wantTotal = 1
		}
		if body["total"] != wantTotal {
			t.Fatalf("result=%s 应 total=%v，实际 %v", want, wantTotal, body["total"])
		}
		items, _ := body["items"].([]any)
		if len(items) != int(wantTotal) {
			t.Fatalf("result=%s 应返回 %v 条，实际 %d", want, wantTotal, len(items))
		}
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["result"] != want {
				t.Fatalf("result=%s 结果集含 %v", want, item["result"])
			}
		}
	}

	// 空 result 不过滤 → 全量 3 条
	_, all := getJSON(t, r, "/admin/v1/audits")
	if all["total"] != 3.0 {
		t.Fatalf("空 result 应返回全量 3 条，实际 %v", all["total"])
	}

	// 非法 result → 400 INVALID_PARAM
	code, body := getJSON(t, r, "/admin/v1/audits?result=success")
	if code != http.StatusBadRequest || body["code"] != apperr.ErrInvalidParam.Code {
		t.Fatalf("非法 result 应 400 %s，实际 %d：%v", apperr.ErrInvalidParam.Code, code, body)
	}
}

// TestAuditColdAndExportResultParam 验证冷查询与导出入口同样受 result 白名单约束：
// 非法值一律 400（导出还须在写响应头之前拒绝）；合法值放行（冷查询因归档库未配置转 503 而非参数错误）。
func TestAuditColdAndExportResultParam(t *testing.T) {
	r, repo := newAuditResultRouter(t, "audit_result_cold_export")
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedAuditResult(t, repo, model.ResultOK, base)
	seedAuditResult(t, repo, model.ResultFail, base.Add(time.Minute))

	from := url.QueryEscape(base.Add(-time.Hour).Format(time.RFC3339))
	to := url.QueryEscape(base.Add(time.Hour).Format(time.RFC3339))

	// 冷查询入口：非法 result → 400 INVALID_PARAM（时间范围合法，确保拒绝来自 result 校验）
	code, body := getJSON(t, r, "/admin/v1/audits?includeArchived=true&from="+from+"&to="+to+"&result=success")
	if code != http.StatusBadRequest || body["code"] != apperr.ErrInvalidParam.Code {
		t.Fatalf("冷查询非法 result 应 400 %s，实际 %d：%v", apperr.ErrInvalidParam.Code, code, body)
	}
	// 合法 result 放行：归档库未配置 → 503 ARCHIVE_UNAVAILABLE（证明校验通过）
	code, body = getJSON(t, r, "/admin/v1/audits?includeArchived=true&from="+from+"&to="+to+"&result=fail")
	if code != http.StatusServiceUnavailable || body["code"] != apperr.ErrArchiveUnavailable.Code {
		t.Fatalf("冷查询合法 result 应 503 %s，实际 %d：%v", apperr.ErrArchiveUnavailable.Code, code, body)
	}

	// 导出入口：非法 result → 400，且写头前拒绝（不带附件响应头）
	rec := doJSON(t, r, "/admin/v1/audits/export?result=success")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("导出非法 result 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("非法 result 不应写出附件响应头，实际 %q", cd)
	}
	var errBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody.Code != apperr.ErrInvalidParam.Code {
		t.Fatalf("导出非法 result 错误体错误：%s（%v）", rec.Body.String(), err)
	}

	// 导出入口：合法 result=fail → CSV 只含失败行
	rec = doJSON(t, r, "/admin/v1/audits/export?result=fail&format=csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("导出 result=fail 状态=%d：%s", rec.Code, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("解析导出 CSV 失败: %v", err)
	}
	if len(rows) != 2 { // 表头 + 1 条失败记录
		t.Fatalf("result=fail 导出应表头 + 1 行，实际 %d 行：%v", len(rows), rows)
	}
	if got := rows[1][7]; got != model.ResultFail {
		t.Fatalf("导出 result 列应为 %s，实际 %q", model.ResultFail, got)
	}
}

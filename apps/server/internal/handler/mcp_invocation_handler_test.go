package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// mcpInvocationItemKeys 是 contracts MCPInvocationItem 的全部键（逐键契约断言用，camelCase）。
var mcpInvocationItemKeys = []string{
	"invocationId", "clientId", "profile", "toolName", "riskLevel", "result", "reason",
	"targetDigest", "argKeys", "argBytes", "durationMs", "traceId", "clientIp", "errorSummary", "createdAt",
}

// newMCPInvocationRouter 构造挂 sqlite 真仓库的流水查询路由。
func newMCPInvocationRouter(t *testing.T, name string) (chi.Router, *repository.MCPInvocationRepository, *gorm.DB) {
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
	h := NewMCPInvocationHandler(service.NewMCPInvocationService(nil, repo))
	r := chi.NewRouter()
	// 与 router.go 的注册形态一致（含路径参数名，详情据它直定日表）。
	r.Get("/admin/v2/mcp/invocations", h.List)
	r.Get("/admin/v2/mcp/invocations/{invocationId}", h.Detail)
	return r, repo, db
}

// seedMCPInvocation 造一行流水（主键与 createdAt 同源，据内嵌毫秒路由日表）。
func seedMCPInvocation(t *testing.T, repo *repository.MCPInvocationRepository, ms int64, mutate func(*model.MCPInvocation)) model.MCPInvocation {
	t.Helper()
	row := model.MCPInvocation{
		InvocationID: store.NewUUIDv7(ms), ClientID: "client-a", Profile: model.MCPClientProfileAutomation,
		ToolName: "beacon.audit.events.list", RiskLevel: model.MCPInvocationRiskLow,
		Result: model.MCPInvocationResultOK, ArgBytes: 24, DurationMs: 3,
		TraceID: "9f2c1a4b7d8e0f31", ClientIP: "10.0.0.7", CreatedAt: time.UnixMilli(ms).UTC(),
	}
	if mutate != nil {
		mutate(&row)
	}
	if _, err := repo.FlushDaily([]model.MCPInvocation{row}); err != nil {
		t.Fatalf("写入流水失败: %v", err)
	}
	return row
}

// doJSON 发一次 GET 并返回响应记录器。
func doJSON(t *testing.T, r chi.Router, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestMCPInvocationListEndpointContract 校验列表端点的响应形状与 item 键集合（逐键对齐 contracts）。
func TestMCPInvocationListEndpointContract(t *testing.T) {
	r, repo, _ := newMCPInvocationRouter(t, "mcp_inv_list_contract")
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	seedMCPInvocation(t, repo, day.UnixMilli(), nil)

	rec := doJSON(t, r, "/admin/v2/mcp/invocations")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表状态=%d，期望 200；响应=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v；原文=%s", err, rec.Body.String())
	}
	if len(body.Items) != 1 {
		t.Fatalf("items 行数=%d，期望 1", len(body.Items))
	}
	item := body.Items[0]
	for _, key := range mcpInvocationItemKeys {
		if _, ok := item[key]; !ok {
			t.Fatalf("item 缺键 %q：%v", key, item)
		}
	}
	if len(item) != len(mcpInvocationItemKeys) {
		t.Fatalf("item 键数=%d，期望恰好 %d：%v", len(item), len(mcpInvocationItemKeys), item)
	}
	// nextCursor 为字符串（末页空串），不引入 null 分支。
	if body.NextCursor != "" {
		t.Fatalf("单行结果应为末页（nextCursor 空串），实际 %q", body.NextCursor)
	}
	// createdAt 为 RFC3339（毫秒精度 UTC）。
	createdAt, _ := item["createdAt"].(string)
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Fatalf("createdAt=%q 不是 RFC3339: %v", createdAt, err)
	}
	if !strings.HasSuffix(createdAt, "Z") {
		t.Fatalf("createdAt=%q 应为 UTC（Z 结尾）", createdAt)
	}
}

// TestMCPInvocationListEndpointFiltersAndPaging 端到端核对六维过滤 + 游标分页（§5 第 6 条）。
func TestMCPInvocationListEndpointFiltersAndPaging(t *testing.T) {
	r, repo, _ := newMCPInvocationRouter(t, "mcp_inv_list_filters")
	today := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	seedMCPInvocation(t, repo, yesterday.UnixMilli(), func(m *model.MCPInvocation) { m.ClientID = "client-a" })
	seedMCPInvocation(t, repo, today.UnixMilli(), func(m *model.MCPInvocation) { m.ClientID = "client-a" })
	seedMCPInvocation(t, repo, today.Add(time.Minute).UnixMilli(), func(m *model.MCPInvocation) {
		m.ClientID = "client-b"
		m.ToolName = "beacon.files.create"
		m.RiskLevel = model.MCPInvocationRiskHigh
		m.Result = model.MCPInvocationResultRejected
		m.Reason = model.MCPInvocationReasonHandlerRejected
		m.ErrorSummary = "目标文件不存在"
		m.TargetDigest = "path=/plugins/x.jar"
		m.ArgKeys = "content:1180,path"
		m.ArgBytes = 130
	})
	seedMCPInvocation(t, repo, today.Add(2*time.Minute).UnixMilli(), func(m *model.MCPInvocation) {
		m.ClientID = "client-c"
		m.ToolName = "beacon.system.update.apply"
		m.RiskLevel = model.MCPInvocationRiskCritical
		m.Result = model.MCPInvocationResultRejected
		m.Reason = model.MCPInvocationReasonProductionMode
	})

	listIDs := func(path string) []string {
		t.Helper()
		rec := doJSON(t, r, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s 状态=%d，期望 200；响应=%s", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Items      []mcpInvocationItemJS `json:"items"`
			NextCursor string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		ids := make([]string, 0, len(body.Items))
		for _, item := range body.Items {
			ids = append(ids, item.InvocationID)
		}
		return ids
	}

	// 逐维过滤。
	cases := []struct {
		name  string
		path  string
		wantN int
	}{
		{"按工具", "/admin/v2/mcp/invocations?tool=beacon.files.create", 1},
		{"按 clientId", "/admin/v2/mcp/invocations?clientId=client-a", 2},
		{"按结果", "/admin/v2/mcp/invocations?result=rejected", 2},
		{"按风险等级", "/admin/v2/mcp/invocations?riskLevel=critical", 1},
		{"按原因", "/admin/v2/mcp/invocations?reason=production_mode", 1},
		{"无过滤全部", "/admin/v2/mcp/invocations", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(listIDs(tc.path)); got != tc.wantN {
				t.Fatalf("命中行数=%d，期望 %d", got, tc.wantN)
			}
		})
	}

	// 时间窗：不带窗两日都在；带昨日窗只返回昨日行。
	allRange := "from=" + urlEscape(time.UnixMilli(yesterday.Add(-time.Hour).UnixMilli()).UTC().Format(time.RFC3339Nano)) +
		"&to=" + urlEscape(time.UnixMilli(today.Add(time.Hour).UnixMilli()).UTC().Format(time.RFC3339Nano))
	if got := len(listIDs("/admin/v2/mcp/invocations?" + allRange)); got != 4 {
		t.Fatalf("跨日时间窗应返回 4 行，实际 %d", got)
	}
	yesterdayOnly := "from=" + urlEscape(time.UnixMilli(yesterday.Add(-time.Hour).UnixMilli()).UTC().Format(time.RFC3339Nano)) +
		"&to=" + urlEscape(time.UnixMilli(yesterday.Add(time.Hour).UnixMilli()).UTC().Format(time.RFC3339Nano))
	if got := len(listIDs("/admin/v2/mcp/invocations?" + yesterdayOnly)); got != 1 {
		t.Fatalf("昨日时间窗应只返回 1 行，实际 %d", got)
	}

	// 游标分页：limit=1 逐页续查，无重复无遗漏。
	seen := map[string]struct{}{}
	cursor := ""
	for page := 0; page < 6; page++ {
		path := "/admin/v2/mcp/invocations?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := doJSON(t, r, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("分页 GET 状态=%d；响应=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items      []mcpInvocationItemJS `json:"items"`
			NextCursor string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		for _, item := range body.Items {
			if _, dup := seen[item.InvocationID]; dup {
				t.Fatalf("分页出现重复行: %s", item.InvocationID)
			}
			seen[item.InvocationID] = struct{}{}
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("分页覆盖 %d 行，期望 4 行（不得遗漏）", len(seen))
	}
}

// TestMCPInvocationListEndpointRejectsInvalidParams 校验非法枚举值与倒置时间窗返回 400 INVALID_PARAM（§3.7）。
func TestMCPInvocationListEndpointRejectsInvalidParams(t *testing.T) {
	r, _, _ := newMCPInvocationRouter(t, "mcp_inv_list_invalid")
	paths := []string{
		"/admin/v2/mcp/invocations?result=success",
		"/admin/v2/mcp/invocations?riskLevel=medium",
		"/admin/v2/mcp/invocations?reason=whatever",
		"/admin/v2/mcp/invocations?from=2026-07-12T00:00:00Z&to=2026-07-11T00:00:00Z",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := doJSON(t, r, path)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态=%d，期望 400；响应=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析错误体失败: %v", err)
			}
			if body.Code != apperr.ErrInvalidParam.Code {
				t.Fatalf("错误码=%q，期望 %q", body.Code, apperr.ErrInvalidParam.Code)
			}
		})
	}
}

// TestMCPInvocationDetailEndpoint 校验详情命中返回同行、未命中 / 非法 ID 一律 404（§5 第 8 条）。
func TestMCPInvocationDetailEndpoint(t *testing.T) {
	r, repo, _ := newMCPInvocationRouter(t, "mcp_inv_detail")
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	row := seedMCPInvocation(t, repo, day.UnixMilli(), func(m *model.MCPInvocation) {
		m.ToolName = "beacon.files.create"
		m.RiskLevel = model.MCPInvocationRiskHigh
		m.Result = model.MCPInvocationResultRejected
		m.Reason = model.MCPInvocationReasonProductionMode
		m.TargetDigest = "path=/plugins/x.jar"
		m.ArgKeys = "content:1180,path"
		m.ErrorSummary = "生产模式已禁用 critical 风险等级工具：beacon.files.create"
	})

	rec := doJSON(t, r, "/admin/v2/mcp/invocations/"+row.InvocationID)
	if rec.Code != http.StatusOK {
		t.Fatalf("详情状态=%d，期望 200；响应=%s", rec.Code, rec.Body.String())
	}
	var item mcpInvocationItemJS
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if item.InvocationID != row.InvocationID || item.ArgKeys != row.ArgKeys ||
		item.TargetDigest != row.TargetDigest || item.ErrorSummary != row.ErrorSummary {
		t.Fatalf("详情与写入行不一致:\n写入 %+v\n读回 %+v", row, item)
	}
	// 响应绝不含任何参数正文（只有脱敏摘要列）。
	if strings.Contains(rec.Body.String(), "SECRET-CANARY") {
		t.Fatalf("详情响应含参数正文: %s", rec.Body.String())
	}

	for _, id := range []string{"not-a-uuid", store.NewUUIDv7(day.AddDate(0, 0, -400).UnixMilli())} {
		t.Run("未命中 "+id[:8], func(t *testing.T) {
			rec := doJSON(t, r, "/admin/v2/mcp/invocations/"+id)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("状态=%d，期望 404；响应=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析错误体失败: %v", err)
			}
			if body.Code != apperr.ErrMCPInvocationNotFound.Code {
				t.Fatalf("错误码=%q，期望 %q", body.Code, apperr.ErrMCPInvocationNotFound.Code)
			}
		})
	}
}

// urlEscape 对查询值做最小转义（时间戳里的 + / : 不能直接进 query）。
func urlEscape(v string) string {
	replacer := strings.NewReplacer("+", "%2B", ":", "%3A")
	return replacer.Replace(v)
}

//go:build integration

package server_test

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/testsupport"
)

// seedExportAudit 经真实 DB 仓库直插一条带 detail 的审计，供 detail LIKE 检索与导出流验证。
func seedExportAudit(t *testing.T, repo *repository.AuditLogRepository, operator, detail string, at time.Time) {
	t.Helper()
	if err := repo.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Detail: detail, Result: model.ResultOK, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// doRawGet 发起带令牌的 GET，返回状态码、Content-Type 与原始响应体（导出非 JSON 包装，需读原文）。
func doRawGet(t *testing.T, url string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(data)
}

// TestAuditDetailKeywordAndExportREST 集成（真 MySQL 验可移植）：
// ① detail LIKE 子串检索（含 % 转义）；② CSV 导出表头 + 命中行 + Content-Type；
// ③ JSON 导出数组；④ 非法 format → 400。
func TestAuditDetailKeywordAndExportREST(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	db := testsupport.OpenTestDB(t, "server")
	repo := repository.NewAuditLogRepository(db)

	base := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	seedExportAudit(t, repo, "alice", `{"dataId":"mysql.yml","note":"50%"}`, base)
	seedExportAudit(t, repo, "bob", `{"dataId":"redis.yml"}`, base.Add(time.Minute))
	seedExportAudit(t, repo, "carol", `{"dataId":"mysql-slave.yml"}`, base.Add(2*time.Minute))

	// ① detail LIKE：关键字 mysql 命中 alice + carol（含子串），不含 bob
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?detailKeyword=mysql", nil)
	if code != http.StatusOK {
		t.Fatalf("detail 检索应 200，实际 %d：%v", code, body)
	}
	if total, _ := body["total"].(float64); total != 2 {
		t.Fatalf("detailKeyword=mysql 应 2 条，实际 %v", body["total"])
	}

	// ① 通配符转义：关键字 50% 应作字面命中 alice 一条（不被当 50 + 任意）
	code, pctBody := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?detailKeyword=50%25", nil)
	if code != http.StatusOK {
		t.Fatalf("50%% 检索应 200，实际 %d", code)
	}
	if total, _ := pctBody["total"].(float64); total != 1 {
		t.Fatalf("detailKeyword=50%% 转义后应 1 条，实际 %v", pctBody["total"])
	}

	// ② CSV 导出（复用 detailKeyword=mysql）：表头 + 2 行命中，Content-Type=text/csv
	code, ct, raw := doRawGet(t, ts.URL+"/admin/v1/audits/export?detailKeyword=mysql&format=csv")
	if code != http.StatusOK {
		t.Fatalf("CSV 导出应 200，实际 %d：%s", code, raw)
	}
	if !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("CSV Content-Type 应 text/csv，实际 %q", ct)
	}
	records, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v（原文 %s）", err, raw)
	}
	if len(records) != 3 { // 表头 + 2 命中
		t.Fatalf("CSV 应表头 + 2 行，实际 %d 行：%v", len(records), records)
	}
	if records[0][0] != "id" {
		t.Fatalf("CSV 首列表头应 id，实际 %q", records[0][0])
	}

	// ③ JSON 导出（全量，无过滤）：JSON 数组，至少 3 条
	code, ct, raw = doRawGet(t, ts.URL+"/admin/v1/audits/export?format=json")
	if code != http.StatusOK {
		t.Fatalf("JSON 导出应 200，实际 %d：%s", code, raw)
	}
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("JSON Content-Type 应 application/json，实际 %q", ct)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		t.Fatalf("解析导出 JSON 失败: %v（原文 %s）", err, raw)
	}
	if len(arr) < 3 {
		t.Fatalf("JSON 导出应 >=3 条，实际 %d", len(arr))
	}

	// ④ 非法 format → 400 INVALID_PARAM（写头前拒绝）
	code, ebody := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits/export?format=xml", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 format 应 400，实际 %d：%v", code, ebody)
	}
}

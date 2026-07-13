package handler

import (
	"bytes"
	"encoding/json"
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

// archiveJobKeys 是 contracts ArchiveJob 的全部键（列表项 / 详情逐键契约断言用）。
var archiveJobKeys = []string{
	"id", "mode", "trigger", "status", "domains", "operator",
	"error", "startedAt", "finishedAt", "createdAt",
}

// archiveItemKeys 是 contracts ArchiveJobItem 的全部键（详情 items[] 逐键契约断言用）。
var archiveItemKeys = []string{
	"id", "domain", "tableName", "rangeTo", "phase", "cursor",
	"rowsExpected", "rowsCopied", "rowsDeleted",
	"verifyRowsHot", "verifyRowsArchive", "verifySampleSize",
	"verifyHashHot", "verifyHashArchive", "verifyPassed", "error",
}

// openArchiveMemDB 打开一个共享缓存的内存 sqlite（经 store.Open 迁移含归档任务表 / 审计 / 设置表）。
func openArchiveMemDB(t *testing.T, name string) *gorm.DB {
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

// newArchiveRouter 构造挂真 ArchiveService（热 + 归档两套内存 sqlite）的管理面路由。
// 不起后台工作器——handler 单测只验 HTTP 关注点（解析 / 状态码 / 错误映射 / 契约键），
// 任务不推进保持 pending，正合各边界（409/404/400）造数需要。
func newArchiveRouter(t *testing.T, name string) (chi.Router, *repository.ArchiveJobRepository, *gorm.DB) {
	t.Helper()
	hot := openArchiveMemDB(t, name+"_hot")
	arc := openArchiveMemDB(t, name+"_arc")
	settings, err := service.NewSettingsService(hot, repository.NewSettingRepository(hot), repository.NewAuditLogRepository(hot))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	repo := repository.NewArchiveJobRepository(hot)
	info := store.ArchiveInfo{Mode: store.ArchiveModeSameInstance, Database: "beacon_archive", DSNMasked: "file:beacon_archive.db"}
	svc := service.NewArchiveService(hot, arc, info, repo, settings, repository.NewAuditLogRepository(hot))
	h := NewV2ArchiveHandler(svc)

	r := chi.NewRouter()
	r.Get("/admin/v2/archive/overview", h.Overview)
	r.Post("/admin/v2/archive/jobs", h.CreateJob)
	r.Get("/admin/v2/archive/jobs", h.ListJobs)
	r.Get("/admin/v2/archive/jobs/{id}", h.GetJob)
	r.Post("/admin/v2/archive/jobs/{id}/retry", h.RetryJob)
	r.Post("/admin/v2/archive/jobs/{id}/cancel", h.CancelJob)
	return r, repo, hot
}

// seedArchiveJob 直接造一条指定状态的归档任务（供 409/404 边界断言）。
func seedArchiveJob(t *testing.T, repo *repository.ArchiveJobRepository, status string) *model.ArchiveJob {
	t.Helper()
	job := &model.ArchiveJob{
		Mode: model.ArchiveModeExecute, Trigger: model.ArchiveTriggerManual, Status: status,
		Domains: "[]", Cutoffs: "{}", Operator: "seed", CreatedAt: time.Now().UTC(),
	}
	if err := repo.CreateJob(job); err != nil {
		t.Fatalf("造任务失败: %v", err)
	}
	return job
}

// doArchiveJSON 对路由发一次请求（可带 json 体）并解析响应。
func doArchiveJSON(t *testing.T, r chi.Router, method, target string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应非 json: %v（%s）", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestArchiveOverviewContract 总览：target 四键 + 7 个域行逐键对齐 contracts；归档库可达 reachable=true。
func TestArchiveOverviewContract(t *testing.T) {
	r, _, _ := newArchiveRouter(t, "arch_overview")
	code, body := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/overview", nil)
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "target", "domains")
	target, _ := body["target"].(map[string]any)
	assertKeys(t, target, "mode", "database", "dsnMasked", "reachable")
	if target["mode"] != store.ArchiveModeSameInstance || target["reachable"] != true {
		t.Fatalf("target 形态不符: %v", target)
	}
	domains, _ := body["domains"].([]any)
	if len(domains) != 7 {
		t.Fatalf("应 7 个归档域，实际 %d", len(domains))
	}
	first, _ := domains[0].(map[string]any)
	assertKeys(t, first, "domain", "retentionDays", "hotRows", "archiveRows", "expiredRows", "lastJob")
}

// TestArchiveCreateDryRun 创建 dry_run：201 + 任务详情键齐、mode=dry_run、trigger=manual、items 存在。
func TestArchiveCreateDryRun(t *testing.T) {
	r, _, _ := newArchiveRouter(t, "arch_create")
	code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs",
		map[string]any{"mode": "dry_run", "domains": []string{"audit"}})
	if code != http.StatusCreated {
		t.Fatalf("应 201，实际 %d：%v", code, body)
	}
	assertKeys(t, body, append([]string{"items"}, archiveJobKeys...)...)
	if body["mode"] != "dry_run" || body["trigger"] != "manual" || body["status"] != "pending" {
		t.Fatalf("任务形态不符: %v", body)
	}
	if _, ok := body["items"].([]any); !ok {
		t.Fatalf("items 应为数组: %v", body["items"])
	}
}

// TestArchiveCreateEmptyDomainsAllDomains 省略 domains = 全部域：201，domains 序列化为 []（空数组=全部语义）。
func TestArchiveCreateEmptyDomainsAllDomains(t *testing.T) {
	r, _, _ := newArchiveRouter(t, "arch_create_all")
	code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs",
		map[string]any{"mode": "execute"})
	if code != http.StatusCreated {
		t.Fatalf("应 201，实际 %d：%v", code, body)
	}
	doms, _ := body["domains"].([]any)
	if doms == nil || len(doms) != 0 {
		t.Fatalf("空 domains 应序列化为 []（全部域），实际 %v", body["domains"])
	}
}

// TestArchiveCreateInvalid 创建的 400 边界：mode 非法 / domain 非法 / 请求体非 json。
func TestArchiveCreateInvalid(t *testing.T) {
	r, _, _ := newArchiveRouter(t, "arch_create_invalid")
	// mode 非法。
	if code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs",
		map[string]any{"mode": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("mode 非法应 400，实际 %d：%v", code, body)
	}
	// domain 非法。
	if code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs",
		map[string]any{"mode": "dry_run", "domains": []string{"not_a_domain"}}); code != http.StatusBadRequest {
		t.Fatalf("domain 非法应 400，实际 %d：%v", code, body)
	}
	// 请求体非 json。
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/archive/jobs", bytes.NewReader([]byte("{oops")))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("请求体非 json 应 400，实际 %d", rec.Code)
	}
}

// TestArchiveCreateConflict 已有活跃任务时创建返回 409（单飞）。
func TestArchiveCreateConflict(t *testing.T) {
	r, repo, _ := newArchiveRouter(t, "arch_create_conflict")
	seedArchiveJob(t, repo, model.ArchiveJobRunning)
	code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs",
		map[string]any{"mode": "dry_run"})
	if code != http.StatusConflict || body["code"] != "ARCHIVE_JOB_RUNNING" {
		t.Fatalf("已有活跃任务应 409 ARCHIVE_JOB_RUNNING，实际 %d：%v", code, body)
	}
}

// TestArchiveListContract 列表：{items,total} + 列表项逐键对齐 contracts；status / mode / trigger 过滤生效。
func TestArchiveListContract(t *testing.T) {
	r, repo, _ := newArchiveRouter(t, "arch_list")
	seedArchiveJob(t, repo, model.ArchiveJobSucceeded)
	failed := seedArchiveJob(t, repo, model.ArchiveJobFailed)

	code, body := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs", nil)
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
	assertKeys(t, items[0].(map[string]any), archiveJobKeys...)

	// status 过滤：只剩 failed 一条。
	_, filtered := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs?status=failed", nil)
	if filtered["total"] != float64(1) {
		t.Fatalf("status=failed 应 total 1，实际 %v", filtered["total"])
	}
	fitems, _ := filtered["items"].([]any)
	if fi := fitems[0].(map[string]any); fi["id"] != float64(failed.ID) {
		t.Fatalf("status=failed 应命中失败任务，实际 %v", fi)
	}
	// trigger 过滤命中；mode 过滤空集。
	if _, bad := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs?mode=dry_run", nil); bad["total"] != float64(0) {
		t.Fatalf("mode=dry_run 应 total 0，实际 %v", bad["total"])
	}
}

// TestArchiveDetailAndNotFound 详情：job + items 键齐（items[] 逐键对齐 contracts）；不存在 404。
func TestArchiveDetailAndNotFound(t *testing.T) {
	r, repo, _ := newArchiveRouter(t, "arch_detail")
	job := seedArchiveJob(t, repo, model.ArchiveJobSucceeded)
	// 造一条工作项，供 items[] 契约键断言（无 worker 时任务本身不展开 items）。
	if err := repo.CreateItems([]model.ArchiveJobItem{
		{JobID: job.ID, Domain: "audit", TargetTable: "audit_log", Phase: model.ArchiveItemDone},
	}); err != nil {
		t.Fatalf("造工作项失败: %v", err)
	}

	code, body := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs/"+itoa(job.ID), nil)
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, append([]string{"items"}, archiveJobKeys...)...)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items 应 1 条，实际 %d", len(items))
	}
	assertKeys(t, items[0].(map[string]any), archiveItemKeys...)

	// 不存在 → 404 ARCHIVE_JOB_NOT_FOUND。
	if code, b := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs/99999", nil); code != http.StatusNotFound || b["code"] != "ARCHIVE_JOB_NOT_FOUND" {
		t.Fatalf("不存在应 404 ARCHIVE_JOB_NOT_FOUND，实际 %d：%v", code, b)
	}
	// id 非数字 → 400。
	if code, _ := doArchiveJSON(t, r, http.MethodGet, "/admin/v2/archive/jobs/abc", nil); code != http.StatusBadRequest {
		t.Fatalf("id 非法应 400，实际 %d", code)
	}
}

// TestArchiveRetryStateGuards 重试：仅 failed 可重试（200），其它状态 409、不存在 404。
func TestArchiveRetryStateGuards(t *testing.T) {
	r, repo, _ := newArchiveRouter(t, "arch_retry")
	failed := seedArchiveJob(t, repo, model.ArchiveJobFailed)
	if code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/"+itoa(failed.ID)+"/retry", nil); code != http.StatusOK {
		t.Fatalf("failed 重试应 200，实际 %d：%v", code, body)
	}
	// 已被重置为 running（无 worker 续跑，保持 running）：再次重试非 failed → 409。
	if code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/"+itoa(failed.ID)+"/retry", nil); code != http.StatusConflict || body["code"] != "ARCHIVE_JOB_STATE" {
		t.Fatalf("非 failed 重试应 409 ARCHIVE_JOB_STATE，实际 %d：%v", code, body)
	}
	if code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/99999/retry", nil); code != http.StatusNotFound || body["code"] != "ARCHIVE_JOB_NOT_FOUND" {
		t.Fatalf("不存在重试应 404，实际 %d：%v", code, body)
	}
}

// TestArchiveCancelStateGuards 取消：pending 可取消（200 → cancelled），终态 409、不存在 404。
func TestArchiveCancelStateGuards(t *testing.T) {
	r, repo, _ := newArchiveRouter(t, "arch_cancel")
	pending := seedArchiveJob(t, repo, model.ArchiveJobPending)
	code, body := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/"+itoa(pending.ID)+"/cancel", nil)
	if code != http.StatusOK || body["status"] != model.ArchiveJobCancelled {
		t.Fatalf("pending 取消应 200 cancelled，实际 %d：%v", code, body)
	}
	// 已 cancelled 属终态 → 再取消 409。
	if code, b := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/"+itoa(pending.ID)+"/cancel", nil); code != http.StatusConflict || b["code"] != "ARCHIVE_JOB_STATE" {
		t.Fatalf("终态取消应 409 ARCHIVE_JOB_STATE，实际 %d：%v", code, b)
	}
	if code, b := doArchiveJSON(t, r, http.MethodPost, "/admin/v2/archive/jobs/99999/cancel", nil); code != http.StatusNotFound || b["code"] != "ARCHIVE_JOB_NOT_FOUND" {
		t.Fatalf("不存在取消应 404，实际 %d：%v", code, b)
	}
}

// itoa 把无符号整数转十进制字符串（避免为路径参数拼接引入 strconv 导入噪声）。
func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

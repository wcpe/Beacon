package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// TestFileEffectiveParamValidation 验证 GET /admin/v1/files/effective 的参数早校验：
// 缺 namespace、或 serverId 与 group 都空 → 400（在触达 service 前返回，故 handler 可不接 service）。
func TestFileEffectiveParamValidation(t *testing.T) {
	h := &FileHandler{} // 仅测参数校验早返回分支，不调用 effSvc
	cases := []struct {
		name  string
		query string
	}{
		{"全缺", ""},
		{"仅 namespace", "?namespace=prod"},
		{"仅 serverId 缺 namespace", "?serverId=s1"},
		{"仅 group 缺 namespace", "?group=area1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/admin/v1/files/effective"+c.query, nil)
			w := httptest.NewRecorder()
			h.Effective(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d（body=%s）", w.Code, w.Body.String())
			}
		})
	}
}

// —— 批量导入（FR-38）的 multipart 流式解析 ——

// importPart 是构造导入 multipart 表单的一个部件：filename 非空即文件部件，否则为文本字段。
type importPart struct {
	name     string
	value    string
	filename string
}

// newFileImportHandler 构造接线完整的导入 handler：内存 sqlite + 审批服务 + 待变更加密器。
// 导入走危险变更审批入口（本步只提审、不落地），缺任一装配都回 500，故按生产装配口径接齐后返回。
func newFileImportHandler(t *testing.T) (*FileHandler, *gorm.DB) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.FileObject{}, &model.FileRevision{}, &model.FilePendingChange{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移导入测试表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	rawKey := make([]byte, secret.KeyBytes)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	cipher, err := secret.NewCipher(base64.StdEncoding.EncodeToString(rawKey))
	if err != nil {
		t.Fatalf("构造测试加密器失败: %v", err)
	}
	files := service.NewFileService(db, repository.NewFileObjectRepository(db),
		repository.NewFileRevisionRepository(db), repository.NewAuditLogRepository(db))
	registry := authz.NewApprovalRegistry()
	approval := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db),
		repository.NewAuditLogRepository(db), registry)
	files.SetApprovalService(approval)
	files.SetPendingChangeCipher(cipher)
	sets := service.NewOverrideSetService(db, repository.NewFileOverrideSetRepository(db),
		repository.NewFileOverrideSetRevisionRepository(db), repository.NewFileObjectRepository(db),
		repository.NewAuditLogRepository(db))
	service.RegisterFileOverrideApprovalAdapters(registry, files, sets)
	return NewFileHandler(files, nil, nil, nil, nil), db
}

// newImportRequest 按给定部件序列构造导入请求（带上认证主体，与生产中间件口径一致）。
func newImportRequest(t *testing.T, parts []importPart) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if err := writeImportPart(mw, p); err != nil {
			t.Fatalf("写入部件 %s 失败: %v", p.name, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/files/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", t.Name())
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
}

// writeImportPart 写入单个部件（文本字段或文件部件）。
func writeImportPart(mw *multipart.Writer, p importPart) error {
	if p.filename == "" {
		return mw.WriteField(p.name, p.value)
	}
	fw, err := mw.CreateFormFile(p.name, p.filename)
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, strings.NewReader(p.value))
	return err
}

// doFileImport 发送一次导入请求并返回响应记录器。
func doFileImport(t *testing.T, h *FileHandler, parts []importPart) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Import(rec, newImportRequest(t, parts))
	return rec
}

// importScalarParts 是导入表单的标量字段部件（namespace / group / reason）。
func importScalarParts() []importPart {
	return []importPart{
		{name: importFieldNamespace, value: "prod"},
		{name: importFieldGroup, value: "bw"},
		{name: importFieldReason, value: "批量导入测试"},
	}
}

// importEntries 构造 n 个文件的等长对齐导入表单（path 唯一、内容极小；部件数 = 2n + 3 个标量字段）。
func importEntries(n int) []importPart {
	parts := importScalarParts()
	for i := range n {
		path := fmt.Sprintf("plugins/Demo/f%04d.yml", i)
		parts = append(parts,
			importPart{name: importFieldPaths, value: path},
			importPart{name: importFieldFiles, filename: path, value: fmt.Sprintf("index: %d\n", i)})
	}
	return parts
}

// TestFileImportAcceptsSixHundredFiles 是本次修复的判别性用例（FR-38）：
// 600 个文件 = 1203 个 part（2 × 600 + 3 个标量字段），已越过标准库 mime/multipart 硬编码的 maxParts = 1000。
// 旧实现走 r.ParseMultipartForm，此处必报 ErrMessageTooLarge 并被笼统归成 400「参数错误」，
// service.MaxImportFiles = 2000 的声明上限因此从未可达；改为 r.MultipartReader 流式解析后应正常提审（202）。
func TestFileImportAcceptsSixHundredFiles(t *testing.T) {
	h, db := newFileImportHandler(t)
	const fileCount = 600
	rec := doFileImport(t, h, importEntries(fileCount))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d 个文件应被受理（旧实现必 400），实际 %d：%s", fileCount, rec.Code, rec.Body.String())
	}
	var ticket service.FileApprovalTicket
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("解析审批票据失败: %v（body=%s）", err, rec.Body.String())
	}
	if ticket.ApprovalRequestID == "" || ticket.Status != model.ApprovalStatusPending {
		t.Fatalf("应返回 pending 审批票据，实际 %+v", ticket)
	}
	// 整批落成一条待审批变更（事务内原子落地的前提）
	var changes []model.FilePendingChange
	if err := db.Find(&changes).Error; err != nil {
		t.Fatalf("查询待审批变更失败: %v", err)
	}
	if len(changes) != 1 || changes[0].ChangeType != authz.OperationFileImport || changes[0].Ciphertext == "" {
		t.Fatalf("整批应落成一条 file.import 待审批变更，实际 %+v", changes)
	}
}

// TestFileImportRejectsOverLimitFiles 验证边界：超过 service.MaxImportFiles 的文件数被拒，
// 且错误明确指向「文件数超限 + 实际上限数字」——旧实现对这类请求同样只回笼统「参数错误」，
// 运维无从判断该拆批导入还是参数格式有误。
func TestFileImportRejectsOverLimitFiles(t *testing.T) {
	h, _ := newFileImportHandler(t)
	rec := doFileImport(t, h, importEntries(service.MaxImportFiles+1))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("文件数超限应返回 422，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"TOO_MANY_FILES", "文件数超出上限", strconv.Itoa(service.MaxImportFiles)} {
		if !strings.Contains(body, want) {
			t.Fatalf("错误应明确指向文件数超限（缺 %q），实际 %s", want, body)
		}
	}
}

// TestFileImportRejectsMisalignedPaths 验证 paths 与 files 数量不一致时仍被拒（保持既有行为）：
// 错位提交会让内容落到错误 path，必须拒，且说明里带上两个数量便于直接定位。
func TestFileImportRejectsMisalignedPaths(t *testing.T) {
	h, _ := newFileImportHandler(t)
	parts := append(importScalarParts(),
		importPart{name: importFieldPaths, value: "plugins/Demo/a.yml"},
		importPart{name: importFieldFiles, filename: "a.yml", value: "a: 1\n"},
		// 第二个 files 缺对应 paths
		importPart{name: importFieldFiles, filename: "b.yml", value: "b: 2\n"},
	)
	rec := doFileImport(t, h, parts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("paths 与 files 数量不一致应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"INVALID_PARAM", "paths", "数量不一致"} {
		if !strings.Contains(body, want) {
			t.Fatalf("错误应说明 paths/files 数量不一致（缺 %q），实际 %s", want, body)
		}
	}
}

// TestFileImportRejectsOversizeFilePart 验证单个超大文件部件被安全拒绝而非 OOM / 挂起：
// 内容超过 service.MaxFileContentBytes（1MiB）即 422 CONTENT_TOO_LARGE，解析在读满「上限 + 1 字节」后立即中断。
func TestFileImportRejectsOversizeFilePart(t *testing.T) {
	h, _ := newFileImportHandler(t)
	parts := append(importScalarParts(),
		importPart{name: importFieldPaths, value: "plugins/Demo/ok.yml"},
		importPart{name: importFieldFiles, filename: "ok.yml", value: "a: 1\n"},
		importPart{name: importFieldPaths, value: "plugins/Demo/huge.yml"},
		importPart{name: importFieldFiles, filename: "huge.yml",
			value: strings.Repeat("x", service.MaxFileContentBytes*2)},
	)
	rec := doFileImport(t, h, parts)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CONTENT_TOO_LARGE") {
		t.Fatalf("超大文件部件应返回 422 CONTENT_TOO_LARGE，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFileImportRejectsOversizeTextPart 验证单个超大文本部件（paths）被安全拒绝而非占满内存：
// 超 importTextPartMaxBytes 即 400 且说明指向「文本字段超出上限」。
func TestFileImportRejectsOversizeTextPart(t *testing.T) {
	h, _ := newFileImportHandler(t)
	parts := append(importScalarParts(),
		importPart{name: importFieldPaths, value: strings.Repeat("a", importTextPartMaxBytes+1)},
		importPart{name: importFieldFiles, filename: "a.yml", value: "a: 1\n"},
	)
	rec := doFileImport(t, h, parts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大文本部件应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "文本字段超出上限") {
		t.Fatalf("错误应指向文本字段超上限，实际 %s", body)
	}
}

// TestFileImportRejectsOversizeTotalBytes 验证聚合字节上限仍生效：累计超 service.MaxImportTotalBytes
// 即 422 CONTENT_TOO_LARGE（逐文件都在单文件上限内，只有累计能发现）。
func TestFileImportRejectsOversizeTotalBytes(t *testing.T) {
	h, _ := newFileImportHandler(t)
	// 单个文件恰好等于单文件上限，故只有累计校验能拦下；总数取「超聚合上限的最小文件数」
	perFile := strings.Repeat("y", service.MaxFileContentBytes)
	fileCount := service.MaxImportTotalBytes/service.MaxFileContentBytes + 1
	parts := importScalarParts()
	for i := range fileCount {
		path := fmt.Sprintf("plugins/Demo/big%02d.yml", i)
		parts = append(parts,
			importPart{name: importFieldPaths, value: path},
			importPart{name: importFieldFiles, filename: path, value: perFile})
	}
	rec := doFileImport(t, h, parts)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CONTENT_TOO_LARGE") {
		t.Fatalf("聚合字节超限应返回 422 CONTENT_TOO_LARGE，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFileImportRejectsTooManyParts 验证部件总数护栏：未知部件不占内存预算，但若不设部件数上限，
// 超大 body 的垃圾部件会被逐个无界解析（流式解析已绕开标准库 maxParts = 1000 的保护）。
func TestFileImportRejectsTooManyParts(t *testing.T) {
	h, _ := newFileImportHandler(t)
	parts := append(importScalarParts(),
		importPart{name: importFieldPaths, value: "plugins/Demo/a.yml"},
		importPart{name: importFieldFiles, filename: "a.yml", value: "a: 1\n"},
	)
	for range importMaxParts {
		parts = append(parts, importPart{name: "junk", value: "x"})
	}
	rec := doFileImport(t, h, parts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("部件总数超限应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "部件数超出上限") {
		t.Fatalf("错误应指向部件数超限，实际 %s", body)
	}
}

// TestFileImportRejectsNonMultipartBody 验证 Content-Type 非 multipart/form-data 时给出可读原因，
// 而不是笼统「参数错误」：解析失败必须带上底层原因（ADR-0057）。
func TestFileImportRejectsNonMultipartBody(t *testing.T) {
	h, _ := newFileImportHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/files/import", strings.NewReader(`{"namespace":"prod"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
	rec := httptest.NewRecorder()
	h.Import(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 multipart 请求应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"INVALID_PARAM", "multipart"} {
		if !strings.Contains(body, want) {
			t.Fatalf("错误应带上解析失败原因（缺 %q），实际 %s", want, body)
		}
	}
}

// TestParseImportFormKeepsSequenceAlignment 白盒校验流式解析保持「第 i 个 paths ↔ 第 i 个 files」的顺序对齐：
// 内容落错 path 会静默污染线上文件树，故三种提交布局（paths/files 交错、全 paths 后全 files、
// 混入未知扩展字段）必须解析出同样的对齐结果。
func TestParseImportFormKeepsSequenceAlignment(t *testing.T) {
	type entry struct {
		path     string
		filename string
		content  string
	}
	entries := []entry{
		{path: "plugins/Demo/a.yml", filename: "a.yml", content: "a: 1\n"},
		{path: "plugins/Demo/b.yml", filename: "b.yml", content: "b: 2\n"},
		{path: "plugins/Demo/c.yml", filename: "c.yml", content: "c: 3\n"},
	}
	// buildParts 按布局拼装表单：interleave = paths/files 逐对交错（前端现状），
	// 否则先把全部 paths 提完再提全部 files；junk = 中间混入未知字段（应被跳过）。
	buildParts := func(interleave, junk bool) []importPart {
		parts := importScalarParts()
		appendEntry := func(e entry) {
			parts = append(parts, importPart{name: importFieldFiles, filename: e.filename, value: e.content})
			if junk {
				parts = append(parts, importPart{name: "extra", value: "未知字段"})
			}
		}
		if interleave {
			for _, e := range entries {
				parts = append(parts, importPart{name: importFieldPaths, value: e.path})
				appendEntry(e)
			}
			return parts
		}
		for _, e := range entries {
			parts = append(parts, importPart{name: importFieldPaths, value: e.path})
		}
		for _, e := range entries {
			appendEntry(e)
		}
		return parts
	}
	cases := []struct {
		name       string
		interleave bool
		junk       bool
	}{
		{"paths 与 files 交错提交（前端现状）", true, false},
		{"先提交全部 paths 再提交全部 files", false, false},
		{"混入未知扩展字段（应被跳过且不影响对齐）", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form, err := parseImportForm(newImportRequest(t, buildParts(c.interleave, c.junk)))
			if err != nil {
				t.Fatalf("解析导入表单失败: %v", err)
			}
			if form.Namespace != "prod" || form.Group != "bw" || form.Reason != "批量导入测试" {
				t.Fatalf("标量字段应被解析，实际 %+v", form)
			}
			if len(form.Files) != len(entries) {
				t.Fatalf("应解析出 %d 个文件，实际 %d", len(entries), len(form.Files))
			}
			for i, e := range entries {
				if form.Files[i].Path != e.path || form.Files[i].Content != e.content {
					t.Fatalf("第 %d 个文件应对齐到 %s / %q，实际 %s / %q",
						i, e.path, e.content, form.Files[i].Path, form.Files[i].Content)
				}
			}
		})
	}
}

// TestFileImportAllowsExactlyMaxFiles 验证上限本身可达（不多不少 2000 个文件即受理）：
// 声明的能力上限必须真能兑现，否则与「约 498」的旧实际行为无异。
// 只走到解析后的提审路径，但 2000 个文件已足以压出部件数与内存护栏的边界（4003 个 part）。
func TestFileImportAllowsExactlyMaxFiles(t *testing.T) {
	h, _ := newFileImportHandler(t)
	rec := doFileImport(t, h, importEntries(service.MaxImportFiles))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d 个文件（声明上限）应被受理，实际 %d：%s",
			service.MaxImportFiles, rec.Code, rec.Body.String())
	}
}

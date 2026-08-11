//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// importFile 是一次 multipart 导入中的单个文件（相对 path + 内容）。
type importFile struct {
	path    string
	content string
}

// doImport 构造 multipart 导入提审请求（namespace/group + files/paths 等长对齐），返回状态码与解析后的响应体。
func doImport(t *testing.T, baseURL, namespace, group string, files []importFile) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("namespace", namespace)
	_ = mw.WriteField("group", group)
	_ = mw.WriteField("reason", "集成测试导入文件")
	for _, f := range files {
		// paths 字段按提交顺序与 files 部件一一对应
		_ = mw.WriteField("paths", f.path)
		fw, err := mw.CreateFormFile("files", f.path)
		if err != nil {
			t.Fatalf("创建文件部件失败: %v", err)
		}
		_, _ = io.Copy(fw, strings.NewReader(f.content))
	}
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, baseURL+"/admin/v1/files/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", t.Name()+"-file-import")
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("导入请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return resp.StatusCode, parsed
}

// TestFileRESTFlow 文件树托管 REST 集成（通道B，与配置对称）：建→取→发布→历史→取单版本→回滚→软删→404 全流程经 HTTP。
func TestFileRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/files"

	// 建（审批执行后首版 v1）
	id := createFileForTest(t, ts, "prod", "__GLOBAL__", "plugins/Demo/config.yml", "global", "a: 1\n")
	itemURL := base + "/" + itoa(id)

	// 取详情（含 content）
	code, got := doJSON(t, http.MethodGet, itemURL, nil)
	if code != http.StatusOK || got["content"] != "a: 1\n" {
		t.Fatalf("取文件应 200 且 content 正确，实际 %d：%v", code, got)
	}

	// 发布 v2
	publishFileForTest(t, ts, id, "a: 2\n", "改值")

	// 历史 2 版
	code, revs := doJSON(t, http.MethodGet, itemURL+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("历史应 200，实际 %d", code)
	}
	if items, _ := revs["items"].([]any); len(items) != 2 {
		t.Fatalf("历史应有 2 版，实际 %v", revs["items"])
	}

	// 取单版本 v1（含 content）
	code, rev1 := doJSON(t, http.MethodGet, itemURL+"/revisions/1", nil)
	if code != http.StatusOK || rev1["content"] != "a: 1\n" {
		t.Fatalf("取 v1 应 200 且 content=a: 1，实际 %d：%v", code, rev1)
	}

	// 回滚到 v1 → 产生 v3
	rollbackFileForTest(t, ts, id, 1, "回滚")

	// 软删
	deleteFileForTest(t, ts, id, "clean")

	// 取不存在的文件 → 404
	code, _ = doJSON(t, http.MethodGet, base+"/999999", nil)
	if code != http.StatusNotFound {
		t.Fatalf("取不存在文件应 404，实际 %d", code)
	}

	// 非法 id → 400
	code, _ = doJSON(t, http.MethodGet, base+"/abc", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 id 应 400，实际 %d", code)
	}
}

// TestFileImportRESTFlow 配置导入 REST 集成（FR-38）：multipart 上传一份目录到组 →
// 成组级文件（出现在 GET /files 与组内 manifest）→ 入 file.import 审计 → 路径穿越被拒。
func TestFileImportRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 上传两份文件到 prod 的 bw 组
	code, res := doImport(t, ts.URL, "prod", "bw", []importFile{
		{path: "plugins/Demo/config.yml", content: "a: 1\n"},
		{path: "plugins/Demo/lang/zh.yml", content: "你好\n"},
	})
	if code != http.StatusAccepted {
		t.Fatalf("导入提审应 202，实际 %d：%v", code, res)
	}
	applyApprovalTicket(t, ts, res)

	// 成组级 file_object：GET /files?group=bw 能列出这两份
	code, listed := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=bw", nil)
	if code != http.StatusOK {
		t.Fatalf("列组文件应 200，实际 %d", code)
	}
	if items, _ := listed["items"].([]any); len(items) != 2 {
		t.Fatalf("组内应有 2 个文件对象，实际 %v", listed["items"])
	}

	// 入审计：file.import 一条
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.import", nil)
	if code != http.StatusOK {
		t.Fatalf("查导入审计应 200，实际 %d", code)
	}
	if total, _ := audits["total"].(float64); total != 1 {
		t.Fatalf("应有 1 条 file.import 审计，实际 %v", audits["total"])
	}

	// 路径穿越被拒 → 400 INVALID_PATH
	code, bad := doImport(t, ts.URL, "prod", "bw", []importFile{
		{path: "../escape.yml", content: "x\n"},
	})
	if code != http.StatusBadRequest || bad["code"] != "INVALID_PATH" {
		t.Fatalf("穿越路径应 400 INVALID_PATH，实际 %d：%v", code, bad)
	}

	// 缺 group → 400 INVALID_PARAM
	code, _ = doImport(t, ts.URL, "prod", "", []importFile{
		{path: "a.yml", content: "x\n"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 group 应 400，实际 %d", code)
	}
}

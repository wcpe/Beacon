//go:build integration

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestServerPageResyncCommandVisible 服务器页重同步闭环：
// admin 下发 resync → agent 拉命令并回传结果 → 命令记录端点可按 serverId 查到 done，且不带瞬态载荷。
func TestServerPageResyncCommandVisible(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "srv-resync-1", "area1")

	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/srv-resync-1/resync?namespace=prod", nil)
	if code != http.StatusAccepted {
		t.Fatalf("触发重同步应 202，实际 %d：%v", code, cmd)
	}
	cmdID := int(cmd["commandId"].(float64))

	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=srv-resync-1", nil)
	if code != http.StatusOK || int(pulled["id"].(float64)) != cmdID || pulled["type"] != "resync-config" {
		t.Fatalf("agent 应拉到 resync-config 命令，实际 %d：%v", code, pulled)
	}

	code, _ = doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/commands/result", map[string]any{
		"commandId": cmdID,
		"ok":        true,
	})
	if code != http.StatusOK {
		t.Fatalf("回传重同步结果应 200，实际 %d", code)
	}

	code, page := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/commands?namespace=prod&serverId=srv-resync-1&type=resync-config", nil)
	if code != http.StatusOK {
		t.Fatalf("命令记录查询应 200，实际 %d：%v", code, page)
	}
	items := asSlice(page["items"])
	if len(items) != 1 {
		t.Fatalf("命令记录应有 1 条，实际 %v", page["items"])
	}
	first := items[0].(map[string]any)
	if first["status"] != "done" || first["type"] != "resync-config" {
		t.Fatalf("命令记录应显示 done resync-config，实际 %v", first)
	}
	if _, ok := first["payload"]; ok {
		t.Fatalf("命令记录不得带 payload：%v", first)
	}
}

// TestServerPageBrowseFullChain 服务器页文件浏览闭环：
// admin 发起浏览 GET → agent 拉 fs-browse 命令并回传目录 → admin 收到目录；readonly 对该 GET 副作用端点被拒。
func TestServerPageBrowseFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "srv-browse-1", "area1")

	type result struct {
		code int
		body map[string]any
		err  string
	}
	done := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/instances/srv-browse-1/browse?namespace=prod&op=list&path=&limit=50", nil)
		if err != nil {
			done <- result{err: err.Error()}
			return
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{err: err.Error()}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		if len(data) > 0 {
			_ = json.Unmarshal(data, &parsed)
		}
		done <- result{code: resp.StatusCode, body: parsed}
	}()

	var cmdID int
	for i := 0; i < 20; i++ {
		code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=srv-browse-1", nil)
		if code == http.StatusOK {
			if pulled["type"] != "fs-browse" {
				t.Fatalf("agent 应拉到 fs-browse 命令，实际 %v", pulled)
			}
			cmdID = int(pulled["id"].(float64))
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cmdID == 0 {
		t.Fatal("agent 未拉到 fs-browse 命令")
	}

	code, body := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/browse-result", map[string]any{
		"namespace": "prod",
		"serverId":  "srv-browse-1",
		"commandId": cmdID,
		"ok":        true,
		"result": map[string]any{
			"path":    "",
			"entries": []map[string]any{{"name": "plugins.yml", "dir": false, "size": 42, "isText": true, "overThreshold": false}},
			"offset":  0,
			"limit":   50,
			"total":   1,
			"hasMore": false,
		},
	})
	if code != http.StatusOK {
		t.Fatalf("agent 回传浏览结果应 200，实际 %d：%v", code, body)
	}

	select {
	case got := <-done:
		if got.err != "" {
			t.Fatalf("浏览请求失败：%s", got.err)
		}
		if got.code != http.StatusOK {
			t.Fatalf("浏览请求应 200，实际 %d：%v", got.code, got.body)
		}
		entries := asSlice(got.body["entries"])
		if len(entries) != 1 || entries[0].(map[string]any)["name"] != "plugins.yml" {
			t.Fatalf("浏览结果应透传目录项，实际 %v", got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("浏览请求未被 agent 回传唤醒")
	}

	roKey, _ := createKey(t, ts.URL, "ro-browse", "readonly")
	code, body = doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances/srv-browse-1/browse?namespace=prod&op=list", roKey, false, nil)
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("readonly 浏览 GET 副作用端点应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
}

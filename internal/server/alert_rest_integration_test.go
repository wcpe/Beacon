//go:build integration

package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime/alert"
)

// TestAlertListRESTFlow 健康告警站内信 REST 集成（FR-28）：初始空；向通道投递一条后经 HTTP 读回（最新在前）。
func TestAlertListRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	url := ts.URL + "/admin/v1/alerts"

	// 初始无告警（站内信进程内、控制面重启清零）
	code, empty := doJSON(t, http.MethodGet, url, nil)
	if code != http.StatusOK || len(asSlice(empty["items"])) != 0 {
		t.Fatalf("初始告警应空，实际 %d %v", code, empty["items"])
	}

	// 直接向当前测试服的站内信通道投递一条告警（模拟健康扫描派发）
	_ = testAlertInbox.Notify(context.Background(), alert.Alert{
		Namespace: "prod", ServerID: "alert-s1", Address: "10.0.0.7:25565",
		PrevStatus: "online", Status: "lost", At: time.Now().UTC(),
	})

	// 经 HTTP 读回该告警
	code, got := doJSON(t, http.MethodGet, url, nil)
	if code != http.StatusOK {
		t.Fatalf("查告警应 200，实际 %d", code)
	}
	items := asSlice(got["items"])
	if len(items) != 1 {
		t.Fatalf("应有 1 条告警，实际 %v", got["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["serverId"] != "alert-s1" || first["status"] != "lost" || first["prevStatus"] != "online" {
		t.Fatalf("告警字段错误：%v", first)
	}
}

//go:build integration

package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestSensitiveConfigPlaintextRequiresApprovedGrant 锁定旧 V1 配置正文路由不能
// 直接返回敏感内容。批准后的 grant 消费端点才是唯一正文出口。
func TestSensitiveConfigPlaintextRequiresApprovedGrant(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"
	secret := "password: 不应直出"

	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "sensitive.yml",
		"scopeLevel": "global", "format": "yaml", "content": secret, "sensitive": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("创建敏感配置失败：%d %#v", code, created)
	}
	id, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("创建响应缺 id：%#v", created)
	}
	itemURL := fmt.Sprintf("%s/%d", base, int(id))
	if code, _ = doJSON(t, http.MethodPut, itemURL, map[string]any{"content": "password: 第二版"}); code != http.StatusOK {
		t.Fatalf("发布敏感配置第二版失败：%d", code)
	}

	for _, path := range []string{
		itemURL,
		itemURL + "/revisions/1",
		itemURL + "/diff?from=1&to=2",
		base + "/effective?namespace=prod&group=__GLOBAL__",
	} {
		code, body := doJSON(t, http.MethodGet, path, nil)
		if code != http.StatusConflict || body["code"] != "operation_requires_approval" {
			t.Fatalf("敏感正文旧路由应要求审批：path=%s code=%d body=%#v", path, code, body)
		}
		if strings.Contains(fmt.Sprint(body), secret) || strings.Contains(fmt.Sprint(body), "password: 第二版") {
			t.Fatalf("拒绝响应不得包含敏感正文：path=%s body=%#v", path, body)
		}
	}
}

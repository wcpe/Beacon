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
	code, normal := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "normal.yml",
		"scopeLevel": "global", "format": "yaml", "content": "enabled: true",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建普通配置失败：%d %#v", code, normal)
	}
	normalID, ok := normal["id"].(float64)
	if !ok {
		t.Fatalf("普通配置创建响应缺 id：%#v", normal)
	}
	code, normalRead := doJSON(t, http.MethodGet, fmt.Sprintf("%s/%d", base, int(normalID)), nil)
	if code != http.StatusOK || normalRead["content"] != "enabled: true" {
		t.Fatalf("普通配置读取必须保持直出：code=%d body=%#v", code, normalRead)
	}

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
	publishConfigForTest(t, ts, int(id), "password: 第二版", "发布敏感配置第二版")

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

	code, ticket := doJSONWithHeaders(t, http.MethodPost, itemURL+"/plaintext/approval-requests", map[string]any{"reason": "排查数据库连接异常"}, map[string]string{"Idempotency-Key": "sensitive-config-read"})
	if code != http.StatusAccepted {
		t.Fatalf("敏感配置读取应创建审批申请：%d %#v", code, ticket)
	}
	requestID, _ := ticket["requestId"].(string)
	if requestID == "" || strings.Contains(fmt.Sprint(ticket), "password:") {
		t.Fatalf("审批申请响应不得泄露正文：%#v", ticket)
	}
	ts.approveAndRun(t, requestID)
	grantID := sensitiveGrantIDForTest(t, ts, requestID)
	code, consumed := doJSON(t, http.MethodPost, itemURL+"/plaintext/grants/"+grantID+"/consume", nil)
	if code != http.StatusOK || consumed["content"] != "password: 第二版" {
		t.Fatalf("原申请人应只经一次性授权读取正文：code=%d body=%#v", code, consumed)
	}
	code, consumed = doJSON(t, http.MethodPost, itemURL+"/plaintext/grants/"+grantID+"/consume", nil)
	if code != http.StatusGone || consumed["code"] != "sensitive_access_consumed" {
		t.Fatalf("敏感配置授权必须只能消费一次：code=%d body=%#v", code, consumed)
	}
}

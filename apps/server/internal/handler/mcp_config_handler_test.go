package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/config"
)

// TestMCPConfigHandlerEnabled 验证启用态回显部署配置，且不含敏感项。
func TestMCPConfigHandlerEnabled(t *testing.T) {
	h := NewMCPConfigHandler(config.MCPConfig{
		Enabled:               true,
		PublicBaseURL:         "https://beacon.example.com",
		TrustedProxyCIDRs:     []string{"10.0.0.0/8"},
		AllowInsecureInternal: false,
		AllowedHosts:          []string{"beacon.example.com"},
		AllowApprovalDecide:   true,
		AllowMachineRegister:  false,
	})

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/mcp/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body mcpConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if !body.Enabled {
		t.Fatalf("enabled 应为 true")
	}
	if body.PublicBaseURL != "https://beacon.example.com" {
		t.Fatalf("publicBaseUrl 应为配置值，实际 %q", body.PublicBaseURL)
	}
	if len(body.TrustedProxyCIDRs) != 1 || body.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("trustedProxyCidrs 应为 [10.0.0.0/8]，实际 %v", body.TrustedProxyCIDRs)
	}
	if !body.AllowApprovalDecide {
		t.Fatalf("allowApprovalDecide 应为 true")
	}
	if body.AllowMachineRegister {
		t.Fatalf("allowMachineRegister 应为 false")
	}
	// 反代模式：显式允许内网明文为 false，故非直连。
	if body.DirectMode {
		t.Fatalf("配置了可信网段且未允许明文，directMode 应为 false")
	}
}

// TestMCPConfigHandlerDisabled 验证未启用时仍返回 200 且 enabled=false（而非错误）。
// 这是管理台展示「入口未启用」配置指引的前提。
func TestMCPConfigHandlerDisabled(t *testing.T) {
	h := NewMCPConfigHandler(config.MCPConfig{})

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/mcp/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("未启用时状态码仍应为 200，实际 %d", rec.Code)
	}
	var body mcpConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if body.Enabled {
		t.Fatalf("enabled 应为 false")
	}
	// nil 切片须序列化为 []，避免前端为 null 单独分支。
	if body.TrustedProxyCIDRs == nil || body.AllowedHosts == nil {
		t.Fatalf("CIDR / Host 列表应为空切片而非 nil，实际 %v / %v", body.TrustedProxyCIDRs, body.AllowedHosts)
	}
	if !strings.Contains(rec.Body.String(), `"trustedProxyCidrs":[]`) {
		t.Fatalf("响应应含空数组而非 null，实际 %s", rec.Body.String())
	}
}

// TestMCPConfigHandlerDirectMode 验证直连模式判定与 MCPProxyPolicy 口径一致：
// 入口已启用 + 显式允许内网明文 + 无可信网段 = 直连。
func TestMCPConfigHandlerDirectMode(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.MCPConfig
		directExp bool
	}{
		{"启用+明文无网段为直连", config.MCPConfig{Enabled: true, AllowInsecureInternal: true}, true},
		{"启用+明文有网段非直连", config.MCPConfig{Enabled: true, AllowInsecureInternal: true, TrustedProxyCIDRs: []string{"10.0.0.0/8"}}, false},
		{"启用+无明文无网段非直连", config.MCPConfig{Enabled: true}, false},
		// 未启用时入口不挂载、无模式可言；此处须为 false 以对齐 MCPProxyPolicy.Direct()。
		{"未启用即使明文无网段也非直连", config.MCPConfig{AllowInsecureInternal: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := directMode(tc.cfg); got != tc.directExp {
				t.Fatalf("directMode 应为 %v，实际 %v", tc.directExp, got)
			}
		})
	}
}

// TestMCPConfigHandlerResponseKeysAreExact 以白名单方式锁定响应字段集合：
// 这是"结构上不泄露"的真正检验——新增任何字段都会让本测试失败并强制 review，
// 而不是靠遍历关键词（那种断言只能测出自己刚写死的结构，属同义反复）。
func TestMCPConfigHandlerResponseKeysAreExact(t *testing.T) {
	h := NewMCPConfigHandler(config.MCPConfig{Enabled: true, PublicBaseURL: "https://beacon.example.com"})

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/mcp/config", nil))

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	want := []string{
		"enabled", "publicBaseUrl", "trustedProxyCidrs", "allowInsecureInternal",
		"allowedHosts", "allowApprovalDecide", "allowMachineRegister", "directMode",
	}
	if len(raw) != len(want) {
		t.Fatalf("字段数应为 %d，实际 %d：%v", len(want), len(raw), raw)
	}
	for _, key := range want {
		if _, ok := raw[key]; !ok {
			t.Fatalf("缺少字段 %q，实际 %v", key, raw)
		}
	}
}

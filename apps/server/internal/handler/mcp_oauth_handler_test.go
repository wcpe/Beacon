package handler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestToMCPClientViewLifecycleFields 验证客户端视图带上生命周期字段，
// 使管理台能回答"谁建的、何时建的、何时被吊销"。
func TestToMCPClientViewLifecycleFields(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 2, 11, 30, 0, 0, time.UTC)
	revoked := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	view := toMCPClientView(&model.MCPOAuthClient{
		ClientID:     "mcp_abc",
		DisplayName:  "内网自动化",
		SecretPrefix: "mcs_xyzw",
		// 填哨兵值：若视图误带 secret 字段，白名单断言会因字段数不符而失败；
		// 若此处留空，即便误带也序列化为空串，测不出来。
		SecretHash:    "SENTINEL_HASH_SHOULD_NEVER_APPEAR",
		Profile:       model.MCPClientProfileAutomation,
		Status:        model.MCPClientStatusRevoked,
		SecretVersion: 3,
		CreatedBy:     "human:admin",
		CreatedAt:     created,
		UpdatedAt:     updated,
		RevokedAt:     &revoked,
	})

	if view.ClientID != "mcp_abc" || view.Profile != model.MCPClientProfileAutomation {
		t.Fatalf("基础字段映射错误: %+v", view)
	}
	if view.CreatedBy != "human:admin" {
		t.Fatalf("createdBy 应为 human:admin，实际 %q", view.CreatedBy)
	}
	if !view.CreatedAt.Equal(created) || !view.UpdatedAt.Equal(updated) {
		t.Fatalf("时间戳映射错误: %+v", view)
	}
	if view.RevokedAt == nil || !view.RevokedAt.Equal(revoked) {
		t.Fatalf("revokedAt 应映射吊销时间，实际 %v", view.RevokedAt)
	}

	// 白名单式锁定字段集合：新增任何字段（尤其是 secret 类）都会让本测试失败并强制 review。
	// 用 reflect 校验结构体字段而非 JSON 键——JSON 序列化受 omitempty 影响，空值字段会被省略，
	// 靠 json tag 断言会漏掉"新增了带 omitempty 的敏感字段"这类回归。
	typ := reflect.TypeOf(mcpClientView{})
	want := []string{
		"ClientID", "DisplayName", "SecretPrefix", "Profile", "Status",
		"SecretVersion", "CreatedBy", "CreatedAt", "UpdatedAt", "RevokedAt",
	}
	if typ.NumField() != len(want) {
		t.Fatalf("字段数应为 %d，实际 %d", len(want), typ.NumField())
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Fatalf("第 %d 个字段应为 %q，实际 %q", i, name, got)
		}
	}

	// 序列化结果同样不含 secret 明文（哨兵值不应出现）。
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(raw), "SENTINEL_HASH_SHOULD_NEVER_APPEAR") {
		t.Fatalf("视图泄露了 secret 哈希: %s", raw)
	}
}

// TestToMCPClientViewActiveOmitsRevokedAt 验证未吊销时 revokedAt 被省略，
// 前端据此区分「从未吊销」与「零值时间」。
func TestToMCPClientViewActiveOmitsRevokedAt(t *testing.T) {
	view := toMCPClientView(&model.MCPOAuthClient{
		ClientID:      "mcp_live",
		DisplayName:   "观测客户端",
		Profile:       model.MCPClientProfileObserver,
		Status:        model.MCPClientStatusActive,
		SecretVersion: 1,
		CreatedBy:     "human:admin",
		CreatedAt:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})

	if view.RevokedAt != nil {
		t.Fatalf("未吊销客户端 revokedAt 应为 nil，实际 %v", view.RevokedAt)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(raw), "revokedAt") {
		t.Fatalf("未吊销时响应应省略 revokedAt，实际 %s", raw)
	}
}

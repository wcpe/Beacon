package server

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// TestMCPSharedToolsAreDiscoverableForBothProfiles 断言目录中所有「非 automation
// 专属」工具对 observer 与 automation **都**可见。
//
// 期望集合直接取自 mcpToolCatalog，不再维护第二份只读工具名字符串清单——
// 后者是重复真源，会与目录静默漂移。
func TestMCPSharedToolsAreDiscoverableForBothProfiles(t *testing.T) {
	for _, profile := range []string{model.MCPClientProfileObserver, model.MCPClientProfileAutomation} {
		names := MCPToolNames(profile)
		for _, spec := range mcpToolCatalog {
			if spec.AutomationOnly {
				continue
			}
			if !containsMCPTool(names, spec.Name) {
				t.Fatalf("两 profile 共用工具未暴露: profile=%s tool=%s", profile, spec.Name)
			}
		}
	}
}

func TestMCPMessageHistoryViewExcludesPayloadAndPlayerIdentifier(t *testing.T) {
	view := mcpMessageHistoryView(service.MsgPage{Items: []model.MsgTrace{{
		MessageID: "msg-1", TargetPlayer: "player-uuid", Hops: `[{"node":"internal"}]`, PayloadSize: 12, PayloadStored: true, CreatedAt: time.Unix(0, 0),
	}}})
	item := view["items"].([]map[string]any)[0]
	for _, forbidden := range []string{"payload", "targetPlayer", "hops"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("消息历史 MCP 响应泄露受限字段: %s", forbidden)
		}
	}
}

func TestMCPAuditHistoryViewExcludesDetailAndClientIP(t *testing.T) {
	view := mcpAuditHistoryView([]model.AuditLog{{Detail: "token=secret", ClientIP: "192.0.2.1"}}, 1)
	item := view["items"].([]map[string]any)[0]
	for _, forbidden := range []string{"detail", "clientIp"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("审计 MCP 响应泄露受限字段: %s", forbidden)
		}
	}
}

package server

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

func TestMCPReadToolsAreDiscoverableForBothProfiles(t *testing.T) {
	for _, profile := range []string{model.MCPClientProfileObserver, model.MCPClientProfileAutomation} {
		for _, name := range mcpReadToolNames {
			if !containsMCPTool(MCPToolNames(profile), name) {
				t.Fatalf("只读工具未对 profile 暴露: profile=%s tool=%s", profile, name)
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

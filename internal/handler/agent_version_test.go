package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/runtime"
)

// TestRegisterRequestParsesAgentVersion 验证注册请求体解析 agent 自身构建版本（FR-86，见 ADR-0039）。
func TestRegisterRequestParsesAgentVersion(t *testing.T) {
	body := `{"namespace":"prod","serverId":"lobby-1","role":"bukkit","agentVersion":"0.12.0"}`
	var req registerRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.AgentVersion != "0.12.0" {
		t.Fatalf("agentVersion 解析错误：%q", req.AgentVersion)
	}
}

// TestRegisterRequestBackwardCompatNoAgentVersion 验证旧 agent 不发 agentVersion 时缺省为空串（向后兼容）。
func TestRegisterRequestBackwardCompatNoAgentVersion(t *testing.T) {
	body := `{"namespace":"prod","serverId":"lobby-1","role":"bukkit"}`
	var req registerRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.AgentVersion != "" {
		t.Fatalf("缺 agentVersion 键应缺省为空串，实际 %q", req.AgentVersion)
	}
}

// TestInstanceViewOutputsAgentVersion 验证实例视图输出 agentVersion（供管理台展示，FR-86）。
func TestInstanceViewOutputsAgentVersion(t *testing.T) {
	view := toInstanceView(&runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", AgentVersion: "0.12.0",
	}, nil, zeroHealthCtx)
	if view.AgentVersion != "0.12.0" {
		t.Fatalf("实例视图 agentVersion 应为 0.12.0，实际 %q", view.AgentVersion)
	}
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(out), `"agentVersion":"0.12.0"`) {
		t.Fatalf("实例视图应输出 agentVersion，实际 %s", out)
	}
}

// TestInstanceViewAgentVersionEmptyForOldAgent 验证旧 agent（未上报版本）实例视图 agentVersion 为空串。
func TestInstanceViewAgentVersionEmptyForOldAgent(t *testing.T) {
	view := toInstanceView(&runtime.Instance{Namespace: "prod", ServerID: "lobby-1", Role: "bukkit"}, nil, zeroHealthCtx)
	if view.AgentVersion != "" {
		t.Fatalf("旧 agent 实例视图 agentVersion 应为空串，实际 %q", view.AgentVersion)
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

// guardProbeInput 是执行面包装测试的入参类型：只用于驱动包装层，与真实工具无关。
type guardProbeInput struct {
	Reason string `json:"reason"`
}

// guardProbeTool 是执行面包装测试使用的工具名，取自目录中的 critical 档。
const guardProbeTool = "beacon.system.update.apply"

// connectMCPTestSession 把 in-memory 客户端接到给定 server；会话随测试结束关闭。
//
// 与 listRegisteredTools 的差别：这里接的是**调用方已构造好的那个 server**，
// 因此在开关翻转前后调用的始终是同一个注册快照——正是本项要兜住的场景。
func connectMCPTestSession(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("服务端连接失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "execution-guard-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// mcpSessionHasTool 判定会话可见的工具集合是否包含指定工具。
func mcpSessionHasTool(t *testing.T, session *mcp.ClientSession, name string) bool {
	t.Helper()
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("枚举工具失败: %v", err)
		}
		if tool.Name == name {
			return true
		}
	}
	return false
}

// mcpResultText 拼接工具结果里的文本与结构化内容，便于断言拒执理由。
func mcpResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			b.Write(raw)
		}
	}
	return b.String()
}

// TestMCPToolExecutionBlockedMatchesCatalog 逐项核对执行面判定（FR-242）。
//
// 开关关闭时目录内**每一个**工具都必须放行（关闭态与现状逐项一致，且不对
// 「未登记」追加任何新语义）；开关开启时恰好 critical 档被拦、低/高风险档不受影响。
func TestMCPToolExecutionBlockedMatchesCatalog(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })
	mcpResetRejections()

	auth.SetMCPProductionMode(false)
	for _, spec := range mcpToolCatalog {
		if mcpToolExecutionBlocked(spec.Name) {
			t.Fatalf("开关关闭时工具 %s 被执行面拦截：关闭态行为必须与现状逐项一致", spec.Name)
		}
	}
	if mcpToolExecutionBlocked("beacon.unregistered.probe") {
		t.Fatalf("开关关闭时未登记工具被执行面拦截：关闭态不得引入新语义")
	}

	auth.SetMCPProductionMode(true)
	blocked, allowed := 0, 0
	for _, spec := range mcpToolCatalog {
		want := spec.RiskLevel == MCPRiskCritical
		if got := mcpToolExecutionBlocked(spec.Name); got != want {
			t.Fatalf("工具 %s（风险等级 %s）执行面判定=%v，期望 %v", spec.Name, spec.RiskLevel, got, want)
		}
		if want {
			blocked++
		} else {
			allowed++
		}
	}
	if blocked == 0 || allowed == 0 {
		t.Fatalf("执行面判定失去区分度：被拦 %d 项、放行 %d 项", blocked, allowed)
	}
	// 未登记工具（风险等级未知）在生产模式下 fail-closed 拒执。
	if !mcpToolExecutionBlocked("beacon.unregistered.probe") {
		t.Fatalf("生产模式下未登记工具未被拒执：fail-closed 失效")
	}
}

// TestMCPProductionModeGuardBlocksHandlerWithoutSideEffect 直接驱动包装后的 handler：
// 断言 critical 调用被拒、原 handler **完全未执行**（零副作用）、留痕含调用主体，
// 且结构化输出非 null；关闭开关后同一包装 handler 行为与现状逐项一致。
func TestMCPProductionModeGuardBlocksHandlerWithoutSideEffect(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })
	mcpResetRejections()

	invocations := 0
	handler := mcpGuardToolExecution(guardProbeTool, func(_ context.Context, _ *mcp.CallToolRequest, _ guardProbeInput) (*mcp.CallToolResult, map[string]any, error) {
		invocations++
		return &mcp.CallToolResult{}, map[string]any{"status": "executed"}, nil
	})
	principal := auth.MCPPrincipal("client-guard", "执行面包装测试", model.MCPClientProfileAutomation)
	ctx := render.WithTraceID(auth.WithPrincipal(context.Background(), principal), "trace-fr242")

	auth.SetMCPProductionMode(true)
	res, out, err := handler(ctx, &mcp.CallToolRequest{}, guardProbeInput{Reason: "生产模式拒执测试"})
	if err != nil {
		t.Fatalf("拒执应以工具结果表达，而不是协议错误: %v", err)
	}
	if !res.IsError {
		t.Fatalf("critical 工具在生产模式下未被拒执: %+v", res)
	}
	text := mcpResultText(res)
	if !strings.Contains(text, mcpProductionModeRejectedReason) || !strings.Contains(text, guardProbeTool) {
		t.Fatalf("拒执结果缺少可辨识的理由与工具名: %q", text)
	}
	if out == nil {
		t.Fatalf("拒执返回了 nil 结构化输出：SDK 会按输出 schema 判成协议错误")
	}
	if invocations != 0 {
		t.Fatalf("拒执后原 handler 仍被执行 %d 次：执行面必须零副作用", invocations)
	}
	traces := mcpRejectionsSnapshot()
	if len(traces) != 1 || traces[0].Tool != guardProbeTool || traces[0].ClientID != principal.ID ||
		traces[0].Profile != principal.Role || traces[0].TraceID != "trace-fr242" {
		t.Fatalf("拒执留痕不完整: %+v", traces)
	}

	// 回归：关闭开关后同一个包装 handler 必须原样放行。
	auth.SetMCPProductionMode(false)
	mcpResetRejections()
	res, out, err = handler(ctx, &mcp.CallToolRequest{}, guardProbeInput{Reason: "非生产模式"})
	if err != nil || res.IsError || invocations != 1 || out["status"] != "executed" {
		t.Fatalf("关闭开关后行为与现状不一致: err=%v res=%+v 调用次数=%d out=%v", err, res, invocations, out)
	}
	if len(mcpRejectionsSnapshot()) != 0 {
		t.Fatalf("关闭开关时不应产生拒执留痕")
	}
}

// TestMCPProductionModeRejectsCallOnAlreadyRegisteredTool 复现本项要兜住的窗口：
// 工具在开关关闭时已完成注册（客户端缓存了旧清单 / 注册期快照早于开关翻转），
// 随后开关开启——对**同一个 server** 的直接调用必须被拒，理由可辨识。
func TestMCPProductionModeRejectsCallOnAlreadyRegisteredTool(t *testing.T) {
	t.Cleanup(func() {
		auth.SetMCPProductionMode(false)
		auth.SetMCPApprovalDecide(false)
	})
	mcpResetRejections()
	auth.SetMCPProductionMode(false)

	registry := newFullMCPToolRegistry()
	principal := auth.MCPPrincipal("client-stale-list", "缓存旧清单的客户端", model.MCPClientProfileAutomation)
	session := connectMCPTestSession(t, registry.NewMCPServer(principal))

	// 反证前提：此刻工具确实在 server 里；否则本测试会退化成「未注册即不可调用」，失去意义。
	if !mcpSessionHasTool(t, session, guardProbeTool) {
		t.Fatalf("前置条件不成立：开关关闭时 %s 未注册进 server", guardProbeTool)
	}

	auth.SetMCPProductionMode(true)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      guardProbeTool,
		Arguments: map[string]any{"reason": "生产模式拒执测试", "idempotencyKey": "fr242-registered"},
	})
	if err != nil {
		t.Fatalf("调用不应成为协议错误: %v", err)
	}
	if !res.IsError || !strings.Contains(mcpResultText(res), mcpProductionModeRejectedReason) {
		t.Fatalf("已注册的 critical 工具在生产模式下未被拒执: %+v 文本=%q", res, mcpResultText(res))
	}
	if traces := mcpRejectionsSnapshot(); len(traces) != 1 || traces[0].Tool != guardProbeTool {
		t.Fatalf("拒执未留痕: %+v", traces)
	}
}

// mcpGuardTestRegistrar 是测试用工具注册方：刻意绕过注册面判定（直接 mcp.AddTool），
// 模拟「工具意外可见」——此时发现面已无从拦截，只剩执行面兜底。
type mcpGuardTestRegistrar struct {
	tool    string
	invoked *atomic.Bool
}

func (r mcpGuardTestRegistrar) NewMCPServer(auth.Principal) *mcp.Server {
	server := newEmptyMCPServer()
	mcp.AddTool(server, &mcp.Tool{Name: r.tool}, mcpGuardToolExecution(r.tool, func(_ context.Context, _ *mcp.CallToolRequest, _ guardProbeInput) (*mcp.CallToolResult, map[string]any, error) {
		r.invoked.Store(true)
		return &mcp.CallToolResult{}, map[string]any{"status": "executed"}, nil
	}))
	return server
}

// TestMCPProductionModeExecutionGuardOnHTTPPath 端到端验证真机路径：
// 经真实 HTTP MCP 入口直接调用一个「本不该可调用却出现在 server 里」的 critical 工具，
// 断言生产模式下被拒、原 handler 未执行、且留痕带上认证主体（clientId / profile）；
// 关闭开关时同一条路径行为与现状一致（正常执行）。
func TestMCPProductionModeExecutionGuardOnHTTPPath(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })

	policy, err := NewMCPProxyPolicy(true, "https://beacon.example", nil,
		MCPProxyOptions{AllowInsecureInternal: true, AllowedHosts: []string{"beacon.example"}})
	if err != nil {
		t.Fatalf("构造 MCP 代理策略失败: %v", err)
	}
	principal := auth.MCPPrincipal("client-http", "HTTP 路径测试客户端", model.MCPClientProfileAutomation)
	tool := "beacon.credentials.api-key.create"
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"reason":"生产模式拒执测试"}}}`

	for _, production := range []bool{false, true} {
		name := "生产模式关闭时行为与现状一致"
		if production {
			name = "生产模式下 critical 工具直接调用被拒"
		}
		t.Run(name, func(t *testing.T) {
			auth.SetMCPProductionMode(production)
			mcpResetRejections()

			var invoked atomic.Bool
			handler := NewPublicMCPHandlerWithTools(
				mcpVerifierStub{principal: principal}, policy,
				mcpGuardTestRegistrar{tool: tool, invoked: &invoked}, true)

			req := mcpPost(call)
			req.Host = "beacon.example"
			req.RemoteAddr = "127.0.0.1:1234"
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != 200 {
				t.Fatalf("MCP HTTP 状态=%d，期望 200；响应=%s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			traces := mcpRejectionsSnapshot()
			if production {
				if !strings.Contains(body, mcpProductionModeRejectedReason) {
					t.Fatalf("生产模式下 critical 工具未被拒执；响应=%s", body)
				}
				if invoked.Load() {
					t.Fatalf("拒执后原 handler 仍被执行：执行面必须零副作用")
				}
				if len(traces) != 1 || traces[0].Tool != tool || traces[0].ClientID != principal.ID || traces[0].Profile != principal.Role {
					t.Fatalf("拒执留痕未带上认证主体: %+v", traces)
				}
				return
			}
			if !strings.Contains(body, "executed") {
				t.Fatalf("关闭开关时工具未按现状执行；响应=%s", body)
			}
			if len(traces) != 0 {
				t.Fatalf("关闭开关时不应产生拒执留痕: %+v", traces)
			}
		})
	}
}

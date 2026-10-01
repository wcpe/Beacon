package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// ── 测试脚手架 ──

// mcpInvocationSink 是 MCPInvocationRecorder 的测试替身：把记录到的行收进切片供断言。
// 带锁——middleware 无状态，但测试可能在并发场景下驱动它。
type mcpInvocationSink struct {
	mu   sync.Mutex
	rows []model.MCPInvocation
}

func (s *mcpInvocationSink) Record(row model.MCPInvocation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
}

// take 返回已记录行的副本并清空缓冲，便于「一次调用一行」的逐段断言。
func (s *mcpInvocationSink) take() []model.MCPInvocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.MCPInvocation(nil), s.rows...)
	s.rows = nil
	return out
}

// only 断言恰好记录一行并返回它——「每次调用留痕」的最低要求。
func (s *mcpInvocationSink) only(t *testing.T) model.MCPInvocation {
	t.Helper()
	rows := s.take()
	if len(rows) != 1 {
		t.Fatalf("期望恰好 1 行流水，实际 %d 行: %+v", len(rows), rows)
	}
	return rows[0]
}

// mcpInvocationTestPrincipal 是流水测试的统一主体：clientId 用可辨识的固定值。
var mcpInvocationTestPrincipal = auth.MCPPrincipal("client-invocation-test", "流水测试客户端", model.MCPClientProfileAutomation)

// mcpInjectInvocationContext 模拟 MCPHandler.ServeHTTP 的主体 + 地址 + traceId 注入（FR-240 §3.1）。
//
// 必须挂成**最外层** middleware（后挂的在外层，见 SDK addMiddleware 的反向包装）：
// 流水 middleware 要在「主体已注入」的 ctx 上取 clientId / profile / traceId / clientIp，
// 与真机路径（MCPHandler 注入后再进 SDK）的次序一致。
func mcpInjectInvocationContext(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		ctx = auth.WithClientIP(ctx, "10.0.0.7")
		ctx = render.WithTraceID(ctx, "trace-fr240")
		ctx = auth.WithPrincipal(ctx, mcpInvocationTestPrincipal)
		return next(ctx, method, req)
	}
}

// mcpAuditTestServer 构造一个挂了流水 middleware 的 server，并注册给定工具。
//
// 不走 NewMCPServer 而手工挂载，是为了把「被测 middleware」与「真实入口的主体 / traceId 注入」
// 解耦：注入层用 mcpInjectInvocationContext 显式模拟，其余（含 mcpAddTool 的 FR-242 包装）全走生产代码。
func mcpAuditTestServer(t *testing.T, rec MCPInvocationRecorder, tool *mcp.Tool, handler mcp.ToolHandlerFor[map[string]any, map[string]any]) *mcp.Server {
	t.Helper()
	server := newEmptyMCPServer()
	mcpAttachInvocationAudit(server, rec)
	server.AddReceivingMiddleware(mcpInjectInvocationContext)
	// 经 mcpAddTool 注册：与真实路径一致（含 FR-242 执行面包装）。
	mcpAddTool(server, tool, handler)
	return server
}

// callMCPTool 经 in-memory transport 真实调用一次工具（与 FR-236/242 覆盖测试同手法）。
//
// 返回 (结果, 错误)：SDK 对未知工具以 JSON-RPC error 上抛（该次调用没有 Result），
// 调用方据需要断言错误或结果。
func callMCPTool(t *testing.T, server *mcp.Server, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	session := connectMCPTestSession(t, server)
	return session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
}

// mustCallMCPTool 调用并断言不返回协议错误（成功 / 业务拒绝 / 拒执路径都用它）。
func mustCallMCPTool(t *testing.T, server *mcp.Server, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := callMCPTool(t, server, name, arguments)
	if err != nil {
		t.Fatalf("调用工具 %s 失败: %v", name, err)
	}
	return res
}

// ── §5 第 1 条：每次调用留痕且字段完整 ──

// TestMCPInvocationRecordedWithCompleteFields 验证一次成功调用恰好留一行，且 clientId / profile /
// toolName / riskLevel / result / durationMs / traceId / createdAt 齐备，主体与目录值都与真源一致。
func TestMCPInvocationRecordedWithCompleteFields(t *testing.T) {
	const tool = "beacon.audit.events.list"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return &mcp.CallToolResult{}, map[string]any{"items": []any{}}, nil
		})

	mustCallMCPTool(t, server, tool, map[string]any{"page": 1})

	row := sink.only(t)
	if row.ClientID != mcpInvocationTestPrincipal.ID || row.Profile != mcpInvocationTestPrincipal.Role {
		t.Fatalf("主体字段与认证主体不一致: clientId=%q profile=%q", row.ClientID, row.Profile)
	}
	if row.ToolName != tool {
		t.Fatalf("工具名=%q，期望 %q", row.ToolName, tool)
	}
	spec, _ := mcpToolSpecByName(tool)
	if row.RiskLevel != spec.RiskLevel {
		t.Fatalf("风险等级=%q，与目录 %q 不一致", row.RiskLevel, spec.RiskLevel)
	}
	if row.Result != model.MCPInvocationResultOK || row.Reason != "" {
		t.Fatalf("成功调用应为 ok 且无原因，实际 result=%q reason=%q", row.Result, row.Reason)
	}
	if row.TraceID != "trace-fr240" {
		t.Fatalf("traceId=%q，期望与响应头同值的 trace-fr240", row.TraceID)
	}
	if row.ClientIP != "10.0.0.7" {
		t.Fatalf("clientIp=%q，期望入口注入的 10.0.0.7", row.ClientIP)
	}
	if row.CreatedAt.IsZero() || row.DurationMs < 0 {
		t.Fatalf("createdAt / durationMs 未填充: %+v", row)
	}
	if !model.IsValidMCPInvocationResult(row.Result) || !model.IsValidMCPInvocationReason(row.Reason) ||
		!model.IsValidMCPInvocationRiskLevel(row.RiskLevel) {
		t.Fatalf("枚举列取值越界: result=%q reason=%q risk=%q", row.Result, row.Reason, row.RiskLevel)
	}
	// 主键 UUIDv7 的内嵌毫秒必须与 created_at 同源（同一次取整到毫秒的 time）。
	ms, ok := store.TimeMsFromUUIDv7(row.InvocationID)
	if !ok {
		t.Fatalf("invocationId 不是合法 UUIDv7 文本: %q", row.InvocationID)
	}
	if ms != row.CreatedAt.UnixMilli() {
		t.Fatalf("invocationId 内嵌毫秒 %d 与 created_at %d 不同源", ms, row.CreatedAt.UnixMilli())
	}
}

// ── §5 第 2 条：参数正文不入库（三档各一例）──

// TestMCPInvocationArgumentsNeverPersist 以 canary 构造三档参数，扫描整行**所有文本列**确认正文不入库，
// 同时核对 A 档落 target_digest、B 档落 键名:字节数、C 档只落裸键名，arg_bytes 等于原文长度。
func TestMCPInvocationArgumentsNeverPersist(t *testing.T) {
	const tool = "beacon.files.create"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return &mcp.CallToolResult{}, map[string]any{}, nil
		})

	canary := "SECRET-CANARY-9f3a1c"
	arguments := map[string]any{
		"path":           "/plugins/x.jar",                   // A 档：目标标识（唯一允许存值的一档）
		"namespace":      "prod",                             // A 档
		"idempotencyKey": "idem-0123456789",                  // B 档（含 key）：只记字节数
		"content":        canary + strings.Repeat("X", 1200), // B 档（含 content）：只记字节数
		"reason":         canary,                             // B 档（含 reason）：只记字节数
		"opaqueFlag":     true,                               // C 档：只记裸键名
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}

	mustCallMCPTool(t, server, tool, arguments)
	row := sink.only(t)

	assertNoCanaryInRow(t, row, canary)
	if row.ArgBytes != len(raw) {
		t.Fatalf("argBytes=%d，期望参数原文长度 %d", row.ArgBytes, len(raw))
	}
	if !strings.Contains(row.TargetDigest, "path=/plugins/x.jar") {
		t.Fatalf("target_digest 未含目标标识: %q", row.TargetDigest)
	}
	if !strings.Contains(row.TargetDigest, "namespace=prod") {
		t.Fatalf("target_digest 未按声明顺序取 A 档键: %q", row.TargetDigest)
	}
	if strings.Contains(row.TargetDigest, "content") || strings.Contains(row.TargetDigest, "idempotencyKey") {
		t.Fatalf("B 档键不得进 target_digest: %q", row.TargetDigest)
	}
	// B 档：只记 键名:字节数。
	if !strings.Contains(row.ArgKeys, "content:") || !strings.Contains(row.ArgKeys, "reason:") {
		t.Fatalf("arg_keys 未按 B 档记 键名:字节数: %q", row.ArgKeys)
	}
	if want := "content:" + strconv.Itoa(len(canary+strings.Repeat("X", 1200))); !strings.Contains(row.ArgKeys, want) {
		t.Fatalf("arg_keys 中 content 的字节数不符，期望 %q，实际 %q", want, row.ArgKeys)
	}
	if !strings.Contains(row.ArgKeys, "reason:") || strings.Contains(row.ArgKeys, "reason:"+canary) {
		t.Fatalf("B 档键的值不得出现在 arg_keys: %q", row.ArgKeys)
	}
	// C 档：只记裸键名。
	if !strings.Contains(row.ArgKeys, "opaqueFlag") || strings.Contains(row.ArgKeys, "opaqueFlag:") {
		t.Fatalf("C 档键只应记裸键名: %q", row.ArgKeys)
	}
	// 键名按升序拼接。
	if want := "content,idempotencyKey,namespace,opaqueFlag,path,reason"; !mcpKeysPrefixMatch(row.ArgKeys, want) {
		t.Fatalf("arg_keys 未按键名升序拼接，期望前缀 %q，实际 %q", want, row.ArgKeys)
	}
}

// mcpKeysPrefixMatch 比对 arg_keys 的键名序列（忽略 B 档的 :字节数 后缀），用于断言升序拼接。
func mcpKeysPrefixMatch(argKeys, want string) bool {
	got := make([]string, 0, 8)
	for _, item := range strings.Split(argKeys, ",") {
		got = append(got, strings.SplitN(item, ":", 2)[0])
	}
	wantNames := strings.Split(want, ",")
	if len(got) != len(wantNames) {
		return false
	}
	for i := range got {
		if got[i] != wantNames[i] {
			return false
		}
	}
	return true
}

// assertNoCanaryInRow 扫描流水行**所有**文本列，确认 canary 未以任何形式落库。
func assertNoCanaryInRow(t *testing.T, row model.MCPInvocation, canary string) {
	t.Helper()
	fields := map[string]string{
		"invocation_id": row.InvocationID, "client_id": row.ClientID, "profile": row.Profile,
		"tool_name": row.ToolName, "risk_level": row.RiskLevel, "result": row.Result,
		"reason": row.Reason, "target_digest": row.TargetDigest, "arg_keys": row.ArgKeys,
		"trace_id": row.TraceID, "client_ip": row.ClientIP, "error_summary": row.ErrorSummary,
	}
	for column, value := range fields {
		if strings.Contains(value, canary) {
			t.Fatalf("参数正文 canary 落入列 %s: %q", column, value)
		}
	}
}

// ── §5 第 3 条：未知工具 ──

// TestMCPInvocationUnknownTool 验证调用未登记工具名时留痕为 rejected / unknown_tool / unknown，
// 且即便 SDK 以 error 形式上抛也不落 handler_error。
func TestMCPInvocationUnknownTool(t *testing.T) {
	sink := &mcpInvocationSink{}
	// 用空 registry 构造：目录内工具未注册，但 middleware 仍挂在（recorder 非 nil）。
	registry := &MCPToolRegistry{invocations: sink}
	server := registry.NewMCPServer(mcpInvocationTestPrincipal)

	// SDK 对未注册工具以 JSON-RPC error 上抛（本次调用没有 Result），这正是分类要覆盖的形态。
	if _, err := callMCPTool(t, server, "beacon.unregistered.probe", map[string]any{"namespace": "prod"}); err == nil {
		t.Fatalf("前置条件不成立：未注册工具应被 SDK 以 unknown tool 上抛")
	}

	row := sink.only(t)
	if row.Result != model.MCPInvocationResultRejected || row.Reason != model.MCPInvocationReasonUnknownTool {
		t.Fatalf("未登记工具应为 rejected/unknown_tool，实际 %s/%s", row.Result, row.Reason)
	}
	if row.RiskLevel != model.MCPInvocationRiskUnknown {
		t.Fatalf("未登记工具风险等级应为 unknown，实际 %q", row.RiskLevel)
	}
	if row.ToolName != "beacon.unregistered.probe" {
		t.Fatalf("未知工具名应原样记录，实际 %q", row.ToolName)
	}
	if !strings.Contains(row.ErrorSummary, mcpUnknownToolPhrase) {
		t.Fatalf("error_summary 应保留 SDK 原文，实际 %q", row.ErrorSummary)
	}
}

// TestMCPInvocationCatalogToolHiddenBySwitchStaysUnknownTool 覆盖「目录内但本次未注册」这一夹缝：
// 审批决定工具在开关关闭时被 mcpToolDiscoverable 隐去，调用落到 SDK 的 unknown tool。
//
// 主判据（工具名不在 mcpToolCatalog）在这里为假，只有 SDK 的错误消息片段才认得出，
// 因此该次调用仍须记 unknown_tool，而不是退化成 handler_error（漏行、错分档都算回归）。
func TestMCPInvocationCatalogToolHiddenBySwitchStaysUnknownTool(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPApprovalDecide(false) })
	auth.SetMCPApprovalDecide(false)
	// 前置条件：该工具确在目录内（否则本测试退化为上一条的重复）。
	const tool = "beacon.approvals.approve"
	if _, ok := mcpToolSpecByName(tool); !ok {
		t.Fatalf("前置条件不成立：%s 应在 mcpToolCatalog 内", tool)
	}
	if mcpToolDiscoverable(tool) {
		t.Fatalf("前置条件不成立：审批决定开关关闭时 %s 不应可发现", tool)
	}

	sink := &mcpInvocationSink{}
	registry := newFullMCPToolRegistry()
	registry.SetInvocationRecorder(sink)
	server := registry.NewMCPServer(mcpInvocationTestPrincipal)

	if _, err := callMCPTool(t, server, tool, map[string]any{"reason": "开关关闭"}); err == nil {
		t.Fatalf("前置条件不成立：未注册的工具应被 SDK 以 unknown tool 上抛")
	}
	row := sink.only(t)
	if row.Result != model.MCPInvocationResultRejected || row.Reason != model.MCPInvocationReasonUnknownTool {
		t.Fatalf("目录内但未注册的调用应为 rejected/unknown_tool，实际 %s/%s", row.Result, row.Reason)
	}
	if row.RiskLevel != MCPRiskCritical {
		t.Fatalf("风险等级应取自目录（critical），实际 %q", row.RiskLevel)
	}
}

// ── §5 第 4 条：业务拒绝 ──

// TestMCPInvocationBusinessRejection 验证走 mcpRejectedResultWithReason 的业务拒绝
// 落 rejected / handler_rejected，且 error_summary 只留原因（去掉统一前缀）、已脱敏。
func TestMCPInvocationBusinessRejection(t *testing.T) {
	const tool = "beacon.files.delete"
	const reason = "目标文件不存在且 token=abcdef123456"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return mcpRejectedResultWithReason(reason)
		})

	res := mustCallMCPTool(t, server, tool, map[string]any{"id": 12})
	if !res.IsError {
		t.Fatalf("拒绝结果应标记 IsError: %+v", res)
	}
	row := sink.only(t)
	if row.Result != model.MCPInvocationResultRejected || row.Reason != model.MCPInvocationReasonHandlerRejected {
		t.Fatalf("业务拒绝应为 rejected/handler_rejected，实际 %s/%s", row.Result, row.Reason)
	}
	if !strings.Contains(row.ErrorSummary, "目标文件不存在") {
		t.Fatalf("error_summary 未含拒绝原因: %q", row.ErrorSummary)
	}
	if strings.Contains(row.ErrorSummary, mcpInvocationRejectedPrefix) {
		t.Fatalf("error_summary 应去掉统一拒绝前缀: %q", row.ErrorSummary)
	}
	if strings.Contains(row.ErrorSummary, "abcdef123456") || !strings.Contains(row.ErrorSummary, "***") {
		t.Fatalf("error_summary 未按 ADR-0057 脱敏凭据: %q", row.ErrorSummary)
	}
}

// TestMCPInvocationBusinessRejectionWithoutReasonKeepsPrefix 验证无原因拒绝存前缀本身（spec §3.4）。
func TestMCPInvocationBusinessRejectionWithoutReasonKeepsPrefix(t *testing.T) {
	const tool = "beacon.files.delete"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return mcpRejectedResult()
		})

	mustCallMCPTool(t, server, tool, map[string]any{})
	row := sink.only(t)
	if row.Result != model.MCPInvocationResultRejected || row.Reason != model.MCPInvocationReasonHandlerRejected {
		t.Fatalf("无原因拒绝仍应记 rejected/handler_rejected，实际 %s/%s", row.Result, row.Reason)
	}
	if row.ErrorSummary != mcpInvocationRejectedPrefix {
		t.Fatalf("无原因拒绝应存前缀本身，实际 %q", row.ErrorSummary)
	}
}

// ── §5 第 5 条：失败与拒执分类（含与 FR-242 的接口断言）──

// TestMCPInvocationHandlerError 验证 handler 返回普通 error → fail / handler_error，
// error_summary 非空且已脱敏（SDK 包装路径经 GetError 判出，不漏行）。
func TestMCPInvocationHandlerError(t *testing.T) {
	const tool = "beacon.config.publish"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return nil, nil, errors.New("上游失败 password=hunter2")
		})

	mustCallMCPTool(t, server, tool, map[string]any{"id": 7})
	row := sink.only(t)
	if row.Result != model.MCPInvocationResultFail || row.Reason != model.MCPInvocationReasonHandlerError {
		t.Fatalf("handler error 应为 fail/handler_error，实际 %s/%s", row.Result, row.Reason)
	}
	if row.ErrorSummary == "" {
		t.Fatalf("handler error 的 error_summary 不得为空")
	}
	if strings.Contains(row.ErrorSummary, "hunter2") || !strings.Contains(row.ErrorSummary, "***") {
		t.Fatalf("error_summary 未脱敏: %q", row.ErrorSummary)
	}
}

// TestMCPInvocationProductionModeClassification 逐项核对与 FR-242 的接口（spec §3.6 / §5 第 5 条）：
//
//   - 生产模式开启时 critical 调用经 mcpGuardToolExecution 拒执 → rejected / production_mode，
//     且原 handler 零执行；
//   - 生产模式关闭时同一工具的正常调用 → 成功（或至少不是 production_mode），
//     同时再对「业务拒绝」分支断言 handler_rejected——证明判据确实由开关驱动；
//   - **可测不变量**：生产模式下 critical 工具的任何调用都不得出现 handler_rejected。
func TestMCPInvocationProductionModeClassification(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })
	const tool = guardProbeTool // beacon.system.update.apply，目录中的 critical 档

	cases := []struct {
		name       string
		production bool
		// handler 是「业务逻辑本该做什么」；guard 命中时它必须完全不被执行。
		handler    mcp.ToolHandlerFor[map[string]any, map[string]any]
		wantResult string
		wantReason string
		// wantCalled 期望业务 handler 是否真的执行（guard 命中即零执行，§3.6 第 2 条）。
		wantCalled bool
	}{
		{
			name: "生产模式开启：guard 拒执且业务零执行", production: true,
			handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return &mcp.CallToolResult{}, map[string]any{"status": "executed"}, nil
			},
			wantResult: model.MCPInvocationResultRejected, wantReason: model.MCPInvocationReasonProductionMode,
			wantCalled: false,
		},
		{
			name: "生产模式关闭：同一工具放行并成功", production: false,
			handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return &mcp.CallToolResult{}, map[string]any{"status": "executed"}, nil
			},
			wantResult: model.MCPInvocationResultOK, wantReason: "", wantCalled: true,
		},
		{
			name: "生产模式关闭：业务拒绝落 handler_rejected", production: false,
			handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return mcpRejectedResultWithReason("业务侧拒绝")
			},
			wantResult: model.MCPInvocationResultRejected, wantReason: model.MCPInvocationReasonHandlerRejected,
			wantCalled: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			inner := tc.handler
			sink := &mcpInvocationSink{}
			// 关键次序：先关开关把工具注册进 server（发现面会隐藏 critical），再翻转开关——
			// 复现「客户端缓存旧清单 / 注册快照早于开关翻转」这一执行面兜底要挡的真实窗口。
			auth.SetMCPProductionMode(false)
			server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
				func(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
					called = true
					return inner(ctx, req, in)
				})
			auth.SetMCPProductionMode(tc.production)

			mustCallMCPTool(t, server, tool, map[string]any{"reason": "接口断言"})
			row := sink.only(t)
			if row.Result != tc.wantResult || row.Reason != tc.wantReason {
				t.Fatalf("期望 %s/%s，实际 %s/%s", tc.wantResult, tc.wantReason, row.Result, row.Reason)
			}
			if called != tc.wantCalled {
				t.Fatalf("业务 handler 执行=%v，期望 %v（guard 必须先于业务逻辑）", called, tc.wantCalled)
			}
			if tc.production {
				if !strings.Contains(row.ErrorSummary, mcpProductionModeRejectedReason) {
					t.Fatalf("拒执摘要应含统一理由: %q", row.ErrorSummary)
				}
				// §3.6 可测不变量：生产模式下 critical 调用不得出现 handler_rejected。
				if row.Reason == model.MCPInvocationReasonHandlerRejected {
					t.Fatalf("生产模式下 critical 工具出现 handler_rejected —— guard 被绕过（工具名 %s）", tool)
				}
			}
		})
	}
}

// TestMCPInvocationProductionModeReasonConsistency 双向断言 §3.6 的一致性兜底判据。
//
// 以**真实的 guard 调用 + 真实分类**逐工具驱动：同一份 catalog、同一个开关，
// 断言「文本判据命中的拒绝」与「critical 且开关打开」给出相同结论，两个方向都不可矛盾：
//   - 不得出现「文本命中却非 critical」（非 critical 工具不该拿到统一拒执理由）；
//   - 不得出现「critical + 开关开却记 handler_rejected」（guard 被绕过）。
func TestMCPInvocationProductionModeReasonConsistency(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })
	ctx := auth.WithPrincipal(context.Background(), mcpInvocationTestPrincipal)
	req := &mcp.CallToolRequest{}
	type probeInput struct{}

	for _, production := range []bool{false, true} {
		auth.SetMCPProductionMode(production)
		for _, spec := range mcpToolCatalog {
			// 真实 guard 包装下的「业务拒绝」handler：guard 不命中时它会执行并返回业务拒绝。
			businessReject := func(context.Context, *mcp.CallToolRequest, probeInput) (*mcp.CallToolResult, map[string]any, error) {
				return mcpRejectedResultWithReason("业务侧拒绝")
			}
			res, _, err := mcpGuardToolExecution(spec.Name, businessReject)(ctx, req, probeInput{})
			if err != nil {
				t.Fatalf("工具 %s：guard 包装不得返回协议错误: %v", spec.Name, err)
			}
			row := mcpClassifyInvocation(ctx, spec.Name, nil, res, err, 0)

			guardShouldReject := production && spec.RiskLevel == MCPRiskCritical
			byText := strings.HasPrefix(mcpRejectionSummary(res), mcpProductionModeRejectedReason)
			if byText != guardShouldReject {
				t.Fatalf("工具 %s（%s）生产模式=%v：文本判据=%v，期望 %v（两判据必须恒等）",
					spec.Name, spec.RiskLevel, production, byText, guardShouldReject)
			}
			if guardShouldReject {
				if row.Reason != model.MCPInvocationReasonProductionMode {
					t.Fatalf("工具 %s（%s）生产模式=开：期望 production_mode，实际 %s —— guard 被绕过",
						spec.Name, spec.RiskLevel, row.Reason)
				}
				if byText && spec.RiskLevel != MCPRiskCritical {
					t.Fatalf("工具 %s（%s）：非 critical 工具拿到统一拒执理由", spec.Name, spec.RiskLevel)
				}
				continue
			}
			// guard 不命中：业务拒绝必须落 handler_rejected，绝不能被误判成 production_mode。
			if row.Result != model.MCPInvocationResultRejected || row.Reason != model.MCPInvocationReasonHandlerRejected {
				t.Fatalf("工具 %s（%s）生产模式=%v：业务拒绝应落 rejected/handler_rejected，实际 %s/%s",
					spec.Name, spec.RiskLevel, production, row.Result, row.Reason)
			}
		}
	}
}

// ── §5 第 9 条：middleware 自身健壮（panic 恢复 + 无 recorder 时 no-op）──

// TestMCPInvocationMiddlewarePanicRecovery 验证 middleware 的 defer 恢复路径：
// 下游 handler panic 时先记一行 internal_error（不携带 panic 值文本，防携带数据），
// 再把 panic 原样上抛给 HTTP 层 recoverMiddleware（既有行为不变）。
//
// 直接驱动 middleware 而非走会话：panic 若被 SDK 吞掉会表现为调用挂起，无法在测试里观察。
func TestMCPInvocationMiddlewarePanicRecovery(t *testing.T) {
	const tool = "beacon.audit.events.list"
	sink := &mcpInvocationSink{}
	var next mcp.MethodHandler = func(context.Context, string, mcp.Request) (mcp.Result, error) {
		panic("handler 崩了 token=super-secret")
	}
	ctx := render.WithTraceID(auth.WithClientIP(
		auth.WithPrincipal(context.Background(), mcpInvocationTestPrincipal), "10.0.0.7"), "trace-panic")
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool}}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("handler panic 应原样上抛给 HTTP 层 recoverMiddleware，不得被 middleware 吞掉")
		}
		row := sink.only(t)
		if row.Result != model.MCPInvocationResultFail || row.Reason != model.MCPInvocationReasonInternalError {
			t.Fatalf("panic 恢复路径应为 fail/internal_error，实际 %s/%s", row.Result, row.Reason)
		}
		if row.ErrorSummary != "panic" {
			t.Fatalf("panic 路径只记固定文案 panic（防携带数据），实际 %q", row.ErrorSummary)
		}
		if row.TraceID != "trace-panic" || row.ToolName != tool {
			t.Fatalf("panic 行仍须带全上下文: %+v", row)
		}
	}()
	_, _ = mcpAuditToolCall(ctx, req, sink, next)
}

// TestMCPInvocationMiddlewareNoopWithoutRecorder 验证 recorder 为 nil 时 middleware 不挂载：
// 既有单测与未装配路径行为零变化（spec §3.1）。
func TestMCPInvocationMiddlewareNoopWithoutRecorder(t *testing.T) {
	registry := newFullMCPToolRegistry() // invocations 为零值 nil
	if registry.invocations != nil {
		t.Fatalf("前置条件不成立：新构造的 registry 不应自带 recorder")
	}
	// 既有的目录一致性测试走的正是这条路径（recorder 为 nil）：构造必须成功、不报错。
	server := registry.NewMCPServer(mcpInvocationTestPrincipal)
	if server == nil {
		t.Fatalf("recorder 为 nil 时 NewMCPServer 仍须正常返回 server")
	}
	// 纯 nil 场景下挂载函数自身也是 no-op（防构造期误触发 SDK 调用）。
	mcpAttachInvocationAudit(newEmptyMCPServer(), nil)
}

// TestMCPInvocationRecordedOnHTTPPath 端到端验证真机路径：经真实 HTTP MCP 入口调用工具后留痕一行，
// 主体与地址（X-Forwarded-For 首段）都取自入口注入，而不是请求体自报值。
func TestMCPInvocationRecordedOnHTTPPath(t *testing.T) {
	policy, err := NewMCPProxyPolicy(true, "https://beacon.example", nil,
		MCPProxyOptions{AllowInsecureInternal: true, AllowedHosts: []string{"beacon.example"}})
	if err != nil {
		t.Fatalf("构造 MCP 代理策略失败: %v", err)
	}
	const tool = "beacon.audit.events.list"
	sink := &mcpInvocationSink{}
	registrar := mcpInvocationTestRegistrar{rec: sink, tool: tool}

	handler := NewPublicMCPHandlerWithTools(
		mcpVerifierStub{principal: mcpInvocationTestPrincipal}, policy, registrar, true)

	// 与真机路由一致：traceMiddleware 在最外层注入 traceId + 回写 X-Trace-Id 响应头（spec §3.2）。
	wrapped := traceMiddleware(handler)
	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"namespace":"prod"}}}`
	req := mcpPost(call)
	req.Host = "beacon.example"
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.1.1.1")
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, req)

	if recorder.Code != 200 {
		t.Fatalf("MCP HTTP 状态=%d，期望 200；响应=%s", recorder.Code, recorder.Body.String())
	}
	row := sink.only(t)
	if row.ClientID != mcpInvocationTestPrincipal.ID || row.Profile != mcpInvocationTestPrincipal.Role {
		t.Fatalf("主体应取自认证主体而非请求体: %+v", row)
	}
	if row.ClientIP != "203.0.113.9" {
		t.Fatalf("clientIp=%q，期望 X-Forwarded-For 首段 203.0.113.9", row.ClientIP)
	}
	if row.ToolName != tool || row.Result != model.MCPInvocationResultOK {
		t.Fatalf("HTTP 路径留痕不完整: %+v", row)
	}
	// traceId 必须与响应头 X-Trace-Id 同值（契约：一次 HTTP POST 对应一次 tools/call）。
	if want := recorder.Header().Get("X-Trace-Id"); row.TraceID != want || want == "" {
		t.Fatalf("traceId=%q，期望与响应头 X-Trace-Id %q 同值", row.TraceID, want)
	}
}

// mcpInvocationTestRegistrar 是挂了流水 middleware 的测试注册方（经 NewMCPServer 同样路径挂载）。
type mcpInvocationTestRegistrar struct {
	rec  MCPInvocationRecorder
	tool string
}

func (r mcpInvocationTestRegistrar) NewMCPServer(auth.Principal) *mcp.Server {
	server := newEmptyMCPServer()
	mcpAttachInvocationAudit(server, r.rec)
	mcpAddTool(server, &mcp.Tool{Name: r.tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return &mcp.CallToolResult{}, map[string]any{"items": []any{}}, nil
		})
	return server
}

// ── §3.3 纯函数：三档规则与截断 ──

// TestMCPSummarizeArgumentsRules 逐条核对三档判定、声明顺序、截断与非法输入的 fail-closed 行为。
func TestMCPSummarizeArgumentsRules(t *testing.T) {
	cases := []struct {
		name        string
		args        string
		wantDigest  string
		wantArgKeys string
	}{
		{
			name:        "非 JSON 对象不回落存原文",
			args:        `["SECRET-CANARY"]`,
			wantDigest:  "",
			wantArgKeys: "",
		},
		{
			name:        "解析失败不回落存原文",
			args:        `{not json`,
			wantDigest:  "",
			wantArgKeys: "",
		},
		{
			name:        "A 档按声明顺序取前 3 个标量",
			args:        `{"id":9,"namespace":"prod","serverId":"srv-1","identityId":"ignored"}`,
			wantDigest:  "namespace=prod;serverId=srv-1;id=9",
			wantArgKeys: "id,identityId,namespace,serverId",
		},
		{
			name:        "A 档跳过数组对象与 null 且不占名额",
			args:        `{"namespace":{"nested":1},"serverId":null,"zone":["a"],"group":"g1"}`,
			wantDigest:  "group=g1",
			wantArgKeys: "group,namespace,serverId,zone",
		},
		{
			name:        "B 档只记键名字节数",
			args:        `{"content":"abc","note":"de"}`,
			wantDigest:  "",
			wantArgKeys: "content:3,note:2",
		},
		{
			// zeta / omega 既不属 A 档白名单，也不含任何 B 档内容类子串（含嵌套对象的键同样是 C 档）。
			name:        "C 档只记裸键名",
			args:        `{"zeta":1,"omega":{"x":1}}`,
			wantDigest:  "",
			wantArgKeys: "omega,zeta",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			digest, argKeys := mcpSummarizeArguments(json.RawMessage(tc.args))
			if digest != tc.wantDigest {
				t.Fatalf("target_digest=%q，期望 %q", digest, tc.wantDigest)
			}
			if argKeys != tc.wantArgKeys {
				t.Fatalf("arg_keys=%q，期望 %q", argKeys, tc.wantArgKeys)
			}
		})
	}
}

// TestMCPSummarizeArgumentsBounds 验证两列的列宽上界：digest ≤255、arg_keys ≤512、A 档值 ≤96。
func TestMCPSummarizeArgumentsBounds(t *testing.T) {
	long := strings.Repeat("中", 400)

	// digest：三个 A 档超长值拼起来必然超 255，应从整体截断。
	digest, _ := mcpSummarizeArguments(json.RawMessage(
		`{"path":"` + long + `","namespace":"` + long + `","serverId":"` + long + `"}`))
	if got := len([]rune(digest)); got > mcpInvocationDigestMax || !strings.HasSuffix(digest, "…") {
		t.Fatalf("target_digest 应从整体截断到 %d 字符，实际 %d 字符: %q", mcpInvocationDigestMax, got, digest)
	}

	// arg_keys：24 个长内容键把合计推过 512，应从尾部截断并保留前缀。
	obj := make(map[string]any, mcpInvocationArgKeysEntries)
	for i := 0; i < mcpInvocationArgKeysEntries; i++ {
		obj[fmt.Sprintf("contentPayloadField%02d", i)] = strings.Repeat("x", 1200)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("构造测试参数失败: %v", err)
	}
	_, argKeys := mcpSummarizeArguments(raw)
	if len(argKeys) > mcpInvocationArgKeysMax {
		t.Fatalf("arg_keys 超列宽: %d", len(argKeys))
	}
	if !strings.HasSuffix(argKeys, "…") {
		t.Fatalf("arg_keys 超长应从尾部截断并追加省略号: %q", argKeys)
	}
	if !strings.HasPrefix(argKeys, "contentPayloadField00:1200,") {
		t.Fatalf("arg_keys 截断必须保留前缀（头部项稳定可断言）: %q", argKeys)
	}

	// A 档单值截断到 96 字符 + 省略号。
	var one map[string]json.RawMessage
	if err := json.Unmarshal(json.RawMessage(`{"path":"`+long+`"}`), &one); err != nil {
		t.Fatalf("构造测试参数失败: %v", err)
	}
	value, ok := mcpScalarValue(one["path"])
	if !ok || len([]rune(value)) != mcpInvocationTextMax {
		t.Fatalf("A 档单值应截断到 %d 字符（含省略号），实际 %d 字符: %q",
			mcpInvocationTextMax, len([]rune(value)), value)
	}
	if !strings.HasSuffix(value, "…") {
		t.Fatalf("超长 A 档值应追加省略号: %q", value)
	}
	// 多字节字符不得被切碎（截断按 rune 计）。
	if !utf8.ValidString(value) {
		t.Fatalf("截断后不是合法 UTF-8: %q", value)
	}
}

// TestMCPArgumentKeysEntryLimit 验证 arg_keys 最多 24 项。
func TestMCPArgumentKeysEntryLimit(t *testing.T) {
	obj := make(map[string]any, 40)
	for i := 0; i < 40; i++ {
		obj[fmt.Sprintf("field%02d", i)] = i
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	_, argKeys := mcpSummarizeArguments(raw)
	if got := len(strings.Split(argKeys, ",")); got != mcpInvocationArgKeysEntries {
		t.Fatalf("arg_keys 项数=%d，期望上限 %d", got, mcpInvocationArgKeysEntries)
	}
}

// TestMCPInvocationDurationMeasured 验证 duration_ms 覆盖 handler 真实耗时（不含响应序列化 / 网络）。
func TestMCPInvocationDurationMeasured(t *testing.T) {
	const tool = "beacon.audit.events.list"
	sink := &mcpInvocationSink{}
	server := mcpAuditTestServer(t, sink, &mcp.Tool{Name: tool},
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			time.Sleep(20 * time.Millisecond)
			return &mcp.CallToolResult{}, map[string]any{}, nil
		})

	mustCallMCPTool(t, server, tool, map[string]any{})
	row := sink.only(t)
	if row.DurationMs < 20 {
		t.Fatalf("duration_ms=%d，未覆盖 handler 的 20ms 耗时", row.DurationMs)
	}
	if row.DurationMs > 2000 {
		t.Fatalf("duration_ms=%d 异常偏大", row.DurationMs)
	}
}

// TestMCPInvocationServiceRecorderContract 验证 service.MCPInvocationService 满足 server 侧的窄接口。
func TestMCPInvocationServiceRecorderContract(t *testing.T) {
	svc := service.NewMCPInvocationService(nil, nil)
	var rec MCPInvocationRecorder = svc
	// 通道未装配（Writer 为 nil）时仍不得 panic，只计一次丢弃（§5 第 9 条）。
	rec.Record(model.MCPInvocation{ToolName: "beacon.audit.events.list"})
	if got := svc.Dropped(); got != 1 {
		t.Fatalf("未装配通道时应计一次丢弃，实际 %d", got)
	}
}

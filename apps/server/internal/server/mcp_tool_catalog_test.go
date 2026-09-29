package server

import (
	"context"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newFullMCPToolRegistry 构造填满全部可选服务的 registry，使 NewMCPServer
// 走完整注册路径（不因 nil 守卫提前返回）。
//
// 各 service 仅作零值引用：注册期只生成闭包、不调用其方法，故零值安全。
func newFullMCPToolRegistry() *MCPToolRegistry {
	return &MCPToolRegistry{
		approvals: &service.ApprovalService{},
		apiKeys:   &service.APIKeyService{},
		v2:        &service.V2ControlPlaneService{},
		commands:  &service.AgentCommandService{},
		settings:  &service.SettingsService{},
		updates:   &service.UpdateService{},
		delivery:  &service.DeliveryOrchestrator{},
		orders:    &service.DeliveryOrderService{},
		configs:   &service.ConfigService{},
		files:     &service.FileService{},
		overrides: &service.OverrideSetService{},
		assets:    &service.AssetPreviewService{},
		messages:  &service.MessagePayloadService{},
		reads: MCPReadServices{
			v2:          &service.V2ControlPlaneService{},
			topology:    &service.TopologyService{},
			health:      &service.HealthQueryService{},
			messages:    &service.MessageQueryService{},
			connections: &service.ConnQueryService{},
			commands:    &service.CommandObserveService{},
			scheduling:  &service.SchedDecisionQueryService{},
			audits:      &service.AuditService{},
		},
	}
}

// listRegisteredTools 经 in-memory transport 走真实注册路径，枚举已注册的工具名。
//
// 必须用 Tools 迭代器而非单次 ListTools：服务端 PageSize 为 100，
// automation 目录已超 100 条，单次调用只会拿到首页。
func listRegisteredTools(t *testing.T, registry *MCPToolRegistry, principal auth.Principal) []string {
	t.Helper()
	ctx := context.Background()
	server := registry.NewMCPServer(principal)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("服务端连接失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "catalog-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	defer func() { _ = session.Close() }()

	names := make([]string, 0, len(mcpToolCatalog))
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("枚举工具失败: %v", err)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// sortedStrings 返回排序副本，供集合比对。
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// equalStringSets 比对两个字符串切片是否构成同一集合。
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sortedStrings(a), sortedStrings(b)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// TestMCPToolCatalogIntegrity 验证风险分级目录自洽（FR-236）：
// 工具名非空且唯一、风险等级取值合法。
func TestMCPToolCatalogIntegrity(t *testing.T) {
	validRisk := map[string]bool{MCPRiskLow: true, MCPRiskHigh: true, MCPRiskCritical: true}
	seen := make(map[string]bool, len(mcpToolCatalog))
	for _, spec := range mcpToolCatalog {
		if spec.Name == "" {
			t.Fatalf("目录存在空工具名: %+v", spec)
		}
		if seen[spec.Name] {
			t.Fatalf("目录存在重复登记的工具: %s", spec.Name)
		}
		seen[spec.Name] = true
		if !validRisk[spec.RiskLevel] {
			t.Fatalf("工具 %s 的风险等级非法: %q", spec.Name, spec.RiskLevel)
		}
	}
	if len(seen) != len(mcpToolCatalog) {
		t.Fatalf("目录键数 %d 与条目数 %d 不一致", len(seen), len(mcpToolCatalog))
	}
}

// TestMCPToolCatalogMatchesRuntimeRegistration 是 FR-236 的覆盖门禁：
// 以真实注册路径枚举工具，断言与目录派生的清单**双向一致**。
//
// 这是「未登记即 fail-closed」的守护——新增工具若未进目录，本测试失败。
func TestMCPToolCatalogMatchesRuntimeRegistration(t *testing.T) {
	registry := newFullMCPToolRegistry()
	for _, profile := range []string{model.MCPClientProfileObserver, model.MCPClientProfileAutomation} {
		t.Run(profile, func(t *testing.T) {
			principal := auth.MCPPrincipal("catalog-test", "目录一致性测试", profile)
			registered := listRegisteredTools(t, registry, principal)
			declared := sortedStrings(MCPToolNames(profile))
			if !equalStringSets(registered, declared) {
				t.Fatalf("目录声明与真实注册不一致\n声明 %d 项: %v\n注册 %d 项: %v",
					len(declared), declared, len(registered), registered)
			}
		})
	}
}

// criticalToolNames 返回当前开关状态下可发现的 critical 工具名。
func criticalToolNames(approvalDecide bool) []string {
	names := make([]string, 0, 4)
	for _, spec := range mcpToolCatalog {
		if spec.RiskLevel != MCPRiskCritical {
			continue
		}
		if spec.RequireApprovalDecide && !approvalDecide {
			continue
		}
		names = append(names, spec.Name)
	}
	return names
}

// TestMCPProductionModeHidesCriticalTools 是 FR-237 的门禁：
// 生产模式开启时被隐藏的工具集合，精确等于目录中当前可见的 critical 集合。
//
// 必须在同一 allow-approval-decide 状态下取 A/B 两条基线——approve / reject
// 同为 critical 且 RequireApprovalDecide，开关关闭时它们本就不可见，
// 直接套用「A−B == 全部 critical」会误报。
func TestMCPProductionModeHidesCriticalTools(t *testing.T) {
	t.Cleanup(func() {
		auth.SetMCPProductionMode(false)
		auth.SetMCPApprovalDecide(false)
	})
	for _, approvalDecide := range []bool{false, true} {
		name := "approval-decide关闭"
		if approvalDecide {
			name = "approval-decide开启"
		}
		t.Run(name, func(t *testing.T) {
			auth.SetMCPApprovalDecide(approvalDecide)

			auth.SetMCPProductionMode(false)
			baseline := MCPToolNames(model.MCPClientProfileAutomation)

			auth.SetMCPProductionMode(true)
			restricted := MCPToolNames(model.MCPClientProfileAutomation)

			hidden := make([]string, 0, 4)
			stillVisible := make(map[string]bool, len(restricted))
			for _, n := range restricted {
				stillVisible[n] = true
			}
			for _, n := range baseline {
				if !stillVisible[n] {
					hidden = append(hidden, n)
				}
			}

			want := criticalToolNames(approvalDecide)
			if !equalStringSets(hidden, want) {
				t.Fatalf("生产模式隐藏集合与 critical 目录不一致\n隐藏 %v\n期望 %v", hidden, want)
			}
			if len(want) == 0 {
				t.Fatalf("critical 集合为空，门禁失去意义")
			}
		})
	}
}

// TestMCPProductionModeKeepsObserverUnchanged 验证 observer 在任何开关状态下
// 可发现集合不变（observer 本不含 critical 工具）。
func TestMCPProductionModeKeepsObserverUnchanged(t *testing.T) {
	t.Cleanup(func() { auth.SetMCPProductionMode(false) })
	auth.SetMCPProductionMode(false)
	before := MCPToolNames(model.MCPClientProfileObserver)
	auth.SetMCPProductionMode(true)
	after := MCPToolNames(model.MCPClientProfileObserver)
	if !equalStringSets(before, after) {
		t.Fatalf("observer 集合随生产模式变化\n前 %v\n后 %v", before, after)
	}
}

// riskOrder 给出风险等级的序，用于「不低于」比对。
func riskOrder(level string) int {
	switch level {
	case MCPRiskLow:
		return 0
	case MCPRiskHigh:
		return 1
	case MCPRiskCritical:
		return 2
	default:
		return -1
	}
}

// buildApprovalRegistry 构造并填充审批注册表，用于读取既有 descriptor。
//
// 各 service 仅作零值引用：注册函数只把 adapter 与 descriptor 存入 registry，
// 不执行领域逻辑（且各自带 nil 保护，依赖不全时整组静默跳过）。
func buildApprovalRegistry() *authz.ApprovalRegistry {
	registry := authz.NewApprovalRegistry()
	service.RegisterConfigApprovalAdapters(registry, &service.ConfigService{})
	service.RegisterFileOverrideApprovalAdapters(registry, &service.FileService{}, &service.OverrideSetService{})
	service.RegisterV2ControlPlaneApprovalAdapters(registry, &service.V2ControlPlaneService{})
	service.RegisterAPIKeyApprovalAdapters(registry, &service.APIKeyService{})
	service.RegisterDeliveryApprovalAdapter(registry, &service.DeliveryOrderService{}, &service.DeliveryOrchestrator{})
	service.RegisterAgentCommandApprovalAdapters(registry, &service.AgentCommandService{}, &service.SensitiveAccessGrantService{})
	service.RegisterAgentLogApprovalAdapter(registry, &service.AgentLogService{}, &service.SensitiveAccessGrantService{})
	service.RegisterAssetPreviewApprovalAdapter(registry, &service.AssetPreviewService{}, &service.SensitiveAccessGrantService{})
	service.RegisterSensitiveConfigApprovalAdapter(registry, &service.ConfigService{}, &service.SensitiveAccessGrantService{})
	service.RegisterReverseFetchTaskApprovalAdapters(registry, &service.ReverseFetchTaskService{})
	service.RegisterMCPOAuthApprovalAdapters(registry, &service.MCPOAuthService{})
	service.RegisterSystemOperationApprovalAdapters(registry, &service.UpdateService{}, &service.SettingsService{}, &gorm.DB{})
	return registry
}

// TestMCPToolCatalogRiskNotBelowDescriptor 是 FR-236 的同步不变量：
// 每个登记了 operation kind 的工具，其在 MCP 面的风险等级**不得低于**
// 既有 OperationDescriptor 的等级。
//
// 该不变量保证 MCP 面只会比人类管理台更严，绝不更松——catalog 中若有
// 工具被误降级，本测试失败。
func TestMCPToolCatalogRiskNotBelowDescriptor(t *testing.T) {
	registry := buildApprovalRegistry()
	checked := 0
	for _, spec := range mcpToolCatalog {
		if spec.OperationKind == "" {
			continue
		}
		descriptor, ok := registry.Descriptor(spec.OperationKind)
		if !ok {
			// 该 kind 未被上述注册函数覆盖（依赖不全时整组跳过），交由
			// 覆盖度下限断言兜底，此处不阻塞。
			continue
		}
		checked++
		if riskOrder(spec.RiskLevel) < riskOrder(descriptor.RiskLevel) {
			t.Fatalf("工具 %s 的 MCP 风险等级 %s 低于其 operation %s 的 descriptor 等级 %s",
				spec.Name, spec.RiskLevel, spec.OperationKind, descriptor.RiskLevel)
		}
	}
	// 覆盖度下限：防止注册函数整组静默跳过导致断言空转。
	const minChecked = 40
	if checked < minChecked {
		t.Fatalf("仅核对了 %d 个工具的 descriptor 等级，低于下限 %d——注册装配可能已失效", checked, minChecked)
	}
}

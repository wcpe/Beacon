package server

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// 拓扑建树工具（FR-221）。
//
// 与 registerTopologyApproval 刻意区分：建树是**低风险结构操作**——新建小区不会让任何玩家改道；
// 删除由 service 层既有约束保护（非空节点返回 ErrZoneHasServers / ErrBCClusterNotEmpty 等），
// 不会误删带服节点。按 FR-220「低风险按能力直执」原则，本组工具直接调用 service 并写审计，
// **不产生审批票据**。
//
// 对照：servers.assign / rezone / transfer-placement / set-default-entry 属高风险
// （影响玩家入口或调度归属），仍走 approval ticket。
//
// 删除的确认机制：service 层已按「非空拒绝」保护，故 MCP 侧不再叠加 confirmCode
// ——后者需要按 id 读取目标当前 code 的能力，而 registry 无此依赖，强行实现会引入
// 与 HTTP 侧不一致的二次真源。

type mcpTopologyCreateInput struct {
	// 观察范围（与只读拓扑工具同口径）：namespaceId 为字符串形式，可带 envId 限定。
	mcpScopeInput
	// region 用：所属 BC 集群 id；zone 用：所属大区 id
	ParentID    uint   `json:"parentId,omitempty"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

type mcpTopologyUpdateInput struct {
	ID          uint    `json:"id"`
	Name        *string `json:"name,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
}

type mcpTopologyDeleteInput struct {
	ID uint `json:"id"`
}

// registerTopologyAuthoring 登记九个建树工具；仅 automation profile 可见（写操作）。
func (r *MCPToolRegistry) registerTopologyAuthoring(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	op := principal.AuditRef()

	// ── BC 集群 ──
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.bc-clusters.create", Description: "新建 BC 集群（低风险结构操作，直接执行；须 namespaceId 或由观察范围唯一确定）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResultWithReason("观察范围无效")
		}
		nsID, err := r.mcpResolveNamespaceID("", in.NamespaceID, scope)
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		cluster, err := r.v2.CreateBCCluster(service.CreateBCClusterParams{
			NamespaceID: nsID, Name: in.Name, Code: in.Code,
			DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": cluster.ID, "name": cluster.Name, "code": cluster.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.bc-clusters.update", Description: "改 BC 集群名或描述（code 不可改）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyUpdateInput) (*mcp.CallToolResult, map[string]any, error) {
		cluster, err := r.v2.UpdateBCCluster(service.UpdateDisplayResourceParams{
			ID: in.ID, Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": cluster.ID, "name": cluster.Name, "code": cluster.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.bc-clusters.delete", Description: "删除 BC 集群（含大区或已分配代理时拒绝）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		if err := r.v2.DeleteBCCluster(service.DeleteNodeParams{ID: in.ID, Operator: op, ClientIP: "mcp"}); err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"deleted": true, "id": in.ID}, nil
	})

	// ── 大区 ──
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.regions.create", Description: "新建大区（须 parentId = 所属 BC 集群 id）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		region, err := r.v2.CreateRegion(service.CreateRegionParams{
			BCClusterID: in.ParentID, Name: in.Name, Code: in.Code,
			DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": region.ID, "name": region.Name, "code": region.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.regions.update", Description: "改大区名或描述（code 不可改）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyUpdateInput) (*mcp.CallToolResult, map[string]any, error) {
		region, err := r.v2.UpdateRegion(service.UpdateDisplayResourceParams{
			ID: in.ID, Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": region.ID, "name": region.Name, "code": region.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.regions.delete", Description: "删除大区（含小区时拒绝）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		if err := r.v2.DeleteRegion(service.DeleteNodeParams{ID: in.ID, Operator: op, ClientIP: "mcp"}); err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"deleted": true, "id": in.ID}, nil
	})

	// ── 小区 ──
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.zones.create", Description: "新建小区（须 parentId = 所属大区 id）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		zone, err := r.v2.CreateZone(service.CreateZoneParams{
			RegionID: in.ParentID, Name: in.Name, Code: in.Code,
			DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": zone.ID, "name": zone.Name, "code": zone.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.zones.update", Description: "改小区名或描述（code 不可改）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyUpdateInput) (*mcp.CallToolResult, map[string]any, error) {
		zone, err := r.v2.UpdateZone(service.UpdateDisplayResourceParams{
			ID: in.ID, Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			Operator: op, ClientIP: "mcp",
		})
		if err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"id": zone.ID, "name": zone.Name, "code": zone.Code}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.zones.delete", Description: "删除小区（含服务器时拒绝）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		if err := r.v2.DeleteZone(service.DeleteNodeParams{ID: in.ID, Operator: op, ClientIP: "mcp"}); err != nil {
			return mcpRejectedResultWithReason(mcpTopologyErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"deleted": true, "id": in.ID}, nil
	})
}

// mcpTopologyErrReason 把领域错误转成可读原因；不透传凭据或内部路径。
func mcpTopologyErrReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "操作未完成"
	}
	return msg
}

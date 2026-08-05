package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// MCPToolRegistry 是显式领域工具的唯一登记点；不存在 HTTP、SQL、文件或内部服务通用代理。
type MCPToolRegistry struct {
	approvals *service.ApprovalService
	apiKeys   *service.APIKeyService
	v2        *service.V2ControlPlaneService
	commands  *service.AgentCommandService
	settings  *service.SettingsService
	updates   *service.UpdateService
	delivery  *service.DeliveryOrchestrator
	orders    *service.DeliveryOrderService
	configs   *service.ConfigService
	files     *service.FileService
	overrides *service.OverrideSetService
	assets    *service.AssetPreviewService
	messages  *service.MessagePayloadService
	reads     mcpReadServices
}

// NewMCPToolRegistry 构造 MCP 显式工具目录。
func NewMCPToolRegistry(approvals *service.ApprovalService, apiKeys *service.APIKeyService, v2 *service.V2ControlPlaneService, settings *service.SettingsService) *MCPToolRegistry {
	return &MCPToolRegistry{approvals: approvals, apiKeys: apiKeys, v2: v2, settings: settings}
}

// SetAgentCommandService 在命令服务完成装配后接入其显式审批工具。
func (r *MCPToolRegistry) SetAgentCommandService(commands *service.AgentCommandService) {
	r.commands = commands
}

// SetUpdateService 在在线更新服务完成装配后接入其显式审批工具。
func (r *MCPToolRegistry) SetUpdateService(updates *service.UpdateService) { r.updates = updates }

// SetDeliveryOrchestrator 在交付编排器完成装配后接入其显式审批工具。
func (r *MCPToolRegistry) SetDeliveryOrchestrator(delivery *service.DeliveryOrchestrator) {
	r.delivery = delivery
}

// SetDeliveryOrderService 接入变更单提交与草稿删除的显式审批工具。
func (r *MCPToolRegistry) SetDeliveryOrderService(orders *service.DeliveryOrderService) {
	r.orders = orders
}

func (r *MCPToolRegistry) SetConfigService(configs *service.ConfigService) { r.configs = configs }

// SetFileOverrideServices 在文件服务完成装配后接入其显式审批工具。
func (r *MCPToolRegistry) SetFileOverrideServices(files *service.FileService, overrides *service.OverrideSetService) {
	r.files = files
	r.overrides = overrides
}

// SetSensitiveReadServices 接入只创建申请或消费授权的敏感读取工具；工具不会回传正文。
func (r *MCPToolRegistry) SetSensitiveReadServices(assets *service.AssetPreviewService, messages *service.MessagePayloadService) {
	r.assets = assets
	r.messages = messages
}

// SetReadServices 接入 MCP 可公开的脱敏只读查询服务；不接 repository 或运行时内部对象。
func (r *MCPToolRegistry) SetReadServices(reads mcpReadServices) { r.reads = reads }

// MCPToolNames 返回 profile 可发现的固定工具名，供覆盖门禁验证。
func MCPToolNames(profile string) []string {
	names := append([]string{"beacon.approvals.own.list", "beacon.approvals.own.get"}, mcpReadToolNames...)
	if profile == model.MCPClientProfileAutomation {
		return append(names, "beacon.approvals.own.withdraw", "beacon.config.publish", "beacon.config.rollback", "beacon.config.gray.publish", "beacon.config.gray.promote", "beacon.config.delete", "beacon.config.batch.delete", "beacon.config.batch.enable", "beacon.config.batch.disable", "beacon.files.create", "beacon.files.import", "beacon.files.publish", "beacon.files.rollback", "beacon.files.delete", "beacon.files.batch.delete", "beacon.files.batch.enable", "beacon.files.batch.disable", "beacon.assets.preview.request", "beacon.assets.preview.consume", "beacon.messages.payload.request", "beacon.messages.payload.consume", "beacon.override-sets.publish", "beacon.override-sets.rollback", "beacon.override-sets.delete", "beacon.credentials.api-key.create", "beacon.credentials.api-key.rotate", "beacon.identity.agent.unbind", "beacon.identity.agent.enable", "beacon.identity.agent.allow-reapply", "beacon.identity.agent.approve", "beacon.identity.agent.resolve-conflict", "beacon.trust.namespace.grant", "beacon.topology.servers.assign", "beacon.topology.servers.rezone", "beacon.topology.server.transfer-placement", "beacon.topology.server.disable-draining", "beacon.topology.server.set-default-entry", "beacon.lifecycle.namespace.archive", "beacon.lifecycle.namespace.restore", "beacon.lifecycle.namespace.permanent-delete", "beacon.lifecycle.server.archive", "beacon.lifecycle.server.restore", "beacon.lifecycle.server.permanent-delete", "beacon.agent.server.resync", "beacon.system.update.apply", "beacon.system.update.rollback", "beacon.system.settings.update-dangerous", "beacon.delivery.order.submit", "beacon.delivery.order.delete", "beacon.delivery.order.resume", "beacon.delivery.order.rollback", "beacon.delivery.batch.confirm", "beacon.delivery.rollback.finish")
	}
	if profile == model.MCPClientProfileObserver {
		return names
	}
	return nil
}

// NewMCPServer 仅登记当前主体被允许发现的固定工具。
func (r *MCPToolRegistry) NewMCPServer(principal auth.Principal) *mcp.Server {
	server := newEmptyMCPServer()
	if r == nil || r.approvals == nil || !principal.HasCapability(auth.CapabilityApprovalRead) {
		return server
	}
	r.registerOwnApprovalRead(server, principal)
	r.registerReadTools(server)
	if principal.HasCapability(auth.CapabilityApprovalWithdrawOwn) {
		r.registerOwnApprovalWithdraw(server, principal)
		r.registerConfigApproval(server, principal)
		r.registerFileOverrideApproval(server, principal)
		r.registerSensitiveReadApproval(server, principal)
		r.registerAPIKeyApproval(server, principal)
		r.registerIdentityApproval(server, principal)
		r.registerNamespaceTrustApproval(server, principal)
		r.registerTopologyApproval(server, principal)
		r.registerLifecycleApproval(server, principal)
		r.registerAgentCommandApproval(server, principal)
		r.registerSystemApproval(server, principal)
		r.registerDeliveryApproval(server, principal)
	}
	return server
}

type mcpOwnApprovalListInput struct {
	Status    string `json:"status,omitempty"`
	Operation string `json:"operation,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

type mcpOwnApprovalGetInput struct {
	RequestID string `json:"requestId"`
}

type mcpAPIKeyCreateInput struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpAPIKeyRotateInput struct {
	ID             uint   `json:"id"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpIdentityTransitionInput struct {
	IdentityID     string `json:"identityId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpIdentityApproveInput struct {
	IdentityID     string `json:"identityId"`
	ServerID       string `json:"serverId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpIdentityConflictInput struct {
	IdentityID     string `json:"identityId"`
	KeepBootID     string `json:"keepBootId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpNamespaceTrustInput struct {
	FromNamespaceID uint   `json:"fromNamespaceId"`
	ToNamespaceID   uint   `json:"toNamespaceId"`
	Capability      string `json:"capability"`
	Note            string `json:"note"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

type mcpAssignServersInput struct {
	ServerRowIDs   []uint `json:"serverRowIds"`
	TargetKind     string `json:"targetKind"`
	TargetID       uint   `json:"targetId"`
	IsDefaultEntry bool   `json:"isDefaultEntry"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpRezoneServersInput struct {
	ServerRowIDs   []uint `json:"serverRowIds"`
	TargetKind     string `json:"targetKind"`
	TargetID       uint   `json:"targetId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpPlacementTransferInput struct {
	ServerID       string `json:"serverId"`
	TargetKind     string `json:"targetKind"`
	TargetID       uint   `json:"targetId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpDisableDrainingInput struct {
	ServerID       string `json:"serverId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpDefaultEntryInput struct {
	ServerID       string `json:"serverId"`
	Value          bool   `json:"value"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpAgentResyncInput struct {
	NamespaceCode  string `json:"namespaceCode"`
	ServerID       string `json:"serverId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpSystemRequestInput struct {
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpDangerousSettingInput struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpDeliveryResumeInput struct {
	OrderID        uint   `json:"orderId"`
	Mode           string `json:"mode"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type mcpDeliverySubmitInput struct {
	OrderID        uint   `json:"orderId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type mcpDeliveryRollbackInput struct {
	OrderID        uint   `json:"orderId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type mcpDeliveryBatchInput struct {
	OrderID        uint   `json:"orderId"`
	BatchNo        int    `json:"batchNo"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type mcpConfigInput struct {
	ID             uint     `json:"id"`
	Content        string   `json:"content"`
	Cohort         []string `json:"cohort"`
	Version        int64    `json:"version"`
	Reason         string   `json:"reason"`
	Comment        string   `json:"comment"`
	IdempotencyKey string   `json:"idempotencyKey"`
}

type mcpBatchIDsInput struct {
	IDs            []uint `json:"ids"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpFileCreateInput struct {
	Namespace         string `json:"namespace"`
	Group             string `json:"group"`
	Path              string `json:"path"`
	ScopeLevel        string `json:"scopeLevel"`
	ScopeTarget       string `json:"scopeTarget"`
	Content           string `json:"content"`
	Comment           string `json:"comment"`
	WholeFileOverride bool   `json:"wholeFileOverride"`
	SensitiveExcluded bool   `json:"sensitiveExcluded"`
	Reason            string `json:"reason"`
	IdempotencyKey    string `json:"idempotencyKey"`
}

type mcpFileImportInput struct {
	Namespace      string               `json:"namespace"`
	Group          string               `json:"group"`
	ScopeLevel     string               `json:"scopeLevel"`
	ScopeTarget    string               `json:"scopeTarget"`
	Files          []service.ImportFile `json:"files"`
	Comment        string               `json:"comment"`
	Reason         string               `json:"reason"`
	IdempotencyKey string               `json:"idempotencyKey"`
}

type mcpAssetPreviewRequestInput struct {
	ServerID       string `json:"serverId"`
	Path           string `json:"path"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpAssetPreviewConsumeInput struct {
	GrantID   string `json:"grantId"`
	CommandID uint   `json:"commandId"`
}

type mcpMessagePayloadRequestInput struct {
	MessageID      string `json:"messageId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpMessagePayloadConsumeInput struct {
	GrantID   string `json:"grantId"`
	MessageID string `json:"messageId"`
}

type mcpFilePublishInput struct {
	ID             uint   `json:"id"`
	Content        string `json:"content"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpFileRollbackInput struct {
	ID             uint   `json:"id"`
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpFileDeleteInput struct {
	ID             uint   `json:"id"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpOverridePublishInput struct {
	ID             uint   `json:"id"`
	TargetRoot     string `json:"targetRoot"`
	ReloadCommand  string `json:"reloadCommand"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpOverrideRollbackInput struct {
	ID             uint   `json:"id"`
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpOverrideDeleteInput struct {
	ID             uint   `json:"id"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type mcpLifecycleInput struct {
	NamespaceID          uint   `json:"namespaceId"`
	ServerRowID          uint   `json:"serverRowId"`
	Reason               string `json:"reason"`
	ConfirmationCode     string `json:"confirmationCode"`
	ConfirmationServerID string `json:"confirmationServerId"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

type lifecycleRequester func(uint, service.NamespaceLifecycleParams, auth.Principal, string) (service.ApprovalTicketView, error)

func (r *MCPToolRegistry) registerLifecycleApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	registerLifecycleTool(server, "beacon.lifecycle.namespace.archive", "提交命名空间归档审批申请", principal, r.v2.RequestArchiveNamespace, func(in mcpLifecycleInput) uint { return in.NamespaceID }, "")
	registerLifecycleTool(server, "beacon.lifecycle.namespace.restore", "提交命名空间恢复审批申请", principal, r.v2.RequestRestoreNamespace, func(in mcpLifecycleInput) uint { return in.NamespaceID }, "")
	registerLifecycleTool(server, "beacon.lifecycle.namespace.permanent-delete", "提交命名空间永久删除审批申请", principal, r.v2.RequestPermanentDeleteNamespace, func(in mcpLifecycleInput) uint { return in.NamespaceID }, "namespace")
	registerLifecycleTool(server, "beacon.lifecycle.server.archive", "提交服务器归档审批申请", principal, r.v2.RequestArchiveServer, func(in mcpLifecycleInput) uint { return in.ServerRowID }, "")
	registerLifecycleTool(server, "beacon.lifecycle.server.restore", "提交服务器恢复审批申请", principal, r.v2.RequestRestoreServer, func(in mcpLifecycleInput) uint { return in.ServerRowID }, "")
	registerLifecycleTool(server, "beacon.lifecycle.server.permanent-delete", "提交服务器永久删除审批申请", principal, r.v2.RequestPermanentDeleteServer, func(in mcpLifecycleInput) uint { return in.ServerRowID }, "server")
}

func registerLifecycleTool(server *mcp.Server, name, description string, principal auth.Principal, request lifecycleRequester, resourceID func(mcpLifecycleInput) uint, confirmationKind string) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpLifecycleInput) (*mcp.CallToolResult, map[string]any, error) {
		confirmation := ""
		if confirmationKind == "namespace" {
			confirmation = in.ConfirmationCode
		}
		if confirmationKind == "server" {
			confirmation = in.ConfirmationServerID
		}
		ticket, err := request(resourceID(in), service.NamespaceLifecycleParams{
			Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp", Confirmation: confirmation,
		}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status, "operationKey": ticket.OperationKey}, nil
	})
}

func (r *MCPToolRegistry) registerConfigApproval(server *mcp.Server, principal auth.Principal) {
	if r.configs == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.config.publish", Description: "提交配置发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestPublish(in.ID, in.Content, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.config.rollback", Description: "提交配置回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.config.gray.publish", Description: "提交配置灰度发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestGrayPublish(in.ID, in.Content, in.Cohort, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.config.gray.promote", Description: "提交配置灰度晋升审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestGrayPromote(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.config.delete", Description: "提交配置删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpConfigApprovalTicketView(ticket), nil
	})
	registerConfigBatchTool(server, "beacon.config.batch.delete", principal, r.configs.RequestBatchDelete)
	registerConfigBatchSetEnabledTool(server, "beacon.config.batch.enable", principal, true, r.configs.RequestBatchSetEnabled)
	registerConfigBatchSetEnabledTool(server, "beacon.config.batch.disable", principal, false, r.configs.RequestBatchSetEnabled)
}

type configBatchRequester func([]uint, string, string, string, string, auth.Principal) (service.ConfigApprovalTicket, error)
type configBatchSetEnabledRequester func([]uint, bool, string, string, string, string, auth.Principal) (service.ConfigApprovalTicket, error)

func registerConfigBatchTool(server *mcp.Server, name string, principal auth.Principal, request configBatchRequester) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: "提交配置批量删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpConfigApprovalTicketView(ticket), nil
	})
}

func registerConfigBatchSetEnabledTool(server *mcp.Server, name string, principal auth.Principal, enabled bool, request configBatchSetEnabledRequester) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: "提交配置批量启停审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, enabled, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpConfigApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerFileOverrideApproval(server *mcp.Server, principal auth.Principal) {
	if r.files != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.files.create", Description: "提交文件创建审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileCreateInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.files.RequestCreate(service.CreateFileParams{Namespace: in.Namespace, Group: in.Group, Path: in.Path, ScopeLevel: in.ScopeLevel, ScopeTarget: in.ScopeTarget, Content: in.Content, Operator: principal.AuditRef(), Comment: in.Comment, WholeFileOverride: in.WholeFileOverride, SensitiveExcluded: in.SensitiveExcluded, ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.files.import", Description: "提交文件批量导入审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileImportInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.files.RequestImport(service.ImportFilesParams{Namespace: in.Namespace, Group: in.Group, ScopeLevel: in.ScopeLevel, ScopeTarget: in.ScopeTarget, Files: in.Files, Operator: principal.AuditRef(), Comment: in.Comment, ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.files.publish", Description: "提交文件发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFilePublishInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.files.RequestPublish(in.ID, in.Content, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.files.rollback", Description: "提交文件回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.files.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.files.delete", Description: "提交文件删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.files.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
		})
		registerFileBatchTool(server, "beacon.files.batch.delete", principal, r.files.RequestBatchDelete)
		registerFileBatchSetEnabledTool(server, "beacon.files.batch.enable", principal, true, r.files.RequestBatchSetEnabled)
		registerFileBatchSetEnabledTool(server, "beacon.files.batch.disable", principal, false, r.files.RequestBatchSetEnabled)
	}
	if r.overrides == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.override-sets.publish", Description: "提交覆盖集发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverridePublishInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestPublish(in.ID, service.PublishOverrideSetParams{TargetRoot: in.TargetRoot, ReloadCommand: in.ReloadCommand, Comment: in.Comment, Operator: principal.AuditRef(), ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.override-sets.rollback", Description: "提交覆盖集回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverrideRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.override-sets.delete", Description: "提交覆盖集删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverrideDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

type fileBatchRequester func([]uint, string, string, string, string, auth.Principal) (service.FileApprovalTicket, error)
type fileBatchSetEnabledRequester func([]uint, bool, string, string, string, string, auth.Principal) (service.FileApprovalTicket, error)

func registerFileBatchTool(server *mcp.Server, name string, principal auth.Principal, request fileBatchRequester) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: "提交文件批量删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

func registerFileBatchSetEnabledTool(server *mcp.Server, name string, principal auth.Principal, enabled bool, request fileBatchSetEnabledRequester) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: "提交文件批量启停审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, enabled, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

// registerSensitiveReadApproval 仅转发既有申请与一次性消费校验，不把敏感正文放进 MCP 响应。
func (r *MCPToolRegistry) registerSensitiveReadApproval(server *mcp.Server, principal auth.Principal) {
	if r.assets != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.assets.preview.request", Description: "提交敏感文件预览审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssetPreviewRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			request, err := r.assets.RequestAccess(in.ServerID, in.Path, in.Reason, in.IdempotencyKey, principal, "mcp")
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": request.RequestID, "status": request.Status}, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.assets.preview.consume", Description: "消费已批准的敏感文件预览授权，不返回文件正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssetPreviewConsumeInput) (*mcp.CallToolResult, map[string]any, error) {
			if _, err := r.assets.ConsumeApproved(in.GrantID, in.CommandID, principal); err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, map[string]any{"status": "consumed"}, nil
		})
	}
	if r.messages != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.messages.payload.request", Description: "提交消息正文读取审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessagePayloadRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			request, err := r.messages.RequestAccess(in.MessageID, in.Reason, in.IdempotencyKey, principal, "mcp")
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": request.RequestID, "status": request.Status}, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.messages.payload.consume", Description: "消费已批准的消息正文授权，不返回消息正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessagePayloadConsumeInput) (*mcp.CallToolResult, map[string]any, error) {
			if _, err := r.messages.Consume(in.GrantID, in.MessageID, principal); err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, map[string]any{"status": "consumed"}, nil
		})
	}
}

func (r *MCPToolRegistry) registerOwnApprovalRead(server *mcp.Server, principal auth.Principal) {
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.approvals.own.list", Description: "查询当前 MCP 客户端自己的审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalListInput) (*mcp.CallToolResult, map[string]any, error) {
		items, total, err := r.approvals.ListPage(service.ApprovalListFilter{
			Status: in.Status, OperationKey: in.Operation, Page: normalizedMCPPage(in.Page), PageSize: normalizedMCPPageSize(in.PageSize),
		}, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		views := make([]map[string]any, 0, len(items))
		for i := range items {
			views = append(views, mcpApprovalView(&items[i]))
		}
		return &mcp.CallToolResult{}, map[string]any{"items": views, "total": total}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.approvals.own.get", Description: "查询当前 MCP 客户端自己的单个审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalGetInput) (*mcp.CallToolResult, map[string]any, error) {
		item, err := r.approvals.Detail(in.RequestID, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalView(&item), nil
	})
}

func (r *MCPToolRegistry) registerOwnApprovalWithdraw(server *mcp.Server, principal auth.Principal) {
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.approvals.own.withdraw", Description: "撤回当前 MCP 客户端自己仍处于待处理状态的审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalGetInput) (*mcp.CallToolResult, map[string]any, error) {
		item, err := r.approvals.Withdraw(in.RequestID, principal, "mcp")
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalView(&item), nil
	})
}

func (r *MCPToolRegistry) registerAPIKeyApproval(server *mcp.Server, principal auth.Principal) {
	if r.apiKeys == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.credentials.api-key.create", Description: "提交 API 密钥创建审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAPIKeyCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.apiKeys.RequestCreate(in.Name, in.Role, nil, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.credentials.api-key.rotate", Description: "提交 API 密钥轮换审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAPIKeyRotateInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.apiKeys.RequestReset(in.ID, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerIdentityApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	registerIdentityTransitionTool(server, "beacon.identity.agent.unbind", principal, r.v2.RequestUnbindAgentIdentity)
	registerIdentityTransitionTool(server, "beacon.identity.agent.enable", principal, r.v2.RequestEnableAgentIdentity)
	registerIdentityTransitionTool(server, "beacon.identity.agent.allow-reapply", principal, r.v2.RequestAllowAgentIdentityReapply)
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.identity.agent.approve", Description: "提交身份确认审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityApproveInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestApproveAgentIdentity(in.IdentityID, service.ApproveAgentIdentityParams{ServerID: in.ServerID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.identity.agent.resolve-conflict", Description: "提交身份冲突处置审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityConflictInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestResolveAgentIdentityConflict(in.IdentityID, service.ResolveConflictParams{KeepBootID: in.KeepBootID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

type identityApprovalRequester func(string, service.IdentityTransitionParams, auth.Principal, string) (service.ApprovalTicketView, error)

func registerIdentityTransitionTool(server *mcp.Server, name string, principal auth.Principal, request identityApprovalRequester) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: "提交身份生命周期审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityTransitionInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IdentityID, service.IdentityTransitionParams{Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerNamespaceTrustApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.trust.namespace.grant", Description: "提交 namespace 信任授予审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpNamespaceTrustInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestGrantNamespaceTrust(service.GrantNamespaceTrustParams{FromNamespaceID: in.FromNamespaceID, ToNamespaceID: in.ToNamespaceID, Capability: in.Capability, Note: in.Note, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerTopologyApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.servers.assign", Description: "提交服务器分配审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssignServersInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestAssignServers(service.AssignServersParams{ServerIDs: in.ServerRowIDs, TargetKind: in.TargetKind, TargetID: in.TargetID, IsDefaultEntry: in.IsDefaultEntry, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.servers.rezone", Description: "提交服务器换区审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpRezoneServersInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestRezoneServers(service.RezoneServersParams{ServerIDs: in.ServerRowIDs, TargetKind: in.TargetKind, TargetID: in.TargetID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.server.transfer-placement", Description: "提交服务器大厅归属迁移审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpPlacementTransferInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestTransferServerPlacement(service.ServerPlacementTransferParams{ServerID: in.ServerID, TargetKind: in.TargetKind, TargetID: in.TargetID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.server.disable-draining", Description: "提交服务器取消排空审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDisableDrainingInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestDisableServerDraining(service.SetServerDrainingParams{ServerID: in.ServerID, Draining: false, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.server.set-default-entry", Description: "提交服务器默认入口变更审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDefaultEntryInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestSetServerDefaultEntryByServerID(in.ServerID, in.Value, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerAgentCommandApproval(server *mcp.Server, principal auth.Principal) {
	if r.commands == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.agent.server.resync", Description: "提交在线实例强制重同步审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAgentResyncInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.commands.RequestResyncApproval(in.NamespaceCode, in.ServerID, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerSystemApproval(server *mcp.Server, principal auth.Principal) {
	if r.updates != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.system.update.apply", Description: "提交控制面更新审批申请"}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSystemRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.updates.RequestApply(ctx, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.system.update.rollback", Description: "提交控制面回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSystemRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.updates.RequestRollback(in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
		})
	}
	if r.settings == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.system.settings.update-dangerous", Description: "提交高影响设置变更审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDangerousSettingInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.settings.RequestUpdate(in.Key, in.Value, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerDeliveryApproval(server *mcp.Server, principal auth.Principal) {
	if r.delivery == nil && r.orders == nil {
		return
	}
	if r.orders != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.order.submit", Description: "提交变更单统一审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliverySubmitInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.orders.RequestSubmit(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.order.delete", Description: "提交变更单草稿删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliverySubmitInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.orders.RequestDelete(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
			if err != nil {
				return mcpToolError(), nil, nil
			}
			return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
		})
	}
	if r.delivery == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.order.resume", Description: "提交交付变更单继续审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryResumeInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestResume(in.OrderID, in.Mode, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.order.rollback", Description: "提交交付变更单回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestRollback(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.batch.confirm", Description: "提交交付批次确认审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryBatchInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestConfirmBatch(in.OrderID, in.BatchNo, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.delivery.rollback.finish", Description: "提交交付回滚结束审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestFinishRollback(in.OrderID, principal, in.IdempotencyKey, principal.AuditRef(), "mcp")
		if err != nil {
			return mcpToolError(), nil, nil
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
}

func normalizedMCPPage(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

func normalizedMCPPageSize(size int) int {
	if size < 1 {
		return 20
	}
	if size > 100 {
		return 100
	}
	return size
}

func mcpApprovalView(req *model.ApprovalRequest) map[string]any {
	return map[string]any{
		"approvalRequestId": req.RequestID,
		"status":            req.Status,
		"operation":         req.OperationKey,
		"resultRef":         req.ResultRef,
		"createdAt":         req.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt":         approvalTime(req.ExpiresAt),
	}
}

func mcpApprovalTicketView(ticket service.ApprovalTicketView) map[string]any {
	return map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status, "operation": ticket.OperationKey}
}

func mcpConfigApprovalTicketView(ticket service.ConfigApprovalTicket) map[string]any {
	return map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}
}

func mcpFileApprovalTicketView(ticket service.FileApprovalTicket) map[string]any {
	return map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}
}

func mcpDeliveryTicketView(ticket service.DeliveryApprovalTicketView) map[string]any {
	return map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status, "operation": ticket.OperationKey}
}

func approvalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func mcpToolError() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "请求被拒绝或目标不可用"}}, IsError: true}
}

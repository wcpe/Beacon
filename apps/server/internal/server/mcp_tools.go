package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
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
	// alerts 供告警事件处理工具（单条 / 批量）直执领域动作；只读列表走 reads.alertEvents。
	alerts *service.AlertEventService
	reads  MCPReadServices
	// invocations 是工具调用流水写入方（FR-240，spec §3.1）；为 nil 时 middleware 不挂载，
	// 既有单测与未装配路径零依赖、零行为变化。
	invocations MCPInvocationRecorder
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

// SetAlertEventService 接入告警事件处理工具（单条 / 批量直执 + 同事务写审计）。
func (r *MCPToolRegistry) SetAlertEventService(alerts *service.AlertEventService) { r.alerts = alerts }

// SetReadServices 接入 MCP 可公开的脱敏只读查询服务；不接 repository 或运行时内部对象。
func (r *MCPToolRegistry) SetReadServices(reads MCPReadServices) { r.reads = reads }

// SetInvocationRecorder 装配工具调用流水写入方（FR-240，spec §3.1）；装配期调用一次（与其它 Set* 同区）。
//
// rec 为 nil 即关闭流水——既有测试与不需要流水的部署无需改动调用点。
func (r *MCPToolRegistry) SetInvocationRecorder(rec MCPInvocationRecorder) { r.invocations = rec }

// MCPToolNames 返回 profile 可发现的固定工具名，供覆盖门禁验证。
//
// 名字与可见性一律从 mcpToolCatalog 派生，不再维护第二份字符串清单：
// 「审批决定工具按运行时开关动态纳入」与「生产模式隐藏 critical」（FR-237）
// 都由 mcpToolDiscoverable 统一裁决，且与 mcpAddTool 的实际注册行为共用
// 同一判定，因此清单声明与真实注册不可能漂移。
func MCPToolNames(profile string) []string {
	if profile != model.MCPClientProfileObserver && profile != model.MCPClientProfileAutomation {
		return nil
	}
	names := make([]string, 0, len(mcpToolCatalog))
	for _, spec := range mcpToolCatalog {
		if spec.AutomationOnly && profile != model.MCPClientProfileAutomation {
			continue
		}
		if !mcpToolDiscoverable(spec.Name) {
			continue
		}
		names = append(names, spec.Name)
	}
	return names
}

// NewMCPServer 仅登记当前主体被允许发现的固定工具。
func (r *MCPToolRegistry) NewMCPServer(principal auth.Principal) *mcp.Server {
	server := newEmptyMCPServer()
	// 工具调用流水（FR-240，spec §3.1）：唯一挂载点，构造期即挂上。
	// MCP server 是**每请求构造**的，故每个请求的实例都带 middleware、不存在跨请求共享状态；
	// 也不必逐处改 78 个 mcpAddTool 调用点——middleware 在方法层，天然覆盖全部工具与未知工具。
	if r != nil {
		mcpAttachInvocationAudit(server, r.invocations)
	}
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
		// 建树（FR-221）：低风险结构操作，直接执行不走审批票据；与 topologyApproval 同组可见。
		r.registerTopologyAuthoring(server, principal)
		r.registerLifecycleApproval(server, principal)
		r.registerAgentCommandApproval(server, principal)
		r.registerSystemApproval(server, principal)
		r.registerDeliveryApproval(server, principal)
		// 交付组单 / 止损（FR-246 / FR-247）：draft 阶段组单与进行中单的止损都是**直接执行 + 写审计**，
		// 不发审批票据（对齐告警处置先例）——故与上面的申请类工具分两个注册函数、两种语义。
		r.registerDeliveryDirectTools(server, principal)
		// 告警处置：**高风险**（`mcpToolCatalog` 登记为 high、与 spec / API 文档同档）但直接执行——
		// 管理台同语义可直执、故不发审批票据；批量仅影响 open 行、幂等且同事务写审计；
		// 单条与批量都受调用者观测范围约束（见 mcp_alert_tools.go）。风险等级与是否走审批是两把尺子，
		// 不要因为「直接执行」就把注释写成中等风险——那会成为后人放宽门禁的借口。
		r.registerAlertTools(server, principal)
	}
	// 审批决定需专用能力；仅受信 automation 客户端持有，用于内网闭环审批。
	if principal.HasCapability(auth.CapabilityApprovalDecide) {
		r.registerApprovalDecision(server, principal)
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

// mcpDeliveryResumeInput 是「创建继续灰度审批申请」的入参。
//
// Mode 是恢复模式，只接受 retry_failed / skip_failed（与 HTTP 面 resumeBody.mode 及 service 的
// resumeMode* 常量同一枚举）；MCP 面按告警处置的 status 白名单先例**先判枚举**，不把非法值交给领域层。
// 字段描述随 schema 下发给客户端，使 AI 无需读文档即知合法取值。
type mcpDeliveryResumeInput struct {
	OrderID        uint   `json:"orderId"`
	Mode           string `json:"mode" jsonschema:"恢复模式：retry_failed（重试失败的目标）或 skip_failed（跳过失败的目标）"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// mcpDeliverySubmitInput 是提交审批申请入参。
type mcpDeliverySubmitInput struct {
	OrderID        uint   `json:"orderId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// mcpDeliveryDeleteInput 是草稿删除申请入参。
//
// 与 mcpDeliverySubmitInput **刻意分成两个类型**：两者当前字段集相同纯属巧合，语义（删除 vs 提交）
// 与后续演进（删除可能补确认串、提交可能补摘要）互不相干；共用一个类型会让任一方的字段变更悄悄
// 改到另一方的对外契约。
type mcpDeliveryDeleteInput struct {
	OrderID        uint   `json:"orderId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// mcpDeliveryRollbackInput 是整单回滚申请入参。
type mcpDeliveryRollbackInput struct {
	OrderID        uint   `json:"orderId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// mcpDeliveryRollbackFinishInput 是结束回滚申请入参：**不收 reason**。
//
// 结束回滚在 HTTP 面与 service 侧都不接受调用方原因（申请原因固定为「结束交付回滚」，
// 已写入冻结 payload 与审批依据），此前 MCP 面却声明了 reason 字段并静默丢弃——AI 以为
// 提供了原因、实际什么都没发生。收口方式是移除该字段而不是新增持久化：给它补原因要改冻结
// payload（连带审批指纹与执行侧），属本波范围外的审批契约变更（FR-250 二选一）。
type mcpDeliveryRollbackFinishInput struct {
	OrderID        uint   `json:"orderId"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// mcpDeliveryBatchInput 是批次确认申请入参。
type mcpDeliveryBatchInput struct {
	OrderID        uint   `json:"orderId"`
	BatchNo        int    `json:"batchNo"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ── FR-246 / FR-247：交付域直执写工具入参 ──

// mcpDeliveryConfigChangeInput 是组单的配置变更项入参（键名沿用 HTTP 面 changeConfigChangeInput，
// 即 contracts 的 ConfigChangeInput）：configFromVersionId 由服务端按 ADR-0071 计算，客户端携带值不采信。
type mcpDeliveryConfigChangeInput struct {
	ConfigScopeKind   string `json:"configScopeKind"`
	ConfigScopeID     uint   `json:"configScopeId"`
	ConfigToVersionID uint   `json:"configToVersionId"`
}

// mcpDeliverySelectorInput 是组单的目标筛选器入参。
//
// 与 service.ChangeSelector 同键名（键名即 HTTP 契约），但各选取维度**都可缺省**（缺省 = 空集合）：
// 直接用 service 的结构体会让 schema 把 regions / zones / servers / excludes 全部标成必填，
// 客户端为了通过校验不得不把空数组一个个写出来——那是把存储形状当成入参契约。
type mcpDeliverySelectorInput struct {
	All      bool     `json:"all,omitempty"`
	Regions  []uint   `json:"regions,omitempty"`
	Zones    []uint   `json:"zones,omitempty"`
	Servers  []string `json:"servers,omitempty"`
	Excludes []string `json:"excludes,omitempty"`
}

// toService 映射为组单服务的筛选器。
func (s *mcpDeliverySelectorInput) toService() *service.ChangeSelector {
	if s == nil {
		return nil
	}
	return &service.ChangeSelector{
		All: s.All, Regions: s.Regions, Zones: s.Zones, Servers: s.Servers, Excludes: s.Excludes,
	}
}

// mcpDeliveryOrderEditFields 是组单的可编辑字段集（create 与 update 共形，故抽成一处防漂移）。
//
// 指针字段 = 「未提供」（创建取默认、编辑保持不变），与 service.ChangeOrderInput 同语义；
// 故阈值类字段用指针而非值——`failureRateThresholdPercent: 0` 是「关闭熔断」的有效取值，
// 用值类型会与「未提供」混淆。
type mcpDeliveryOrderEditFields struct {
	Description                   *string                        `json:"description,omitempty"`
	SourceServerID                *string                        `json:"sourceServerId,omitempty"`
	ScanDir                       *string                        `json:"scanDir,omitempty"`
	Selector                      *mcpDeliverySelectorInput      `json:"selector,omitempty"`
	BatchMode                     *string                        `json:"batchMode,omitempty"`
	BatchSizes                    []int                          `json:"batchSizes,omitempty"`
	ActivationMethod              *string                        `json:"activationMethod,omitempty"`
	ObserveWindowSec              *int                           `json:"observeWindowSec,omitempty"`
	ActivateTimeoutSec            *int                           `json:"activateTimeoutSec,omitempty"`
	FailureRateThresholdPercent   *int                           `json:"failureRateThresholdPercent,omitempty"`
	UnhealthyRateThresholdPercent *int                           `json:"unhealthyRateThresholdPercent,omitempty"`
	ConfigChanges                 []mcpDeliveryConfigChangeInput `json:"configChanges,omitempty"`
}

// mcpDeliveryCreateInput 是建 draft 变更单入参（组单一次成型：configChanges 随创建一并写入）。
// namespaceId 走 mcpScopeInput（字符串形式的数值 ID，必填），与交付只读工具同口径。
type mcpDeliveryCreateInput struct {
	mcpScopeInput
	Title string `json:"title"`
	mcpDeliveryOrderEditFields
}

// mcpDeliveryUpdateInput 是编辑 draft 入参：orderId 定位 + 同 create 的可改字段子集（无 namespaceId）。
type mcpDeliveryUpdateInput struct {
	mcpScopeInput
	OrderID uint    `json:"orderId"`
	Title   *string `json:"title,omitempty"`
	mcpDeliveryOrderEditFields
}

// mcpDeliveryOrderWriteInput 是按 orderId 定位的直执工具入参（差异扫描 / 暂停共形）。
type mcpDeliveryOrderWriteInput struct {
	mcpScopeInput
	OrderID uint `json:"orderId"`
}

// mcpDeliveryCancelInput 是紧急终止入参：reason **必填**（与 HTTP 面一致，原因入审计与单据）。
type mcpDeliveryCancelInput struct {
	mcpScopeInput
	OrderID uint   `json:"orderId"`
	Reason  string `json:"reason"`
}

// toServiceInput 把 MCP 入参映射为组单服务入参（nil = 未提供，与 HTTP 面 toServiceInput 同语义）。
func (f mcpDeliveryOrderEditFields) toServiceInput() service.ChangeOrderInput {
	input := service.ChangeOrderInput{
		Description: f.Description, SourceServerID: f.SourceServerID, ScanDir: f.ScanDir,
		Selector: f.Selector.toService(), BatchMode: f.BatchMode, ActivationMethod: f.ActivationMethod,
		ObserveWindowSec: f.ObserveWindowSec, ActivateTimeoutSec: f.ActivateTimeoutSec,
		FailureRateThresholdPercent:   f.FailureRateThresholdPercent,
		UnhealthyRateThresholdPercent: f.UnhealthyRateThresholdPercent,
	}
	if len(f.BatchSizes) > 0 {
		sizes := append([]int(nil), f.BatchSizes...)
		input.BatchSizes = &sizes
	}
	// 显式传入空数组 = 清空配置项（与 service 的 nil 语义区分），故按「字段是否出现」而非长度判断。
	if f.ConfigChanges != nil {
		changes := make([]service.ChangeConfigInput, 0, len(f.ConfigChanges))
		for _, change := range f.ConfigChanges {
			changes = append(changes, service.ChangeConfigInput{
				ConfigScopeKind: change.ConfigScopeKind, ConfigScopeID: change.ConfigScopeID,
				ConfigToVersionID: change.ConfigToVersionID,
			})
		}
		input.ConfigChanges = &changes
	}
	return input
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
	mcpAddTool(server, &mcp.Tool{Name: name, Description: description}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpLifecycleInput) (*mcp.CallToolResult, map[string]any, error) {
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
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status, "operationKey": ticket.OperationKey}, nil
	})
}

func (r *MCPToolRegistry) registerConfigApproval(server *mcp.Server, principal auth.Principal) {
	if r.configs == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.config.publish", Description: "提交配置发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestPublish(in.ID, in.Content, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.config.rollback", Description: "提交配置回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.config.gray.publish", Description: "提交配置灰度发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestGrayPublish(in.ID, in.Content, in.Cohort, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.config.gray.promote", Description: "提交配置灰度晋升审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestGrayPromote(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.config.delete", Description: "提交配置删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConfigInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.configs.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
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
	mcpAddTool(server, &mcp.Tool{Name: name, Description: "提交配置批量删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpConfigApprovalTicketView(ticket), nil
	})
}

func registerConfigBatchSetEnabledTool(server *mcp.Server, name string, principal auth.Principal, enabled bool, request configBatchSetEnabledRequester) {
	mcpAddTool(server, &mcp.Tool{Name: name, Description: "提交配置批量启停审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, enabled, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpConfigApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerFileOverrideApproval(server *mcp.Server, principal auth.Principal) {
	if r.files == nil {
		r.registerOverrideSetApproval(server, principal)
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.files.create", Description: "提交文件创建审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.files.RequestCreate(service.CreateFileParams{Namespace: in.Namespace, Group: in.Group, Path: in.Path, ScopeLevel: in.ScopeLevel, ScopeTarget: in.ScopeTarget, Content: in.Content, Operator: principal.AuditRef(), Comment: in.Comment, WholeFileOverride: in.WholeFileOverride, SensitiveExcluded: in.SensitiveExcluded, ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.files.import", Description: "提交文件批量导入审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileImportInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.files.RequestImport(service.ImportFilesParams{Namespace: in.Namespace, Group: in.Group, ScopeLevel: in.ScopeLevel, ScopeTarget: in.ScopeTarget, Files: in.Files, Operator: principal.AuditRef(), Comment: in.Comment, ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.files.publish", Description: "提交文件发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFilePublishInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.files.RequestPublish(in.ID, in.Content, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.files.rollback", Description: "提交文件回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.files.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.files.delete", Description: "提交文件删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFileDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.files.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	registerFileBatchTool(server, "beacon.files.batch.delete", principal, r.files.RequestBatchDelete)
	registerFileBatchSetEnabledTool(server, "beacon.files.batch.enable", principal, true, r.files.RequestBatchSetEnabled)
	registerFileBatchSetEnabledTool(server, "beacon.files.batch.disable", principal, false, r.files.RequestBatchSetEnabled)
	r.registerOverrideSetApproval(server, principal)
}

func (r *MCPToolRegistry) registerOverrideSetApproval(server *mcp.Server, principal auth.Principal) {
	if r.overrides == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.override-sets.publish", Description: "提交覆盖集发布审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverridePublishInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestPublish(in.ID, service.PublishOverrideSetParams{TargetRoot: in.TargetRoot, ReloadCommand: in.ReloadCommand, Comment: in.Comment, Operator: principal.AuditRef(), ClientIP: "mcp"}, in.Reason, in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.override-sets.rollback", Description: "提交覆盖集回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverrideRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestRollback(in.ID, in.Version, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.override-sets.delete", Description: "提交覆盖集删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOverrideDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.overrides.RequestDelete(in.ID, in.Reason, in.IdempotencyKey, principal.AuditRef(), in.Comment, "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

type fileBatchRequester func([]uint, string, string, string, string, auth.Principal) (service.FileApprovalTicket, error)
type fileBatchSetEnabledRequester func([]uint, bool, string, string, string, string, auth.Principal) (service.FileApprovalTicket, error)

func registerFileBatchTool(server *mcp.Server, name string, principal auth.Principal, request fileBatchRequester) {
	mcpAddTool(server, &mcp.Tool{Name: name, Description: "提交文件批量删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

func registerFileBatchSetEnabledTool(server *mcp.Server, name string, principal auth.Principal, enabled bool, request fileBatchSetEnabledRequester) {
	mcpAddTool(server, &mcp.Tool{Name: name, Description: "提交文件批量启停审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchIDsInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IDs, enabled, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpFileApprovalTicketView(ticket), nil
	})
}

// registerSensitiveReadApproval 仅转发既有申请与一次性消费校验，不把敏感正文放进 MCP 响应。
func (r *MCPToolRegistry) registerSensitiveReadApproval(server *mcp.Server, principal auth.Principal) {
	if r.assets != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.assets.preview.request", Description: "提交敏感文件预览审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssetPreviewRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			request, err := r.assets.RequestAccess(in.ServerID, in.Path, in.Reason, in.IdempotencyKey, principal, "mcp")
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": request.RequestID, "status": request.Status}, nil
		})
		mcpAddTool(server, &mcp.Tool{Name: "beacon.assets.preview.consume", Description: "消费已批准的敏感文件预览授权，不返回文件正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssetPreviewConsumeInput) (*mcp.CallToolResult, map[string]any, error) {
			if _, err := r.assets.ConsumeApproved(in.GrantID, in.CommandID, principal); err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, map[string]any{"status": "consumed"}, nil
		})
	}
	if r.messages != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.messages.payload.request", Description: "提交消息正文读取审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessagePayloadRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			request, err := r.messages.RequestAccess(in.MessageID, in.Reason, in.IdempotencyKey, principal, "mcp")
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, map[string]any{"approvalRequestId": request.RequestID, "status": request.Status}, nil
		})
		mcpAddTool(server, &mcp.Tool{Name: "beacon.messages.payload.consume", Description: "消费已批准的消息正文授权，不返回消息正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessagePayloadConsumeInput) (*mcp.CallToolResult, map[string]any, error) {
			if _, err := r.messages.Consume(in.GrantID, in.MessageID, principal); err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, map[string]any{"status": "consumed"}, nil
		})
	}
}

func (r *MCPToolRegistry) registerOwnApprovalRead(server *mcp.Server, principal auth.Principal) {
	mcpAddTool(server, &mcp.Tool{Name: "beacon.approvals.own.list", Description: "查询当前 MCP 客户端自己的审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalListInput) (*mcp.CallToolResult, map[string]any, error) {
		items, total, err := r.approvals.ListPage(service.ApprovalListFilter{
			Status: in.Status, OperationKey: in.Operation, Page: normalizedMCPPage(in.Page), PageSize: normalizedMCPPageSize(in.PageSize),
		}, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		views := make([]map[string]any, 0, len(items))
		for i := range items {
			views = append(views, mcpApprovalViewLightweight(&items[i]))
		}
		return &mcp.CallToolResult{}, map[string]any{"items": views, "total": total}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.approvals.own.get", Description: "查询当前 MCP 客户端自己的单个审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalGetInput) (*mcp.CallToolResult, map[string]any, error) {
		item, err := r.approvals.Detail(in.RequestID, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalViewFull(&item), nil
	})
}

func (r *MCPToolRegistry) registerOwnApprovalWithdraw(server *mcp.Server, principal auth.Principal) {
	mcpAddTool(server, &mcp.Tool{Name: "beacon.approvals.own.withdraw", Description: "撤回当前 MCP 客户端自己仍处于待处理状态的审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpOwnApprovalGetInput) (*mcp.CallToolResult, map[string]any, error) {
		item, err := r.approvals.Withdraw(in.RequestID, principal, "mcp")
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalViewLightweight(&item), nil
	})
}

// mcpApprovalDecisionInput 审批决定入参；reason 为拒绝/批准理由（拒绝必填）。
type mcpApprovalDecisionInput struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason,omitempty"`
}

// registerApprovalDecision 登记审批决定工具；仅 automation profile 可见，供内网受信客户端闭环审批。
// 批准与拒绝都写强审计；批准后由 approval worker 执行领域动作。
func (r *MCPToolRegistry) registerApprovalDecision(server *mcp.Server, principal auth.Principal) {
	if r.approvals == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.approvals.approve", Description: "批准一条待处理审批申请（受信 automation 客户端；服务端记强审计）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpApprovalDecisionInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.RequestID == "" {
			return mcpRejectedResultWithReason("缺少 requestId")
		}
		item, err := r.approvals.Approve(in.RequestID, principal, "mcp")
		if err != nil {
			return mcpRejectedResultWithReason(err.Error())
		}
		return &mcp.CallToolResult{}, mcpApprovalViewLightweight(&item), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.approvals.reject", Description: "拒绝一条待处理审批申请（须给理由；受信 automation 客户端）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpApprovalDecisionInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.RequestID == "" || in.Reason == "" {
			return mcpRejectedResultWithReason("缺少 requestId 或 reason")
		}
		item, err := r.approvals.Reject(in.RequestID, principal, "mcp", in.Reason)
		if err != nil {
			return mcpRejectedResultWithReason(err.Error())
		}
		return &mcp.CallToolResult{}, mcpApprovalViewLightweight(&item), nil
	})
}

func (r *MCPToolRegistry) registerAPIKeyApproval(server *mcp.Server, principal auth.Principal) {
	if r.apiKeys == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.credentials.api-key.create", Description: "提交 API 密钥创建审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAPIKeyCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.apiKeys.RequestCreate(in.Name, in.Role, nil, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.credentials.api-key.rotate", Description: "提交 API 密钥轮换审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAPIKeyRotateInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.apiKeys.RequestReset(in.ID, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
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
	mcpAddTool(server, &mcp.Tool{Name: "beacon.identity.agent.approve", Description: "提交身份确认审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityApproveInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestApproveAgentIdentity(in.IdentityID, service.ApproveAgentIdentityParams{ServerID: in.ServerID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.identity.agent.resolve-conflict", Description: "提交身份冲突处置审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityConflictInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestResolveAgentIdentityConflict(in.IdentityID, service.ResolveConflictParams{KeepBootID: in.KeepBootID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

type identityApprovalRequester func(string, service.IdentityTransitionParams, auth.Principal, string) (service.ApprovalTicketView, error)

func registerIdentityTransitionTool(server *mcp.Server, name string, principal auth.Principal, request identityApprovalRequester) {
	mcpAddTool(server, &mcp.Tool{Name: name, Description: "提交身份生命周期审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpIdentityTransitionInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := request(in.IdentityID, service.IdentityTransitionParams{Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerNamespaceTrustApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.trust.namespace.grant", Description: "提交 namespace 信任授予审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpNamespaceTrustInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestGrantNamespaceTrust(service.GrantNamespaceTrustParams{FromNamespaceID: in.FromNamespaceID, ToNamespaceID: in.ToNamespaceID, Capability: in.Capability, Note: in.Note, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerTopologyApproval(server *mcp.Server, principal auth.Principal) {
	if r.v2 == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.servers.assign", Description: "提交服务器分配审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAssignServersInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestAssignServers(service.AssignServersParams{ServerIDs: in.ServerRowIDs, TargetKind: in.TargetKind, TargetID: in.TargetID, IsDefaultEntry: in.IsDefaultEntry, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.servers.rezone", Description: "提交服务器换区审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpRezoneServersInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestRezoneServers(service.RezoneServersParams{ServerIDs: in.ServerRowIDs, TargetKind: in.TargetKind, TargetID: in.TargetID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.server.transfer-placement", Description: "提交服务器大厅归属迁移审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpPlacementTransferInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestTransferServerPlacement(service.ServerPlacementTransferParams{ServerID: in.ServerID, TargetKind: in.TargetKind, TargetID: in.TargetID, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.server.disable-draining", Description: "提交服务器取消排空审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDisableDrainingInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestDisableServerDraining(service.SetServerDrainingParams{ServerID: in.ServerID, Draining: false, Reason: in.Reason, Operator: principal.AuditRef(), ClientIP: "mcp"}, principal, in.IdempotencyKey)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.server.set-default-entry", Description: "提交服务器默认入口变更审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDefaultEntryInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.v2.RequestSetServerDefaultEntryByServerID(in.ServerID, in.Value, in.Reason, principal.AuditRef(), "mcp", in.IdempotencyKey, principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerAgentCommandApproval(server *mcp.Server, principal auth.Principal) {
	if r.commands == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.agent.server.resync", Description: "提交在线实例强制重同步审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAgentResyncInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.commands.RequestResyncApproval(in.NamespaceCode, in.ServerID, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

func (r *MCPToolRegistry) registerSystemApproval(server *mcp.Server, principal auth.Principal) {
	if r.updates != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.system.update.apply", Description: "提交控制面更新审批申请"}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSystemRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.updates.RequestApply(ctx, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
		})
		mcpAddTool(server, &mcp.Tool{Name: "beacon.system.update.rollback", Description: "提交控制面回滚审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSystemRequestInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.updates.RequestRollback(in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
		})
	}
	if r.settings == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.system.settings.update-dangerous", Description: "提交高影响设置变更审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDangerousSettingInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.settings.RequestUpdate(in.Key, in.Value, in.Reason, in.IdempotencyKey, principal.AuditRef(), "mcp", principal)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpApprovalTicketView(ticket), nil
	})
}

// registerDeliveryApproval 登记交付域「只创建审批申请」的六个工具：工具本身不执行领域动作，
// 只把单据冻到待审批态并创建统一审批申请，批准后由 approval worker 执行（spec §3 的审批链路）。
//
// 拒绝路径统一经 mcpDeliveryErrReason 映射为可区分的中文原因（FR-248）：此前一律回硬编码文案、
// 把领域错误整个丢掉，AI 只能得到「被拒了」而不知为何——状态不允许、缺模板源、无目标、缺原因
// 这几类处置方向完全不同（等状态 / 补源 / 改 selector / 补参数）。
func (r *MCPToolRegistry) registerDeliveryApproval(server *mcp.Server, principal auth.Principal) {
	if r.delivery == nil && r.orders == nil {
		return
	}
	if r.orders != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.submit", Description: "提交变更单统一审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliverySubmitInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.orders.RequestSubmit(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
			if err != nil {
				return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
			}
			return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
		})
		mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.delete", Description: "提交变更单草稿删除审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryDeleteInput) (*mcp.CallToolResult, map[string]any, error) {
			ticket, err := r.orders.RequestDelete(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
			if err != nil {
				return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
			}
			return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
		})
	}
	if r.delivery == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.resume", Description: "提交交付变更单继续审批申请（mode 仅 retry_failed / skip_failed）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryResumeInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := mcpDeliveryResumeMode(in.Mode); !ok {
			return mcpRejectedResultWithReason(mcpDeliveryResumeModeRejectedReason)
		}
		ticket, err := r.delivery.RequestResume(in.OrderID, in.Mode, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.rollback", Description: "提交交付变更单回滚审批申请（原因必填）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryRollbackInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestRollback(in.OrderID, in.Reason, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.batch.confirm", Description: "提交交付批次确认审批申请"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryBatchInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestConfirmBatch(in.OrderID, in.BatchNo, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.rollback.finish", Description: "提交交付回滚结束审批申请（无原因入参，申请原因由服务端固定）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryRollbackFinishInput) (*mcp.CallToolResult, map[string]any, error) {
		ticket, err := r.delivery.RequestFinishRollback(in.OrderID, principal, in.IdempotencyKey, principal.AuditRef(), mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryTicketView(ticket), nil
	})
}

// ── FR-246 / FR-247：交付域直执写工具 ──
//
// 五项工具与上面六项**分档**：组单（create / update / diff-scan）与止损（pause / cancel）按 FR-211
// 与规格 §3.1 的口径**直接执行并写审计**，不创建审批票据（catalog 的 OperationKind 留空），
// 因此不经统一审批服务、也没有幂等键入参（领域动作自身幂等：创建各建一单、编辑是整组覆盖、
// 重扫是整组替换、暂停/终止有状态机 CAS 守卫）。
//
// 观测范围与只读工具同源：建单必须落在调用者可观测的 namespace 内（缺 namespaceId 或范围外即拒），
// 其余四项按 orderId 定位、按单所属 namespace 判归属，**范围外与不存在共用同一条文案**
// （不把范围校验变成存在性探针，与 mcp_alert_tools.go 单条处置同口径）。
//
// 审计口径（FR-250）：五项都把 clientIP 记为 mcpClientIP，与既有六项申请类工具逐字一致，
// 使审计能区分「机器主体经 MCP 直执」与「人类管理台操作」。
func (r *MCPToolRegistry) registerDeliveryDirectTools(server *mcp.Server, principal auth.Principal) {
	op := principal.AuditRef()
	r.registerDeliveryOrderWriteTools(server, op)
	r.registerDeliveryDiffScanTool(server, op)
	r.registerDeliveryStopTools(server, op)
}

// registerDeliveryOrderWriteTools 登记组单工具（建单 / 编辑），依赖组单生命周期服务。
func (r *MCPToolRegistry) registerDeliveryOrderWriteTools(server *mcp.Server, op string) {
	if r.orders == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.create", Description: "创建 draft 变更单（可携带 configChanges 一次成型；namespaceId 为字符串形式的数值 ID，必填）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryCreateInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResultWithReason(mcpScopeRejectedReason)
		}
		if in.NamespaceID == "" {
			return mcpRejectedResultWithReason(mcpDeliveryNamespaceRejectedReason)
		}
		nsID, err := r.mcpResolveNamespaceID("", in.NamespaceID, scope)
		if err != nil {
			return mcpRejectedResultWithReason(mcpScopeRejectedReason)
		}
		input := in.toServiceInput()
		input.Title = &in.Title
		detail, err := r.orders.Create(nsID, input, op, mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryWriteOrderView(detail), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.update", Description: "编辑 draft 变更单（approved 单编辑会作废审批回 draft；configChanges 整组替换，传空数组即清空）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryUpdateInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryWritableOrder(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResultWithReason(mcpDeliveryOutOfScopeRejectedReason)
		}
		input := in.toServiceInput()
		input.Title = in.Title
		detail, err := r.orders.Update(in.OrderID, input, op, mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryWriteOrderView(detail), nil
	})
}

// registerDeliveryDiffScanTool 登记差异扫描工具（另依赖差异面服务；未装配即整项不注册）。
func (r *MCPToolRegistry) registerDeliveryDiffScanTool(server *mcp.Server, op string) {
	if r.reads.deliveryDiff == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.diff-scan", Description: "同步重扫文件差异（要求 draft + 已指定模板源；只回计数聚合，不回逐文件清单）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderWriteInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryWritableOrder(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResultWithReason(mcpDeliveryOutOfScopeRejectedReason)
		}
		view, err := r.reads.deliveryDiff.DiffScan(in.OrderID, op, mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryDiffScanView(view), nil
	})
}

// registerDeliveryStopTools 登记止损工具（暂停 / 终止），依赖灰度编排器的直执入口。
func (r *MCPToolRegistry) registerDeliveryStopTools(server *mcp.Server, op string) {
	if r.delivery == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.pause", Description: "人工暂停进行中的变更单（rolling → paused，不打断在途目标）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderWriteInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryWritableOrder(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResultWithReason(mcpDeliveryOutOfScopeRejectedReason)
		}
		detail, err := r.delivery.Pause(in.OrderID, op, mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryWriteOrderView(detail), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.cancel", Description: "紧急终止变更单（rolling / paused → cancelled；reason 必填并入审计）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryCancelInput) (*mcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.Reason) == "" {
			return mcpRejectedResultWithReason(mcpDeliveryReasonRequiredRejectedReason)
		}
		if _, ok := r.mcpDeliveryWritableOrder(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResultWithReason(mcpDeliveryOutOfScopeRejectedReason)
		}
		detail, err := r.delivery.Cancel(in.OrderID, in.Reason, op, mcpClientIP)
		if err != nil {
			return mcpRejectedResultWithReason(mcpDeliveryErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpDeliveryWriteOrderView(detail), nil
	})
}

// mcpClientIP 是 MCP 面写操作在审计 / 冻结 payload 里登记的来源地址口径。
//
// MCP 工具拿不到（也不需要）调用方的网络地址，而领域方法都要一个 clientIP：统一记 "mcp"，
// 使审计能区分「机器主体经 MCP 直执」与「人类管理台操作（记真实来源地址）」。语义为**来源类别**
// 而非网络地址，已在 docs/API.md 的 MCP 段声明。
const mcpClientIP = "mcp"

// mcpDeliveryWritableOrder 解析观察范围并按 orderId 取单，校验单归属落在范围内（写工具版）。
//
// 复用只读工具的取值口径（mcpDeliveryOrderDetail）：单的 namespace 是服务端事实，范围外与不存在
// 都返回 false，调用方因此只能回同一条拒绝文案——不泄露范围外单是否存在。
func (r *MCPToolRegistry) mcpDeliveryWritableOrder(in mcpScopeInput, orderID uint) (*service.ChangeOrderDetailView, bool) {
	if r.orders == nil {
		return nil, false
	}
	return r.mcpDeliveryOrderDetail(in, orderID)
}

// mcpDeliveryWriteOrderView 投影直执工具的执行结果：只回单据定位与生效状态。
//
// 不回详情视图：create / update / pause / cancel 的返回体是「这次动作把单带到了哪个状态」，
// 全貌走只读工具 order.get / targets.list / events.list 按需拉取（与本域只读投影分工一致）。
func mcpDeliveryWriteOrderView(view *service.ChangeOrderDetailView) map[string]any {
	if view == nil {
		return map[string]any{}
	}
	return map[string]any{"orderId": view.ID, "status": view.Status}
}

// mcpDeliveryDiffScanView 投影差异扫描结果为**计数聚合**：
// 逐文件明细在大单（上千差异项）下会撑爆返回体，且 AI 关心的是「差异面有多大」；
// 明细仍可由 HTTP 面 / 后续按需工具拉取。键名对齐规格 §3.3，动作取值对齐 model.ChangeItemAction*。
func mcpDeliveryDiffScanView(view *service.DiffScanView) map[string]any {
	out := map[string]any{"add": 0, "update": 0, "delete": 0, "total": 0, "snapshotAt": nil}
	if view == nil {
		return out
	}
	counts := map[string]int{"add": 0, "update": 0, "delete": 0}
	for _, item := range view.Items {
		if item.Action == nil {
			continue
		}
		if _, known := counts[*item.Action]; known {
			counts[*item.Action]++
		}
	}
	out["add"], out["update"], out["delete"] = counts["add"], counts["update"], counts["delete"]
	out["total"] = len(view.Items)
	out["snapshotAt"] = mcpNullableTime(view.DiffSnapshotAt)
	return out
}

// ── FR-248：交付域拒绝理由映射 ──

const (
	// mcpDeliveryIllegalStateCode 是交付状态机非法迁移的错误码：由 service 的 changeIllegalState 构造
	// （无 apperr 预定义项），其 Message 自带当前状态与目标动作。
	mcpDeliveryIllegalStateCode = "illegal_state"
	// mcpDeliveryResumeModeRetryFailed / SkipFailed 是恢复模式枚举（与 service 的 resumeMode* 常量、
	// HTTP 面 resumeBody.mode 同取值）。
	mcpDeliveryResumeModeRetryFailed = "retry_failed"
	mcpDeliveryResumeModeSkipFailed  = "skip_failed"
	// mcpDeliveryResumeModeRejectedReason 是恢复模式越界的拒绝文案。
	mcpDeliveryResumeModeRejectedReason = "mode 仅支持 retry_failed / skip_failed"
	// mcpDeliveryNamespaceRejectedReason 是组单缺少落点环境的拒绝文案（写操作必须显式声明 namespace）。
	mcpDeliveryNamespaceRejectedReason = "必须填写 namespaceId（字符串形式的数值 ID）"
	// mcpDeliveryOutOfScopeRejectedReason 是写工具「目标单不可操作」文案，**刻意同时覆盖**两种情况：
	// ① 单不在调用者观测范围内；② 单不存在。共用一条理由，避免调用方拿两者差异探测范围外是否存在该单
	// （与只读工具、告警处置同口径）。
	mcpDeliveryOutOfScopeRejectedReason = "变更单不存在或不在观察范围内"
	// mcpDeliveryRejectedFallbackReason 是非领域错误（存储层等）的统一兜底文案：不透传内部细节，也绝不回空文案。
	mcpDeliveryRejectedFallbackReason = "交付操作未完成"
)

// mcpDeliveryRejectedReasons 是交付系工具的错误码 → 稳定中文短语映射表（FR-248，规格 §3.5）。
//
// 本表只做「code → 短语」一件事，**不复制错误语义**：code 一律取自既有真源（多数直接用
// apperr 的定义，故改名会编译失败、不会静默失配），短语是面向 AI 的稳定文案——同一错误码的文案
// 不随领域内部措辞调整而漂移，AI 可据此分支处置。未列入的 code 沿用领域错误自带的中文说明（见
// mcpDeliveryErrReason），绝不回空文案。
//
// 表中比规格 §3.5 多一项 missing_reason：它是交付域自身产出的「原因必填」错误码（终止 / 整单回滚），
// 与 approval_reason_required 同义，故映射到同一条文案——避免同一件事在 AI 侧出现两种说法。
var mcpDeliveryRejectedReasons = map[string]string{
	apperr.ErrApprovalReasonRequired.Code:       "必须填写原因（reason）",
	mcpDeliveryIllegalStateCode:                 "当前状态不允许该操作",
	apperr.ErrChangeNoItems.Code:                "变更单没有任何变更项，无法提交审批",
	apperr.ErrChangeNoTarget.Code:               "未解析出任何合格目标",
	apperr.ErrChangeNoRollbackTarget.Code:       "单内无曾推送的目标可回滚",
	apperr.ErrChangeSourceMissing.Code:          "未指定黄金模板源，无法扫描文件差异",
	apperr.ErrChangeSourceInvalid.Code:          "模板源必须已确认绑定且在线的 backend 子服",
	apperr.ErrChangeSourceSnapshotMissing.Code:  "模板源尚无文件资产快照，请先重扫",
	apperr.ErrChangeSelectorCrossNamespace.Code: "selector 引用了不属于本环境的实体",
	apperr.ErrChangeConfigVersionInvalid.Code:   "配置版本不存在或与作用域不匹配",
	apperr.ErrChangeBatchNotFound.Code:          "批次不存在",
	apperr.ErrChangeResumeModeRequired.Code:     "熔断/准备失败暂停必须指定 mode 与原因",
	apperr.ErrChangeOrderNotFound.Code:          "变更单不存在",
	apperr.ErrChangeApproverSeparation.Code:     "审批人不得是创建人",
	apperr.ErrChangeNotCreator.Code:             "仅创建人可撤回变更单",
	apperr.ErrForbidden.Code:                    "当前主体无权执行该操作",
	"missing_reason":                            "必须填写原因（reason）",
}

// mcpDeliveryReasonRequiredRejectedReason 是 MCP 面前置校验「原因必填」的文案：
// 取映射表中 approval_reason_required 的短语，使前置校验与服务侧错误码共用同一处文案。
var mcpDeliveryReasonRequiredRejectedReason = mcpDeliveryRejectedReasons[apperr.ErrApprovalReasonRequired.Code]

// mcpDeliveryResumeMode 校验恢复模式枚举；不合法即拒绝（MCP 面先判，不把非法值交给领域层）。
func mcpDeliveryResumeMode(raw string) (string, bool) {
	switch raw {
	case mcpDeliveryResumeModeRetryFailed, mcpDeliveryResumeModeSkipFailed:
		return raw, true
	default:
		return "", false
	}
}

// mcpDeliveryErrReason 把交付领域错误映射为可区分的中文原因（FR-248）。
//
// 顺序：查常量映射表 → 未列入则沿用领域错误自带的中文说明（已按 ADR-0057 脱敏）→ 非领域错误回统一兜底。
// 任何分支都不回空文案（规格 §3.5「其他」行）。
func mcpDeliveryErrReason(err error) string {
	if err == nil {
		return ""
	}
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) {
		return mcpDeliveryRejectedFallbackReason
	}
	reason, ok := mcpDeliveryRejectedReasons[domainErr.Code]
	if !ok {
		if strings.TrimSpace(domainErr.Message) != "" {
			return domainErr.Message
		}
		return mcpDeliveryRejectedFallbackReason
	}
	// illegal_state 是唯一「稳定骨架 + 领域细节」的条目：短语说明处置方向，具体卡点（当前状态与
	// 目标动作）由领域错误给出，拼在后面便于 AI 定位到具体迁移。
	if domainErr.Code == mcpDeliveryIllegalStateCode && strings.TrimSpace(domainErr.Message) != "" {
		return reason + "：" + domainErr.Message
	}
	return reason
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

// mcpApprovalViewLightweight 是审批申请的轻量投影（own.list / own.withdraw / approve / reject 共用）：
// 只回定位与状态必需字段，失败摘要按单行上限截断——列表一次可能回多行，完整摘要留给 own.get。
func mcpApprovalViewLightweight(req *model.ApprovalRequest) map[string]any {
	return map[string]any{
		"approvalRequestId": req.RequestID,
		"status":            req.Status,
		"operation":         req.OperationKey,
		"resultRef":         req.ResultRef,
		"createdAt":         req.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt":         approvalTime(req.ExpiresAt),
		"failureSummary":    mcpClampRunes(mcpApprovalFailureSummary(req), mcpApprovalFailureSummaryMaxRunes),
		"finishedAt":        approvalTime(req.FinishedAt),
	}
}

// mcpApprovalViewFull 是审批申请的全量投影（仅 own.get）：在轻量档之上补审批理由、审批主体与执行时间线。
// 时间字段有值即返（无值回空串），不组装数组，由 AI 按字段名自行解读时序。
// 刻意不投影审批申请的 text 列 ImpactSummary / SafeSummary——那会与票据的 impactSummary 对象同名两型。
func mcpApprovalViewFull(req *model.ApprovalRequest) map[string]any {
	view := mcpApprovalViewLightweight(req)
	view["failureSummary"] = mcpApprovalFailureSummary(req)
	view["rejectReason"] = req.RejectReason
	view["decisionReason"] = req.DecisionReason
	view["approvedBy"] = approvalText(req.ApprovedBy)
	view["decidedAt"] = approvalTime(req.DecidedAt)
	view["approvedAt"] = approvalTime(req.ApprovedAt)
	view["executedAt"] = approvalTime(req.ExecutedAt)
	return view
}

// mcpApprovalFailureSummaryMaxRunes 是 own.list 单行失败摘要的字符上限（完整摘要由 own.get 返回）。
const mcpApprovalFailureSummaryMaxRunes = 200

// mcpApprovalFailureSummary 取审批失败摘要：优先 FailureSummary，缺失时退回 FailureReason
// （两者都由审批 worker 在失败时写入，且已按 ADR-0057 脱敏，原样透出）。
func mcpApprovalFailureSummary(req *model.ApprovalRequest) string {
	if strings.TrimSpace(req.FailureSummary) != "" {
		return req.FailureSummary
	}
	return req.FailureReason
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

// mcpDeliveryTicketView 是交付申请票据的 MCP 投影。
// 既有键名 operation 保持不变（HTTP 面直出结构体、键为 operationKey）；本项两侧同步补 orderId 与 impactSummary。
// impactSummary 直接序列化交付服务的影响摘要结构体，与 HTTP 面共用同一份键名定义。
func mcpDeliveryTicketView(ticket service.DeliveryApprovalTicketView) map[string]any {
	return map[string]any{
		"approvalRequestId": ticket.ApprovalRequestID, "status": ticket.Status, "operation": ticket.OperationKey,
		"orderId": ticket.OrderID, "impactSummary": ticket.ImpactSummary,
	}
}

func approvalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// approvalText 取可选文本列的值；未落库（NULL）回空串。
func approvalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func mcpToolError() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "请求被拒绝或目标不可用"}}, IsError: true}
}

// mcpToolErrorWithReason 同 mcpToolError，但附带可读原因（用于审批决定等需要运维定位的场景）。
func mcpToolErrorWithReason(reason string) *mcp.CallToolResult {
	text := "请求被拒绝或目标不可用"
	if strings.TrimSpace(reason) != "" {
		text = text + "：" + reason
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}
}

// mcpRejectedResult 以 MCP 结果表达已处理的业务拒绝，不把领域错误作为协议故障泄露给客户端。
// 第三个返回值给出非 null 的空对象，避免 SDK 校验 structuredContent 为 record 时报错。
func mcpRejectedResult() (*mcp.CallToolResult, map[string]any, error) {
	return mcpToolError(), map[string]any{}, nil
}

// mcpRejectedResultWithReason 带原因的拒绝结果（同样满足 structuredContent 非 null）。
func mcpRejectedResultWithReason(reason string) (*mcp.CallToolResult, map[string]any, error) {
	return mcpToolErrorWithReason(reason), map[string]any{}, nil
}

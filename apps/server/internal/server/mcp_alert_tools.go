package server

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// 告警处置工具：单条处理 + 按筛选批量处理。
//
// 语义与既有 FR-157（确认 / 标记已处理工作流）与 FR-229（按筛选跨页批量处理）完全一致，
// 只是把管理台已直执的能力补到 MCP 面（此前 MCP 完全没有告警工具，遗留告警无法经 MCP 清理）。
//
// 定级依据（与 mcp_tool_catalog.go 的登记项一致）：告警处理是**运维元数据的直接变更**，与 HTTP 面
// `POST /admin/v1/alert-events/{id}/handle` 及 `POST /admin/v1/alert-events/handle` 语义逐字对齐
// （管理台可直执、无审批票据），故 MCP 面同样按「直接执行 + 同事务写审计」实现，不引入 approval ticket；
// 批量路径由 service 收敛为「一条 UPDATE，且只影响 status='open' 的行」并与审计同事务，因此天然幂等
// （重复调用第二次 affected=0）；单条路径可被后续处理改成其它状态，属可逆操作。
// 但不因此降为 low 档：关闭告警会让故障信号从运维视野中消失（改变生产可见状态），与只读工具不同档。
//
// 观测范围：两个工具**都**受调用者观测范围约束（MCP 面从严于管理台 HTTP 面——后者无 scope 入参，是既有形态）。
// 批量路径把 scope 注入 filter，作用集因此在 SQL 层就不可能越界；单条路径没有筛选条件可注入，
// 改为先按 id 读出目标告警、再按其 namespace 判定范围（见 mcpAlertInScope 与 mcpAlertOutOfScopeRejectedReason）。

type mcpAlertHandleInput struct {
	// 单条工具同样要求显式观测范围：缺省即全局范围（与管理台超管观察全体一致）。
	mcpScopeInput
	ID uint `json:"id"`
	// Status 是目标处理状态，只接受 acknowledged / resolved（与前端契约 HandleAlertBody.status 同口径）。
	Status string `json:"status"`
	// Note 是处理说明；去空白后为空即拒绝，处理必须留原因。
	Note string `json:"note"`
}

type mcpAlertBatchHandleInput struct {
	mcpScopeInput
	Namespace string `json:"namespace,omitempty"`
	ServerID  string `json:"serverId,omitempty"`
	Type      string `json:"type,omitempty"`
	Level     string `json:"level,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	// Status 是目标处理状态，只接受 acknowledged / resolved。
	Status string `json:"status"`
	// Note 是批量处置说明；去空白后为空即拒绝（一条 UPDATE 会改多行，无原因不可追溯）。
	Note string `json:"note"`
}

// registerAlertTools 登记告警处置工具；仅 automation profile 可见（写操作）。
func (r *MCPToolRegistry) registerAlertTools(server *mcp.Server, principal auth.Principal) {
	if r.alerts == nil {
		return
	}
	op := principal.AuditRef()

	mcpAddTool(server, &mcp.Tool{Name: "beacon.alerts.events.handle", Description: "处理单条告警事件（acknowledged / resolved，必填处理说明，直接执行并写审计；目标须位于调用者观测范围内）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAlertHandleInput) (*mcp.CallToolResult, map[string]any, error) {
		status, ok := mcpAlertHandleStatus(in.Status)
		if !ok {
			return mcpRejectedResultWithReason(mcpAlertStatusRejectedReason)
		}
		note, ok := mcpAlertHandleNote(in.Note)
		if !ok {
			return mcpRejectedResultWithReason(mcpAlertNoteRejectedReason)
		}
		// 观测范围先解析成功（与批量工具同序、同口径）：范围无效即拒，不进入任何读写。
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResultWithReason(mcpScopeRejectedReason)
		}
		// 单条路径没有筛选条件可注入，只能「先读目标、再按其 namespace 判范围」。这里的顺序是刻意的：
		// 判据是目标自身的事实（告警的 namespace 落库后终身不变，不存在读到处理之间被改走的窗口），
		// 且**范围外与不存在共用同一条拒绝理由**——若两者文案不同，差异本身就泄露了范围外目标是否存在。
		current, err := r.alerts.Get(in.ID)
		if err != nil {
			if errors.Is(err, apperr.ErrAlertEventNotFound) {
				return mcpRejectedResultWithReason(mcpAlertOutOfScopeRejectedReason)
			}
			return mcpRejectedResultWithReason(mcpAlertErrReason(err))
		}
		if !mcpAlertInScope(scope, current.Namespace) {
			return mcpRejectedResultWithReason(mcpAlertOutOfScopeRejectedReason)
		}
		updated, err := r.alerts.Handle(in.ID, status, note, op, "mcp")
		if err != nil {
			return mcpRejectedResultWithReason(mcpAlertErrReason(err))
		}
		return &mcp.CallToolResult{}, mcpAlertHandleView(updated), nil
	})

	mcpAddTool(server, &mcp.Tool{Name: "beacon.alerts.events.batch-handle", Description: "按筛选条件批量处理未处理（open）告警（必填处理说明，直接执行并写审计；仅影响 open 行，重复调用幂等）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAlertBatchHandleInput) (*mcp.CallToolResult, map[string]any, error) {
		status, ok := mcpAlertHandleStatus(in.Status)
		if !ok {
			return mcpRejectedResultWithReason(mcpAlertStatusRejectedReason)
		}
		note, ok := mcpAlertHandleNote(in.Note)
		if !ok {
			return mcpRejectedResultWithReason(mcpAlertNoteRejectedReason)
		}
		// 观测范围必须先解析成功，并把 scope 注入 filter：批量 UPDATE 的作用集因此**不可能越过调用者的观察范围**。
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResultWithReason(mcpScopeRejectedReason)
		}
		affected, err := r.alerts.HandleBatch(repository.AlertEventFilter{
			Namespace: in.Namespace, NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All,
			ServerID: in.ServerID, Type: in.Type, Level: in.Level,
			From: mcpParseTime(in.From), To: mcpParseTime(in.To),
		}, status, note, op, "mcp")
		if err != nil {
			return mcpRejectedResultWithReason(mcpAlertErrReason(err))
		}
		return &mcp.CallToolResult{}, map[string]any{"affected": affected}, nil
	})
}

// 告警工具的统一拒绝理由（MCP 面先判，不把非法输入交给 service 层报错）。
const (
	mcpAlertStatusRejectedReason = "status 仅支持 acknowledged / resolved"
	mcpAlertNoteRejectedReason   = "必须填写处理说明（note）"
	// mcpScopeRejectedReason 是观测范围解析失败的统一文案（单条 / 批量同口径）。
	mcpScopeRejectedReason = "观察范围无效"
	// mcpAlertOutOfScopeRejectedReason 是单条工具的「目标不可处置」文案，**刻意同时覆盖两种情况**：
	// ① 目标不在调用者观测范围内；② 目标不存在。两者共用一条理由，避免调用方拿「范围外」与「不存在」
	// 的差异探测范围外是否存在该告警（相当于把范围校验变成存在性探针）。
	mcpAlertOutOfScopeRejectedReason = "告警不存在或不在观察范围内"
)

// mcpAlertInScope 判断告警所属 namespace 是否落在调用者的观测范围内（全局范围恒真）。
// 口径与批量路径严格一致：受限范围下必须是 NamespaceCodes 的**精确成员**——批量把同一集合交给 SQL `IN`，
// 此处若自行放宽（如大小写归一 / 前缀匹配），单条就会比批量更宽，两条路径的安全边界随之漂移。
func mcpAlertInScope(scope service.ObservationScope, namespaceCode string) bool {
	if scope.All {
		return true
	}
	for _, code := range scope.NamespaceCodes {
		if code == namespaceCode {
			return true
		}
	}
	return false
}

// mcpAlertHandleStatus 做目标状态白名单：只接受 acknowledged / resolved。
func mcpAlertHandleStatus(raw string) (string, bool) {
	switch raw {
	case model.AlertEventStatusAcknowledged, model.AlertEventStatusResolved:
		return raw, true
	default:
		return "", false
	}
}

// mcpAlertHandleNote 校验处理说明非空（去首尾空白后判断）；空说明即拒绝，保证处置可追溯。
func mcpAlertHandleNote(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	// 返回原值而非 trimmed：运维手写的说明按原样落审计，不被工具侧静默改写。
	return raw, true
}

// mcpAlertErrReason 把领域错误转成可读原因。
//
// 只对已知领域错误给出文案，其余统一回通用说明——不透传存储层 / SQL 细节（与只读工具的脱敏口径一致）。
func mcpAlertErrReason(err error) string {
	if err == nil {
		return ""
	}
	var domainErr *apperr.Error
	if errors.As(err, &domainErr) {
		return domainErr.Message
	}
	return "告警处理未完成"
}

// mcpAlertHandleView 投影单条处理结果：id + 生效状态 + 处理人 + 处理时刻（未落库时刻为 null）。
func mcpAlertHandleView(event *model.AlertEvent) map[string]any {
	if event == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id": event.ID, "status": event.Status,
		"handledBy": mcpNullableString(event.HandledBy),
		"handledAt": mcpNullableTime(event.HandledAt),
	}
}

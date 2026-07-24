package service

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// maxPayloadViewReasonLen 是 payload 查看原因上限（≤255 字，spec §4.4）。
const maxPayloadViewReasonLen = 255

// auditWriter 是 payload 查看对审计落库的窄依赖（仅追加一条），由 repository.AuditLogRepository 满足。
type auditWriter interface {
	Create(entry *model.AuditLog) error
}

// MessagePayloadService 是 payload 受控查看服务（FR-150，见 spec §4.4）：
// 校验原因 → 定位消息 → **先写审计后返回内容**（同请求内审计写失败则整请求失败，不允许「看了没记录」）。
// 权限点 message.payload.view 由路由中间件裁决（POST 属写方法，readonly 经 readonlyWriteGuard 403）。
type MessagePayloadService struct {
	repo      *repository.MessageRepository
	auditRepo auditWriter
}

// NewMessagePayloadService 构造服务。
func NewMessagePayloadService(repo *repository.MessageRepository, auditRepo auditWriter) *MessagePayloadService {
	return &MessagePayloadService{repo: repo, auditRepo: auditRepo}
}

// ViewPayloadParams 是一次 payload 查看请求（operator/clientIp/traceId 为鉴权链与请求上下文注入）。
type ViewPayloadParams struct {
	MessageID string
	Reason    string
	Operator  string
	ClientIP  string
	TraceID   string
}

// PayloadResult 是 payload 查看响应（对齐 contracts MessagePayloadResponse）。
type PayloadResult struct {
	Payload string
	SHA256  string
	Size    int
}

// View 受控查看某消息 payload：原因必填 ≤255 字 → 消息须存在（404）→ 先写审计后返回内容。
// 审计写失败整请求失败（返回错误、绝不返回 payload）。payload 未落库时审计照记、返回空内容。
func (s *MessagePayloadService) View(p ViewPayloadParams) (PayloadResult, error) {
	if strings.TrimSpace(p.Reason) == "" || utf8.RuneCountInString(p.Reason) > maxPayloadViewReasonLen {
		return PayloadResult{}, apperr.ErrPayloadReasonRequired
	}
	trace, err := s.repo.FindByMessageID(p.MessageID)
	if err != nil {
		return PayloadResult{}, err
	}
	if trace == nil {
		return PayloadResult{}, apperr.ErrMessageNotFound
	}
	payload, err := s.repo.FindPayload(p.MessageID)
	if err != nil {
		return PayloadResult{}, err
	}
	// 先写审计（含 messageId/类型/来源目标/原因原文/traceId，绝不含 payload 内容）——写失败整请求失败。
	if err := s.writeViewAudit(trace, p); err != nil {
		return PayloadResult{}, err
	}
	if payload == nil {
		// payload 未落库（payload_stored=false 或已归档）：审计已记，返回空内容。
		return PayloadResult{}, nil
	}
	return PayloadResult{Payload: payload.Payload, SHA256: payload.SHA256, Size: payload.Size}, nil
}

// writeViewAudit 落一条 message.payload.view 审计：detail 记 messageId/类型/来源目标/原因原文/traceId + 跨域标记，绝不含 payload。
func (s *MessagePayloadService) writeViewAudit(trace *model.MsgTrace, p ViewPayloadParams) error {
	detail := map[string]any{
		"messageId":      trace.MessageID,
		"msgType":        trace.MsgType,
		"namespaceId":    trace.NamespaceID,
		"sourceServerId": trace.SourceServerID,
		"reason":         p.Reason,
		"traceId":        p.TraceID,
	}
	if trace.TargetKind == model.MsgTargetKindPlayer {
		detail["targetPlayer"] = trace.TargetPlayer
	} else {
		detail["targetServerId"] = trace.TargetServerID
	}
	if trace.ResolvedServerID != "" {
		detail["resolvedServerId"] = trace.ResolvedServerID
	}
	// 跨域消息的 payload 查看追加 cross_namespace 标记，满足「跨域行为额外审计」（spec §4.4.6）。
	if trace.CrossNamespace {
		detail["crossNamespace"] = true
	}
	raw, _ := json.Marshal(detail)
	entry := &model.AuditLog{
		// 第二版 namespace 为数值 id，audit_log.namespace_code 为字符串码：此处以数值 id 字符串记录，供追溯不误对齐环境码。
		NamespaceCode: strconv.FormatUint(uint64(trace.NamespaceID), 10),
		Operator:      p.Operator,
		Action:        model.ActionMessagePayloadView,
		TargetType:    model.TargetTypeMessage,
		TargetRef:     trace.MessageID,
		Detail:        string(raw),
		Result:        model.ResultOK,
		ClientIP:      p.ClientIP,
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Error("payload 查看审计落库失败，拒绝返回内容",
			"messageId", trace.MessageID, "operator", p.Operator, "traceId", p.TraceID, "原因", err)
		return err
	}
	slog.Info("payload 受控查看", "messageId", trace.MessageID, "msgType", trace.MsgType,
		"operator", p.Operator, "crossNamespace", trace.CrossNamespace, "traceId", p.TraceID)
	return nil
}

package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// AgentLogLine 是 agent 回传的一行日志（级别 + 已脱敏文本，FR-88，见 ADR-0040）。
// 脱敏在 agent 侧落环形缓冲那一刻完成，控制面只忠实转存 / 透传，不再处理原文。
type AgentLogLine struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// AgentLogResult 是取日志查询结果（命令状态 + 若 done 则附脱敏日志行，FR-88）。
type AgentLogResult struct {
	CommandID uint           `json:"commandId"`
	Status    string         `json:"status"`
	Lines     []AgentLogLine `json:"lines"`
}

// AgentLogService 编排取 agent 自身日志的「命令-回传」周期（FR-88，见 ADR-0040）。
//
// admin 触发 → 建 pending tail-logs 命令 + 单活跃限速 + 审计（detail 不含日志内容）+ 唤醒 agent；
// agent 拉命令 → 读自身日志环形缓冲快照（已脱敏）→ 回传；控制面把日志行存为命令瞬态（done）；
// admin 查询 → 取最近一条 tail-logs 命令状态，done 则解出脱敏日志行（只读不清，done 后可重复读；未终结命令超时由清理器过期即清空瞬态）。
//
// 严守 ADR-0040 边界：只承载 agent **自身**日志、行数由 agent 缓冲天然有界、瞬态不入持久真源 / 不进审计 detail。
type AgentLogService struct {
	db        *gorm.DB
	repo      *repository.AgentCommandRepository
	auditRepo *repository.AuditLogRepository
	notifier  CommandNotifier
	approval  *ApprovalService
	grants    *SensitiveAccessGrantService
}

// NewAgentLogService 构造服务。
func NewAgentLogService(db *gorm.DB, repo *repository.AgentCommandRepository, auditRepo *repository.AuditLogRepository) *AgentLogService {
	return &AgentLogService{db: db, repo: repo, auditRepo: auditRepo}
}

// SetNotifier 注入命令待办唤醒器（启动时装配；未注入则建命令后不主动唤醒，留待 agent 重连拉取或超时）。
func (s *AgentLogService) SetNotifier(n CommandNotifier) { s.notifier = n }

// SetApprovalService 注入实时日志审批入口。
func (s *AgentLogService) SetApprovalService(approval *ApprovalService) { s.approval = approval }

// SetSensitiveAccessGrants 注入日志正文的一次性访问授权服务。
func (s *AgentLogService) SetSensitiveAccessGrants(grants *SensitiveAccessGrantService) {
	s.grants = grants
}

// RequestTailLogs 由 admin 触发取某在线实例的自身日志：单活跃限速 → 事务内建 pending tail-logs 命令 + 审计 → 唤醒。
// 在线校验与 SSE 唤醒触发点在 handler 层（与反向抓取一致）。返回命令（含 id 供查询引用）。
func (s *AgentLogService) RequestTailLogs(_, _, _, _ string) (*model.AgentCommand, error) {
	return nil, apperr.ErrForbidden
}

// RequestTailLogsApproval 创建日志命令审批申请。
func (s *AgentLogService) RequestTailLogsApproval(ns, serverID, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil || ns == "" || serverID == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandTailLogs, Resource: "agent-command", ResourceID: ns + "/" + serverID, IdempotencyKey: idempotencyKey, Reason: reason}, map[string]any{"namespace": ns, "serverId": serverID, "operator": operator, "clientIP": clientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyRequestTailLogsInTx 仅由审批适配器在领域事务内创建日志命令。
func (s *AgentLogService) applyRequestTailLogsInTx(tx *gorm.DB, ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 单活跃限速（ADR-0040 决策4）：该实例已有进行中（pending/fetched）的取日志命令则拒，避免刷命令压垮 agent。
	active, err := s.repo.CountActiveByType(ns, serverID, model.CommandTypeTailLogs)
	if err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, apperr.ErrAgentLogActive
	}
	cmd := &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeTailLogs, Payload: "{}",
		Status: model.CommandStatusPending, Operator: operator,
	}
	if e := s.repo.WithTx(tx).Create(cmd); e != nil {
		return nil, e
	}
	if e := s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: ns,
		Operator:      operator, Action: model.ActionInstanceTailLogs,
		TargetType: model.TargetTypeInstance, TargetRef: serverID,
		Detail: fmt.Sprintf(`{"commandId":%d,"serverId":%q}`, cmd.ID, serverID),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		return nil, e
	}
	return cmd, nil
}

// ReceiveLogs 接收 agent 回传的自身日志快照并转存为命令瞬态（FR-88）。
// 命令须存在、type=tail-logs 且处 fetched；把脱敏日志行序列化存 log_content 并 CAS done。
// lines 为已脱敏文本（agent 侧落缓冲即脱敏），控制面不再处理原文。
func (s *AgentLogService) ReceiveLogs(commandID uint, lines []AgentLogLine, clientIP string) error {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeTailLogs {
		return apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return apperr.ErrCommandNotFound // 已完成 / 过期 / 未拉取，均不可回传
	}
	// 序列化脱敏日志行存瞬态（log_content）；nil 归一为空数组保证合法 JSON。
	if lines == nil {
		lines = []AgentLogLine{}
	}
	content, _ := json.Marshal(lines)
	ok, err := s.repo.UpdateStatusWithLogContent(cmd.ID, string(content))
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrCommandNotFound // 被并发终结（前态不符）
	}
	if s.grants != nil {
		hash := fmt.Sprintf("%x", sha256.Sum256(content))
		if err := s.grants.BindAndActivatePendingCommandFromAgent(cmd.ID, authz.OperationAgentCommandTailLogs, hash, time.Now().UTC()); err != nil {
			return err
		}
	}
	slog.Info("收到 agent 日志回传", "commandId", cmd.ID, "lines", len(lines), "clientIp", clientIP)
	return nil
}

// ConsumeApprovedLogs 仅允许原申请主体一次取得已由 Agent 回传的脱敏日志。
func (s *AgentLogService) ConsumeApprovedLogs(grantID string, commandID uint, principal auth.Principal) (*AgentLogResult, error) {
	if s == nil || s.grants == nil || grantID == "" || commandID == 0 {
		return nil, apperr.ErrForbidden
	}
	cmd, err := s.repo.FindByID(commandID)
	if err != nil || cmd == nil || cmd.Type != model.CommandTypeTailLogs || cmd.Status != model.CommandStatusDone {
		return nil, apperr.ErrForbidden
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(cmd.LogContent)))
	if err := s.grants.Consume(grantID, principal, authz.OperationAgentCommandTailLogs, fmt.Sprintf("agent-command/%d", commandID), hash, time.Now().UTC()); err != nil {
		return nil, err
	}
	var lines []AgentLogLine
	if json.Unmarshal([]byte(cmd.LogContent), &lines) != nil {
		return nil, apperr.ErrForbidden
	}
	return &AgentLogResult{CommandID: cmd.ID, Status: cmd.Status, Lines: lines}, nil
}

// GetLatest 取某实例最近一条 tail-logs 命令的状态 + 日志（FR-88，供 admin 查询）。
// 无任何取日志命令返回 (nil, nil)；done 则解出脱敏日志行，其余状态 lines 为空（前端据 status 显示进行中 / 失败）。
// 只读不清：done 命令的瞬态日志可重复读，不在读时抹除；未终结命令的瞬态由清理器超时过期时清空。
func (s *AgentLogService) GetLatest(ns, serverID string) (*AgentLogResult, error) {
	if ns == "" || serverID == "" {
		return nil, apperr.ErrInvalidParam
	}
	cmd, err := s.repo.FindLatestByType(ns, serverID, model.CommandTypeTailLogs)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, nil
	}
	res := &AgentLogResult{CommandID: cmd.ID, Status: cmd.Status, Lines: []AgentLogLine{}}
	if cmd.Status == model.CommandStatusDone && cmd.LogContent != "" {
		var lines []AgentLogLine
		if e := json.Unmarshal([]byte(cmd.LogContent), &lines); e == nil {
			res.Lines = lines
		}
	}
	return res, nil
}

package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// agentLogContentTTL 是日志正文在库内的保留时长，数值与浏览正文（browseResultTTL）一样是 5 分钟。
// 刻意写成独立常量而不是引用 browseResultTTL：两者语义不同、独立演进，
// 引用会让「为浏览 UX 调整 browseResultTTL」静默改掉日志保留期。
const agentLogContentTTL = 5 * time.Minute

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
	db           *gorm.DB
	repo         *repository.AgentCommandRepository
	auditRepo    *repository.AuditLogRepository
	notifier     CommandNotifier
	approval     *ApprovalService
	grants       *SensitiveAccessGrantService
	activeLockMu sync.Mutex
	activeLocks  map[string]*agentLogTargetLock
}

type agentLogTargetLock struct {
	mu         sync.Mutex
	references int
}

func (s *AgentLogService) lockTarget(ns, serverID string) func() {
	key := ns + "\x00" + serverID
	s.activeLockMu.Lock()
	if s.activeLocks == nil {
		s.activeLocks = make(map[string]*agentLogTargetLock)
	}
	target := s.activeLocks[key]
	if target == nil {
		target = &agentLogTargetLock{}
		s.activeLocks[key] = target
	}
	target.references++
	s.activeLockMu.Unlock()

	target.mu.Lock()
	return func() {
		target.mu.Unlock()
		s.activeLockMu.Lock()
		target.references--
		if target.references == 0 {
			delete(s.activeLocks, key)
		}
		s.activeLockMu.Unlock()
	}
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

// RequestTailLogs 直接创建日志读取命令并写专项审计。
func (s *AgentLogService) RequestTailLogs(ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	if s == nil || s.db == nil {
		return nil, apperr.ErrInternal
	}
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 限速锁必须在事务之外持有：锁若在事务回调内获取，defer unlock 会在**提交之前**执行，
	// 下一个持有者用自己的事务仍读不到未提交行，单活跃限速形同失效（并发可重复建命令）；
	// 同时先加锁再开事务，等待中的 goroutine 不会各自占住一个数据库连接阻塞等锁。
	unlock := s.lockTarget(ns, serverID)
	defer unlock()
	var cmd *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cmd, err = s.createTailLogsCommandInTx(tx, ns, serverID, operator, clientIP)
		return err
	})
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		s.notifier.NotifyCommand(ns, serverID)
	}
	return cmd, nil
}

// RequestTailLogsApproval 创建日志命令审批申请。
//
// Deprecated: 日志正文改为直读（RequestTailLogs + ReadCommand）后本入口已无 handler 调用，
// 保留仅为兼容旧审批单的读取 / 回放；适配器注册保留使历史待执行审批仍可按原语义落地。
func (s *AgentLogService) RequestTailLogsApproval(ns, serverID, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil {
		// 装配缺失是服务端问题，不把故障甩给调用方（此前与参数错误混用同一 400）。
		return ApprovalTicketView{}, apperr.ErrInternal
	}
	if ns == "" || serverID == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandTailLogs, Resource: "agent-command", ResourceID: ns + "/" + serverID, IdempotencyKey: idempotencyKey, Reason: reason}, map[string]any{"namespace": ns, "serverId": serverID, "operator": operator, "clientIP": clientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyRequestTailLogsInTx 仅由审批适配器在领域事务内创建日志命令。
//
// Deprecated: 日志正文改为直读（RequestTailLogs）后不再有 handler 调用本入口，
// 保留仅为兼容旧审批单的读取 / 回放（已存在的待执行审批仍能按原语义落地命令）。
//
// 注意它只能在事务回调内部加锁（适配器跑在审批 worker 的事务里），因此解锁发生在提交之前，
// 单活跃限速对「同目标并发」不完全成立；审批 worker 是单 goroutine 串行执行，
// 实际不会并发进入本入口，故此处保持原样而不引入新风险。
func (s *AgentLogService) applyRequestTailLogsInTx(tx *gorm.DB, ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 按 namespace/server 串行化限速检查与命令创建，事务隔离本身不能消除并发竞态。
	unlock := s.lockTarget(ns, serverID)
	defer unlock()
	return s.createTailLogsCommandInTx(tx, ns, serverID, operator, clientIP)
}

// createTailLogsCommandInTx 是「限速检查 + 建命令 + 写审计」的无锁核心。
// 调用方必须已持有该 (namespace, serverID) 的目标锁，否则单活跃限速存在并发窗口。
func (s *AgentLogService) createTailLogsCommandInTx(tx *gorm.DB, ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	// 单活跃限速（ADR-0040 决策4）：该实例已有进行中（pending/fetched）的取日志命令则拒，避免刷命令压垮 agent。
	active, err := s.repo.WithTx(tx).CountActiveByType(ns, serverID, model.CommandTypeTailLogs)
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
		// 用 json.Marshal 而非 `%q` 拼接：serverId 是外部可控文本，Go 的 %q 对控制字符输出 `\x01`
		// 而 JSON 不允许 `\x` 转义，会静默写出非法 detail（issue #144）。
		Detail: auditJSON(map[string]any{"commandId": cmd.ID, "serverId": serverID}),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		return nil, e
	}
	return cmd, nil
}

// ReceiveLogs 接收 agent 回传的自身日志快照并转存为命令瞬态（FR-88）。
// 命令须存在、type=tail-logs 且处 fetched，且归属与回传 agent 身份一致；
// 把脱敏日志行序列化存 log_content 并 CAS done。
// lines 为已脱敏文本（agent 侧落缓冲即脱敏），控制面不再处理原文。
func (s *AgentLogService) ReceiveLogs(identity agentauth.Identity, commandID uint, lines []AgentLogLine, clientIP string) error {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeTailLogs {
		return apperr.ErrCommandNotFound
	}
	// 只允许发起该命令的那台 agent 回传，避免任一 agent 用猜测的 commandID 覆盖他人日志。
	if identity.Namespace == "" || identity.ServerID == "" || cmd.NamespaceCode != identity.Namespace || cmd.ServerID != identity.ServerID {
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
	ok, err := s.repo.UpdateStatusWithLogContent(cmd.ID, string(content), time.Now().UTC().Add(agentLogContentTTL))
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrCommandNotFound // 被并发终结（前态不符）
	}
	if s.grants != nil {
		hash := fmt.Sprintf("%x", sha256.Sum256(content))
		if err := s.grants.BindAndActivatePendingCommandFromAgentIfPresent(cmd.ID, authz.OperationAgentCommandTailLogs, hash, time.Now().UTC()); err != nil {
			return err
		}
	}
	slog.Info("收到 agent 日志回传", "commandId", cmd.ID, "lines", len(lines), "clientIp", clientIP)
	return nil
}

// ConsumeApprovedLogs 仅允许原申请主体一次取得已由 Agent 回传的脱敏日志。
//
// Deprecated: 正文改为直读（ReadCommand）后本入口已无 handler 调用，保留仅为兼容旧授权单的读取 / 回放。
func (s *AgentLogService) ConsumeApprovedLogs(grantID string, commandID uint, principal auth.Principal) (*AgentLogResult, error) {
	if s == nil || s.grants == nil || grantID == "" || commandID == 0 {
		return nil, apperr.ErrForbidden
	}
	cmd, err := s.repo.FindByID(commandID)
	if err != nil || cmd == nil || cmd.Type != model.CommandTypeTailLogs || cmd.Status != model.CommandStatusDone {
		return nil, apperr.ErrForbidden
	}
	// 旧授权单同样受正文独立到期时间约束：否则存量授权会绕过新 TTL 把过期正文读出去。
	if !logContentReadable(cmd, time.Now().UTC()) {
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

// logContentReadable 判断命令的日志正文此刻是否仍可返回。
//
// done 之后正文按 agentLogContentTTL 到期作废：清扫器周期与 TTL 同量级，过期正文最坏还能多读一整个周期，
// 清扫失败时更是永久可读，所以读路径必须自己兜一次，不能只依赖清扫（issue #145）。
// 到期时间列为空视为无期限，兼容本次改造前的历史行——它们由清扫器按「无期限」一并回收。
func logContentReadable(cmd *model.AgentCommand, now time.Time) bool {
	if cmd == nil || cmd.Status != model.CommandStatusDone || cmd.LogContent == "" {
		return false
	}
	return cmd.LogContentExpiresAt == nil || cmd.LogContentExpiresAt.After(now)
}

// buildAgentLogResult 由命令行组装查询结果，并回报「正文是否真的返回」。
//
// 第二个返回值专供读审计判断：正文因过期 / 已清空而没返回时，本次读取并未交出内容，
// 不能再写「成功读取正文」审计（那会让审计看起来像又泄露了一次正文）。
// 落库内容损坏仍返回 error 而不是降级成空日志：静默降级会把「解码失败」伪装成「没有日志」，
// 既掩盖写入缺陷，也会写出一条与事实不符的成功读取审计。
func buildAgentLogResult(cmd *model.AgentCommand, now time.Time) (*AgentLogResult, bool, error) {
	res := &AgentLogResult{CommandID: cmd.ID, Status: cmd.Status, Lines: []AgentLogLine{}}
	if !logContentReadable(cmd, now) {
		return res, false, nil
	}
	var lines []AgentLogLine
	if err := json.Unmarshal([]byte(cmd.LogContent), &lines); err != nil {
		return nil, false, err
	}
	if lines != nil {
		res.Lines = lines
	}
	return res, true, nil
}

// ReadLatest 返回最近日志并先写仅含状态与行数的专项审计。
// done 但正文已过期 / 已清空时不写「成功读取正文」审计（本次没有交出任何内容）。
func (s *AgentLogService) ReadLatest(ns, serverID, operator, clientIP string) (*AgentLogResult, error) {
	if ns == "" || serverID == "" {
		return nil, apperr.ErrInvalidParam
	}
	cmd, err := s.repo.FindLatestByType(ns, serverID, model.CommandTypeTailLogs)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		result := &AgentLogResult{Lines: []AgentLogLine{}}
		if err := s.recordReadAudit(ns, serverID, operator, clientIP, result); err != nil {
			return nil, err
		}
		return result, nil
	}
	result, served, err := buildAgentLogResult(cmd, time.Now().UTC())
	if err != nil {
		// 与 GetLatest 的既有口径对齐：正文损坏属服务端内部问题，不把 json 语法错误原文
		// 透给调用方（其它读路径同样归一到稳定的错误码）。
		return nil, apperr.ErrInternal
	}
	// 未终结命令（pending/fetched/failed）仍记审计：这时的读取是「查状态」，前端据此显示进行中 / 失败。
	// 只有 done 且正文不可返回才跳过——那种情况没有正文流出，不该留「读过正文」的痕迹。
	if cmd.Status != model.CommandStatusDone || served {
		if err := s.recordReadAudit(ns, serverID, operator, clientIP, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ReadCommand 返回指定命令的日志，不依赖 grant，审计只记录命令状态和行数。
// namespace 为读请求的命名空间作用域（可为空，空则按 serverID 反查）；与命令归属不一致时按不存在拒绝。
func (s *AgentLogService) ReadCommand(namespace, serverID string, commandID uint, operator, clientIP string) (*AgentLogResult, error) {
	if serverID == "" || commandID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return nil, err
	}
	if cmd == nil || cmd.ServerID != serverID || cmd.Type != model.CommandTypeTailLogs {
		return nil, apperr.ErrCommandNotFound
	}
	// namespace 作用域校验：commandID 可枚举，只比对裸 serverId 会让同名实例跨环境互相读到日志正文。
	scopeNamespace, err := resolveCommandNamespaceScope(s.db, serverID, namespace)
	if err != nil {
		return nil, err
	}
	if cmd.NamespaceCode != scopeNamespace {
		return nil, apperr.ErrCommandNotFound
	}
	result, served, err := buildAgentLogResult(cmd, time.Now().UTC())
	if err != nil {
		return nil, apperr.ErrForbidden
	}
	// 与 ReadLatest 同口径：done 但正文不可返回时不写「成功读取正文」审计。
	if cmd.Status != model.CommandStatusDone || served {
		if err := s.recordReadAudit(cmd.NamespaceCode, serverID, operator, clientIP, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// recordReadAudit 记录一条「读取日志正文」审计，detail 只含命令状态与行数。
//
// 读路径用独立动作名 ActionInstanceTailLogsRead：与触发命令的 ActionInstanceTailLogs 分开，
// 否则审计页无法按 action 过滤「谁读过正文」（两者混在一起）。
func (s *AgentLogService) recordReadAudit(ns, serverID, operator, clientIP string, result *AgentLogResult) error {
	if s == nil || s.auditRepo == nil {
		return apperr.ErrInternal
	}
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	commandID := uint(0)
	status := "none"
	lineCount := 0
	if result != nil {
		commandID = result.CommandID
		status = result.Status
		lineCount = len(result.Lines)
	}
	// 用 json.Marshal 而非字符串拼接：serverID / status 都是外部可控文本，拼接易产出非法 JSON。
	detail, err := json.Marshal(map[string]any{
		"commandId": commandID, "serverId": serverID, "status": status, "lineCount": lineCount,
	})
	if err != nil {
		return err
	}
	return s.auditRepo.Create(&model.AuditLog{
		NamespaceCode: ns, Operator: operator, Action: model.ActionInstanceTailLogsRead,
		TargetType: model.TargetTypeInstance, TargetRef: serverID,
		Detail: string(detail),
		Result: model.ResultOK, ClientIP: clientIP,
	})
}

// ClearExpiredLogContents 清理所有已过独立到期时间的 done 日志正文（FR-88 / issue #145）。
//
// 挂在 AgentCommandService 上而不是 AgentLogService：与 ClearExpiredBrowseResults 成对，
// 两者共用同一个 CommandSweeper 实例（sweeper 的窄接口只认实现者）。
// done 是终态，ExpireStale 永远收敛不到它，因此成功回传的日志正文只能靠本函数回收。
func (s *AgentCommandService) ClearExpiredLogContents(now time.Time) (int64, error) {
	if s == nil || s.db == nil || s.repo == nil {
		return 0, apperr.ErrForbidden
	}
	return s.repo.ClearExpiredLogContents(now.UTC())
}

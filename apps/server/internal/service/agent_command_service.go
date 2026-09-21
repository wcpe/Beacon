package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// reverseFetchComment 是反向抓取 ingest 落盘的版本 / 审计注释。
const reverseFetchComment = "在线实例反向抓取"

// ingestPayload 是 ingest-plugins 命令的载荷（落 agent_command.payload JSON）。
// FR-39 落库：落 group 或 server 覆盖层（group 层只需 Group；server 层需 Group + Target）。
// FR-46 拓印：Mode=imprint + Path（目标文件相对 path）；回传后取该 path 转存待审，不落库（落层由确认时再选）。
// FR-58 两段式：Mode=scan（agent 只列元信息清单）/ submit（agent 仅读 SelectedPaths 子集内容回传）。
type ingestPayload struct {
	Scope  string `json:"scope,omitempty"`
	Group  string `json:"group,omitempty"`
	Target string `json:"target,omitempty"`
	// 模式（FR-46 / FR-58）：空 / land = 直接落库（FR-39）；imprint = 拓印转存待审；scan / submit = 受管任务两段式。
	Mode string `json:"mode,omitempty"`
	// 拓印目标文件相对 path（FR-46，仅 imprint 模式）。
	Path string `json:"path,omitempty"`
	// 提交选定 path 子集（FR-58，仅 submit 模式）：agent 仅读取并回传这些 path 的内容。
	SelectedPaths []string `json:"selectedPaths,omitempty"`
}

// CommandNotifier 是命令待办唤醒的窄接口（由 ChangeNotifier 实现，可选注入；
// 未注入即建命令后不主动唤醒，命令留待 agent 重连时拉取或超时清理）。
type CommandNotifier interface {
	NotifyCommand(ns, serverID string)
}

// submitIngestReceiver 是受管任务 submit 回传的窄接口（由 ReverseFetchTaskService 实现，可选注入）。
// agent 复用同一 /files/ingest 端点回传 submit 选定内容，控制面据命令 mode=submit 转交受管任务编排落库。
type submitIngestReceiver interface {
	ReceiveSubmitIngest(commandID uint, files []ImportFile, clientIP string) (*ImportResult, error)
}

// AgentCommandService 编排 server→agent 命令（FR-39，见 ADR-0027）。
// 本期唯一命令 ingest-plugins：admin 触发→建 pending 命令 + file.reverse-fetch 审计；
// agent 拉取→CAS fetched；agent 回传→控制面再校验（上限 / 排除 jar / path）→复用
// FileService.Import 落组覆盖（事务内 + file.import 审计）→CAS done；任一步失败 CAS failed。
type AgentCommandService struct {
	db        *gorm.DB
	repo      *repository.AgentCommandRepository
	fileSvc   *FileService
	auditRepo *repository.AuditLogRepository
	notifier  CommandNotifier
	// 拓印 diff 解期望合并值用（FR-46，可选注入；未注入则 ImprintDiff/确认审批不可用）。
	effSvc *FileEffectiveService
	// 受管任务 submit 回传转交（FR-58，可选注入；未注入则 submit 命令回传按未知 mode 拒）。
	submitReceiver submitIngestReceiver
	approval       *ApprovalService
	grants         *SensitiveAccessGrantService
}

// SetApprovalService 注入命令危险操作的统一审批入口。
func (s *AgentCommandService) SetApprovalService(approval *ApprovalService) { s.approval = approval }

// SetSensitiveAccessGrants 注入拓印正文的一次性访问授权服务。
func (s *AgentCommandService) SetSensitiveAccessGrants(grants *SensitiveAccessGrantService) {
	s.grants = grants
}

// NewAgentCommandService 构造服务。
func NewAgentCommandService(db *gorm.DB, repo *repository.AgentCommandRepository, fileSvc *FileService, auditRepo *repository.AuditLogRepository) *AgentCommandService {
	return &AgentCommandService{db: db, repo: repo, fileSvc: fileSvc, auditRepo: auditRepo}
}

// SetNotifier 注入命令待办唤醒器（启动时装配；未注入则建命令后不主动唤醒）。
func (s *AgentCommandService) SetNotifier(n CommandNotifier) { s.notifier = n }

// SetFileEffectiveService 注入有效文件树解析器（FR-46 拓印 diff 取期望合并值；启动时装配）。
func (s *AgentCommandService) SetFileEffectiveService(eff *FileEffectiveService) { s.effSvc = eff }

// SetSubmitIngestReceiver 注入受管任务 submit 回传转交器（FR-58；启动时装配）。
func (s *AgentCommandService) SetSubmitIngestReceiver(r submitIngestReceiver) { s.submitReceiver = r }

// RequestReverseFetch 由 admin 触发对某在线实例的反向抓取：事务内建 pending 命令 + file.reverse-fetch 审计。
// 在线校验与 SSE 唤醒在 handler/server 层。返回命令（含 id 供 agent 回传引用）。
func (s *AgentCommandService) RequestReverseFetch(_, _, _, _, _, _, _ string) (*model.AgentCommand, error) {
	return nil, apperr.ErrForbidden
}

// RequestReverseFetchApproval 创建反向扫描审批申请，命令仅由批准 worker 在同一事务中下发。
func (s *AgentCommandService) RequestReverseFetchApproval(ns, serverID, scope, group, target, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil || ns == "" || serverID == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := map[string]any{"namespace": ns, "serverId": serverID, "scope": scope, "group": group, "target": target, "operator": operator, "clientIP": clientIP}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandReverseScan, Resource: "agent-command", ResourceID: ns + "/" + serverID, IdempotencyKey: idempotencyKey, Reason: reason}, payload, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyRequestReverseFetchInTx 仅由审批适配器在领域事务内创建反向抓取命令。
func (s *AgentCommandService) applyRequestReverseFetchInTx(tx *gorm.DB, ns, serverID, scope, group, target, operator, clientIP string) (*model.AgentCommand, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 反向抓取只落 group / server 两层；空目标先挡，完整归一校验在落库时的 normalizeScope
	if scope != model.ScopeGroup && scope != model.ScopeServer {
		return nil, apperr.ErrInvalidScope
	}
	if group == "" || (scope == model.ScopeServer && target == "") {
		return nil, apperr.ErrInvalidScope
	}
	payload, _ := json.Marshal(ingestPayload{Scope: scope, Group: group, Target: target})
	cmd := &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeIngestPlugins, Payload: string(payload),
		Status: model.CommandStatusPending, Operator: operator,
	}
	if e := s.repo.WithTx(tx).Create(cmd); e != nil {
		return nil, e
	}
	if e := s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: ns,
		Operator:      operator, Action: model.ActionFileReverseFetch,
		TargetType: model.TargetTypeCommand, TargetRef: serverID,
		Detail: fmt.Sprintf(`{"commandId":%d,"scope":%q,"group":%q,"target":%q}`, cmd.ID, scope, group, target),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		return nil, e
	}
	return cmd, nil
}

// RequestResync 由 admin 触发对某在线实例的强制重同步（FR-91）：事务内建 pending resync-config 命令 + instance.resync 审计。
// 语义为「重拉控制面权威的有效配置/文件树/覆盖集并 apply」，无业务载荷（空 JSON），复用命令队列既有模式（见 ADR-0027）。
// 在线校验与 SSE 唤醒在 handler/server 层（与取日志一致）。返回命令（含 id 供 agent 回传结果引用）。
func (s *AgentCommandService) RequestResync(_, _, _, _ string) (*model.AgentCommand, error) {
	return nil, apperr.ErrForbidden
}

// RequestResyncApproval 创建强制重同步审批申请，命令仅在批准 worker 内下发。
func (s *AgentCommandService) RequestResyncApproval(ns, serverID, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	// 逐条区分：装配缺失是服务端问题（500），缺参数是调用方问题（400）。
	// 此前合并成 ErrForbidden，会让 MCP 调用方去查密钥权限，而实际是请求体漏字段。
	if s == nil || s.approval == nil {
		return ApprovalTicketView{}, apperr.ErrInternal
	}
	if ns == "" || serverID == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	if strings.TrimSpace(reason) == "" {
		return ApprovalTicketView{}, apperr.ErrApprovalReasonRequired
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return ApprovalTicketView{}, apperr.ErrIdempotencyKeyRequired
	}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandResync, Resource: "agent-command", ResourceID: ns + "/" + serverID, IdempotencyKey: idempotencyKey, Reason: reason}, map[string]any{"namespace": ns, "serverId": serverID, "operator": operator, "clientIP": clientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

func (s *AgentCommandService) applyRequestResyncInTx(tx *gorm.DB, ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	cmd := &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeResyncConfig, Payload: "{}",
		Status: model.CommandStatusPending, Operator: operator,
	}
	if e := s.repo.WithTx(tx).Create(cmd); e != nil {
		return nil, e
	}
	// Create 后 cmd.ID 已回填，可入审计 detail（无敏感内容）。
	if e := s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: ns,
		Operator:      operator, Action: model.ActionInstanceResync,
		TargetType: model.TargetTypeInstance, TargetRef: serverID,
		Detail: fmt.Sprintf(`{"commandId":%d,"serverId":%q}`, cmd.ID, serverID),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		return nil, e
	}
	return cmd, nil
}

// applyRequestResyncForTest 仅供同包测试验证领域写入；生产路径必须经审批适配器调用 applyRequestResyncInTx。
func (s *AgentCommandService) applyRequestResyncForTest(ns, serverID, operator, clientIP string) (*model.AgentCommand, error) {
	var command *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		command, err = s.applyRequestResyncInTx(tx, ns, serverID, operator, clientIP)
		return err
	})
	return command, err
}

// ReceiveResyncResult 接收 agent 回传的强制重同步执行结果（FR-91）：命令须存在、type=resync-config 且处 fetched。
// ok=true 则 CAS done；否则 CAS failed 并记原因摘要（无敏感内容）。重同步无内容回传，仅推进命令生命周期。
func (s *AgentCommandService) ReceiveResyncResult(commandID uint, ok bool, reason string) error {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeResyncConfig {
		return apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	if err := s.ensureCommandActive(cmd); err != nil {
		return err
	}
	if ok {
		hit, e := s.repo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone, "")
		if e != nil {
			return e
		}
		if !hit {
			return apperr.ErrCommandNotFound // 被并发终结（前态不符）
		}
		slog.Info("收到强制重同步完成回传", "commandId", cmd.ID)
		return nil
	}
	s.markFailed(cmd.ID, reason)
	return nil
}

// ResultCommandType 返回命令结果回传所对应的类型；不存在或未拉取的命令统一按不存在处理。
func (s *AgentCommandService) ResultCommandType(commandID uint) (string, error) {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return "", err
	}
	if cmd == nil || cmd.Status != model.CommandStatusFetched {
		return "", apperr.ErrCommandNotFound
	}
	if err := s.ensureCommandActive(cmd); err != nil {
		return "", err
	}
	return cmd.Type, nil
}

// FetchPending 取某 agent 最早一条 pending 命令并 CAS 迁移 fetched（供 agent 拉取）。
// 生命周期资格检查、查询和 CAS 领取在同一事务中完成；无 pending 或被并发取走返回 (nil, nil)。
func (s *AgentCommandService) FetchPending(ns, serverID string) (*model.AgentCommand, error) {
	var claimed *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureServerActiveForNamespace(tx, ns, serverID); err != nil {
			return err
		}
		repo := s.repo.WithTx(tx)
		cmd, err := repo.FindOldestPending(ns, serverID)
		if err != nil || cmd == nil {
			return err
		}
		ok, err := repo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, "")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		cmd.Status = model.CommandStatusFetched
		claimed = cmd
		return nil
	})
	return claimed, err
}

// ReceiveIngest 接收 agent 回传文件集并 ingest（FR-39，见 ADR-0027）。
// 命令须存在且处 fetched；控制面入库前再校验（双保险：文件数 / 总量上限、排除 .jar；
// 单文件大小与 path 安全由 FileService.Import 兜底）→复用 Import 落组覆盖（事务内 + file.import 审计）
// →CAS done；校验 / ingest 失败 CAS failed + 原因摘要（无敏感内容）。
func (s *AgentCommandService) ReceiveIngest(commandID uint, files []ImportFile, clientIP string) (*ImportResult, error) {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return nil, err
	}
	if cmd == nil || cmd.Type != model.CommandTypeIngestPlugins {
		return nil, apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return nil, apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	if err := s.ensureCommandActive(cmd); err != nil {
		return nil, err
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil {
		s.markFailed(cmd.ID, "载荷不合法")
		return nil, apperr.ErrInvalidParam
	}
	// FR-46 拓印模式：agent 回传整棵 plugins 树、但只取目标单文件转存待审（CAS fetched→ready），不落库。
	// 不套 FR-39 的整批数量 / 总量闸（那是为整批落库设的，会误伤大插件目录下的单文件拓印）；
	// jar 排除与目标单文件大小由 transferImprint 兜底。返回 (nil, nil) 表示转存成功（无落库结果）。
	if payload.Mode == model.IngestModeImprint {
		if err := s.ensureCommandActive(cmd); err != nil {
			return nil, err
		}
		return nil, s.transferImprint(cmd, payload.Path, files)
	}
	// FR-58 受管任务 submit 模式：转交受管任务编排（按 task 的 scope/group/target 落库、迁移任务状态）。
	if payload.Mode == model.IngestModeSubmit {
		if err := s.ensureCommandActive(cmd); err != nil {
			return nil, err
		}
		if s.submitReceiver == nil {
			return nil, apperr.ErrInternal // 未装配（编程 / 装配错误）
		}
		return s.submitReceiver.ReceiveSubmitIngest(commandID, files, clientIP)
	}
	// FR-39 落库模式：整批入库前再校验（双保险）+ 需 group。
	if verr := validateIngestFiles(files); verr != nil {
		s.markFailed(cmd.ID, verr.Error())
		return nil, verr
	}
	if payload.Group == "" {
		s.markFailed(cmd.ID, "载荷不合法")
		return nil, apperr.ErrInvalidParam
	}
	if err := s.ensureCommandActive(cmd); err != nil {
		return nil, err
	}
	result, ierr := s.fileSvc.applyImport(ImportFilesParams{
		Namespace: cmd.NamespaceCode, Group: payload.Group,
		ScopeLevel: payload.Scope, ScopeTarget: payload.Target,
		Files: files, Operator: cmd.Operator, Comment: reverseFetchComment, ClientIP: clientIP,
	})
	if ierr != nil {
		s.markFailed(cmd.ID, ierr.Error())
		return nil, ierr
	}
	if _, e := s.repo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone,
		fmt.Sprintf(`{"files":%d,"created":%d,"updated":%d}`, len(files), result.Created, result.Updated)); e != nil {
		return nil, e
	}
	slog.Info("反向抓取 ingest 完成", "commandId", cmd.ID, "group", payload.Group,
		"files", len(files), "created", result.Created, "updated", result.Updated)
	return result, nil
}

// ExpireStale 把陈旧（pending/fetched 超时）命令标 expired；由后台周期触发。
func (s *AgentCommandService) ExpireStale(before time.Time) (int64, error) {
	return s.repo.ExpireStale(before)
}

func (s *AgentCommandService) ensureCommandActive(cmd *model.AgentCommand) error {
	if cmd == nil {
		return apperr.ErrCommandNotFound
	}
	if err := ensureServerActiveForNamespace(s.db, cmd.NamespaceCode, cmd.ServerID); err != nil {
		if errors.Is(err, apperr.ErrServerArchived) {
			return apperr.ErrCommandNotFound
		}
		return err
	}
	return nil
}

// markFailed 把命令从 fetched 迁移 failed（best-effort，CAS 失败仅告警）。
func (s *AgentCommandService) markFailed(id uint, reason string) {
	if _, e := s.repo.UpdateStatus(id, model.CommandStatusFetched, model.CommandStatusFailed,
		fmt.Sprintf(`{"error":%q}`, reason)); e != nil {
		slog.Warn("标记命令失败态出错", "commandId", id, "原因", e)
	}
}

// validateIngestFiles 控制面入库前对回传文件集再校验（ADR-0027 双保险）：
// 非空、文件数 / 总量上限、排除 .jar（单文件大小与 path 安全交 Import 兜底）。
func validateIngestFiles(files []ImportFile) error {
	if len(files) == 0 {
		return apperr.ErrInvalidParam
	}
	if len(files) > MaxImportFiles {
		return apperr.ErrTooManyFiles
	}
	var total int64
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".jar") {
			return apperr.ErrInvalidPath // 排除 jar（沿 ADR-0011，jar 属 P3 发布编排、非托管配置）
		}
		total += int64(len(f.Content))
	}
	if total > MaxImportTotalBytes {
		return apperr.ErrContentTooLarge
	}
	return nil
}

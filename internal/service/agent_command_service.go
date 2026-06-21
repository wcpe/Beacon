package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// reverseFetchComment 是反向抓取 ingest 落盘的版本 / 审计注释。
const reverseFetchComment = "在线实例反向抓取"

// ingestPayload 是 ingest-plugins 命令的载荷（落 agent_command.payload JSON）。
// 落 group 或 server 覆盖层：group 层只需 Group；server 层需 Group + Target（目标 serverId）。
type ingestPayload struct {
	Scope  string `json:"scope"`
	Group  string `json:"group"`
	Target string `json:"target,omitempty"`
}

// CommandNotifier 是命令待办唤醒的窄接口（由 ChangeNotifier 实现，可选注入；
// 未注入即建命令后不主动唤醒，命令留待 agent 重连时拉取或超时清理）。
type CommandNotifier interface {
	NotifyCommand(ns, serverID string)
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
}

// NewAgentCommandService 构造服务。
func NewAgentCommandService(db *gorm.DB, repo *repository.AgentCommandRepository, fileSvc *FileService, auditRepo *repository.AuditLogRepository) *AgentCommandService {
	return &AgentCommandService{db: db, repo: repo, fileSvc: fileSvc, auditRepo: auditRepo}
}

// SetNotifier 注入命令待办唤醒器（启动时装配；未注入则建命令后不主动唤醒）。
func (s *AgentCommandService) SetNotifier(n CommandNotifier) { s.notifier = n }

// RequestReverseFetch 由 admin 触发对某在线实例的反向抓取：事务内建 pending 命令 + file.reverse-fetch 审计。
// 在线校验与 SSE 唤醒在 handler/server 层。返回命令（含 id 供 agent 回传引用）。
func (s *AgentCommandService) RequestReverseFetch(ns, serverID, scope, group, target, operator, clientIP string) (*model.AgentCommand, error) {
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
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
		// Create 后 cmd.ID 已回填，可入审计 detail（无敏感内容）。
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns,
			Operator:      operator, Action: model.ActionFileReverseFetch,
			TargetType: model.TargetTypeCommand, TargetRef: serverID,
			Detail: fmt.Sprintf(`{"commandId":%d,"scope":%q,"group":%q,"target":%q}`, cmd.ID, scope, group, target),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	// 提交成功后唤醒该 agent 的 SSE 流发 command-pending（agent 离线则无 waiter、留待重连拉取或超时）。
	if s.notifier != nil {
		s.notifier.NotifyCommand(ns, serverID)
	}
	slog.Info("触发在线实例反向抓取", "namespace", ns, "serverId", serverID, "scope", scope, "group", group,
		"target", target, "commandId", cmd.ID, "operator", operator)
	return cmd, nil
}

// FetchPending 取某 agent 最早一条 pending 命令并 CAS 迁移 fetched（供 agent 拉取）。
// 无 pending 或被并发取走返回 (nil, nil)。
func (s *AgentCommandService) FetchPending(ns, serverID string) (*model.AgentCommand, error) {
	cmd, err := s.repo.FindOldestPending(ns, serverID)
	if err != nil || cmd == nil {
		return nil, err
	}
	ok, err := s.repo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, "")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil // 被并发拉走，本次让出
	}
	cmd.Status = model.CommandStatusFetched
	return cmd, nil
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
	if verr := validateIngestFiles(files); verr != nil {
		s.markFailed(cmd.ID, verr.Error())
		return nil, verr
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.Group == "" {
		s.markFailed(cmd.ID, "载荷不合法")
		return nil, apperr.ErrInvalidParam
	}
	result, ierr := s.fileSvc.Import(ImportFilesParams{
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

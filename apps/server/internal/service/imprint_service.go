package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/filetree"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// imprintComment 是拓印确认落库的版本 / 审计注释（FR-46）。
const imprintComment = "按需拓印回写"

// ImprintDiffResult 是拓印 diff 结果：本地实际值（命令转存的磁盘原文）⟷ 期望合并值（FR-45 解析）。
type ImprintDiffResult struct {
	Path string
	// 本地实际值（agent 回传、命令转存的磁盘内容）+ md5（确认时回带作自审凭据）
	ActualContent string
	ActualMD5     string
	// 期望合并值（拓印源 server 视角对该 path 的覆盖链合并结果）+ md5
	ExpectedContent string
	ExpectedMD5     string
	// 期望合并值是否整文件覆盖模式（结构化深合并为 false）
	ExpectedWholeFile bool
	// 期望合并值逐键 / 整文件来源（复用 FR-45 provenance，前端来源徽标）
	ExpectedSources []merge.KeyProvenance
	// 期望侧被减量删除的键（结构化）
	ExpectedDeletions []merge.KeyProvenance
	// 本地实际值与期望合并值是否有差异（按 md5 比对）
	Differs bool
}

// ImprintConfirmResult 概述拓印确认落库结果（落到哪层、版本、md5）。
type ImprintConfirmResult struct {
	FileID     uint
	ScopeLevel string
	Group      string
	Target     string
	Version    int64
	MD5        string
}

type imprintConfirmApprovalPayload struct {
	CommandID   uint   `json:"commandId"`
	Namespace   string `json:"namespace"`
	ServerID    string `json:"serverId"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Group       string `json:"group"`
	Zone        string `json:"zone"`
	Target      string `json:"target"`
	ReviewedMD5 string `json:"reviewedMd5"`
	Operator    string `json:"operator"`
	ClientIP    string `json:"clientIP"`
}

// RequestImprint 由 admin 触发对某在线实例某文件的按需拓印（FR-46）：事务内建 pending 命令
// （载荷 mode=imprint + path）+ file.imprint-fetch 审计；提交后唤醒该 agent SSE（agent 仍读整棵
// plugins 树回传，落库 vs 转存由 mode 区分，agent 零改动）。在线校验与 SSE 唤醒口径同 RequestReverseFetch。
func (s *AgentCommandService) RequestImprint(_, _, _, _, _ string) (*model.AgentCommand, error) {
	return nil, apperr.ErrForbidden
}

// RequestImprintApproval 创建拓印审批申请，Agent 命令与内容访问授权由批准事务一并生成。
func (s *AgentCommandService) RequestImprintApproval(ns, serverID, filePath, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil || ns == "" || serverID == "" || filePath == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	cleanPath, err := normalizePath(filePath)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandImprint, Resource: "agent-command", ResourceID: ns + "/" + serverID + "/" + cleanPath, IdempotencyKey: idempotencyKey, Reason: reason}, map[string]any{"namespace": ns, "serverId": serverID, "path": cleanPath, "operator": operator, "clientIP": clientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyRequestImprintInTx 仅由审批适配器在领域事务内创建拓印命令。
func (s *AgentCommandService) applyRequestImprintInTx(tx *gorm.DB, ns, serverID, filePath, operator, clientIP string) (*model.AgentCommand, error) {
	if ns == "" || serverID == "" || operator == "" || filePath == "" {
		return nil, apperr.ErrInvalidParam
	}
	cleanPath, err := normalizePath(filePath)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(ingestPayload{Mode: model.IngestModeImprint, Path: cleanPath})
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
		Operator:      operator, Action: model.ActionFileImprintFetch,
		TargetType: model.TargetTypeCommand, TargetRef: serverID,
		Detail: fmt.Sprintf(`{"commandId":%d,"path":%q}`, cmd.ID, cleanPath),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		return nil, e
	}
	return cmd, nil
}

// transferImprint 处理拓印回传（mode=imprint）：从回传集取目标 path 转存命令、CAS fetched→ready，不落库。
// 目标 path 不在回传树中（磁盘上无此文件）→ 命令 failed。返回 nil 表示转存成功。
func (s *AgentCommandService) transferImprint(cmd *model.AgentCommand, targetPath string, files []ImportFile) error {
	clean, err := normalizePath(targetPath)
	if err != nil {
		s.markFailed(cmd.ID, "载荷 path 不合法")
		return apperr.ErrInvalidParam
	}
	var content string
	found := false
	for _, f := range files {
		fp, perr := normalizePath(f.Path)
		if perr == nil && fp == clean {
			content, found = f.Content, true
			break
		}
	}
	if !found {
		s.markFailed(cmd.ID, "目标文件不在回传集（磁盘上不存在）")
		return apperr.ErrFileNotFound
	}
	// 拓印只取单文件、跳过了 FR-39 整批闸，故在此对目标单文件兜底：排除 jar（沿 ADR-0011）、限单文件大小。
	if strings.HasSuffix(strings.ToLower(clean), ".jar") {
		s.markFailed(cmd.ID, "拓印目标不能是 jar")
		return apperr.ErrInvalidPath
	}
	if len(content) > MaxFileContentBytes {
		s.markFailed(cmd.ID, "拓印目标文件超单文件大小上限")
		return apperr.ErrContentTooLarge
	}
	ok, err := s.repo.UpdateImprintReady(cmd.ID, content)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrCommandNotFound // 并发已迁移，非 fetched
	}
	if s.grants != nil {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if err := s.grants.BindAndActivatePendingCommandFromAgent(cmd.ID, authz.OperationAgentCommandImprint, hash, time.Now().UTC()); err != nil {
			return err
		}
	}
	slog.Info("拓印回传转存待审", "commandId", cmd.ID, "path", clean, "bytes", len(content))
	return nil
}

// ConsumeApprovedImprint 仅允许原申请主体一次查看已回传拓印正文。
func (s *AgentCommandService) ConsumeApprovedImprint(grantID string, commandID uint, scope, group, zone string, principal auth.Principal) (*ImprintDiffResult, error) {
	if s == nil || s.grants == nil || grantID == "" || commandID == 0 {
		return nil, apperr.ErrForbidden
	}
	cmd, _, err := s.requireReadyImprint(commandID)
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(cmd.ImprintContent)))
	if err := s.grants.Consume(grantID, principal, authz.OperationAgentCommandImprint, fmt.Sprintf("agent-command/%d", commandID), hash, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.ImprintDiff(commandID, scope, group, zone)
}

// ImprintDiff 取拓印 diff（FR-46）：命令须 ready 且 imprint 模式；本地实际值 = 命令转存内容，
// 期望合并值经 FileEffectiveService.ResolveWithProvenance 解出该 path 的覆盖链合并结果（复用 FR-45）。
// 期望恒为「拓印源 server 的有效文件树视角」：源服已指派 zone 时按其 zone_assignment 解 (group,zone)、
// 忽略入参 hint；未指派时以 admin 选定的并入层 group/zone 作兜底 hint（拓印源常尚未指派，需 admin 选的层
// 才能解出大区/小区层）。并入层 scope/group/zone 只决定「确认落库去向」，不改变期望视角恒为源服这一事实。
// 不取 target 形参：期望视角由源服身份（命令）+ hint 决定，与确认落库的目标键无关。
func (s *AgentCommandService) ImprintDiff(commandID uint, scope, group, zone string) (*ImprintDiffResult, error) {
	cmd, payload, err := s.requireReadyImprint(commandID)
	if err != nil {
		return nil, err
	}
	if s.effSvc == nil {
		return nil, apperr.ErrInternal
	}
	// 并入 global 层时不取 group/zone 兜底 hint（global 不挂在具体大区/小区下）；其余层用 admin 选定 group/zone 作兜底。
	// 注意：hint 仅对「未指派 zone 的源服」生效——已指派源服一律按其 zone_assignment 解、hint 被忽略（期望恒为源服视角）。
	groupHint, zoneHint := group, zone
	if scope == model.ScopeGlobal {
		groupHint, zoneHint = "", ""
	}
	tree, err := s.effSvc.ResolveWithProvenance(cmd.NamespaceCode, cmd.ServerID, groupHint, zoneHint)
	if err != nil {
		return nil, err
	}
	actualMD5 := filetree.ContentMD5(cmd.ImprintContent)
	res := &ImprintDiffResult{
		Path:          payload.Path,
		ActualContent: cmd.ImprintContent, ActualMD5: actualMD5,
	}
	for _, f := range tree.Files {
		if f.Path == payload.Path {
			res.ExpectedContent, res.ExpectedMD5 = f.Content, f.MD5
			res.ExpectedWholeFile = f.WholeFile
			res.ExpectedSources, res.ExpectedDeletions = f.Sources, f.Deletions
			break
		}
	}
	// 期望侧无该 path（该 server 当前有效文件树不含它）→ 期望为空、必判有差异。
	res.Differs = res.ActualMD5 != res.ExpectedMD5
	return res, nil
}

// ConfirmImprint 已停用，避免 handler、后台代码或测试外输入直接绕过审批 worker 落库。
func (s *AgentCommandService) ConfirmImprint(uint, string, string, string, string, string, string, string) (*ImprintConfirmResult, error) {
	return nil, apperr.ErrForbidden
}

// RequestImprintConfirmApproval 创建拓印确认审批；冻结来源命令、目标层与已查看内容的 md5，不保存文件正文。
func (s *AgentCommandService) RequestImprintConfirmApproval(commandID uint, scope, group, zone, target, reviewedMD5, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil || commandID == 0 || operator == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	cmd, payload, err := s.requireReadyImprint(commandID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	if reviewedMD5 == "" || reviewedMD5 != filetree.ContentMD5(cmd.ImprintContent) {
		return ApprovalTicketView{}, apperr.ErrImprintReviewMismatch
	}
	normGroup, normTarget, err := normalizeImprintScope(scope, group, zone, target, cmd.ServerID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	frozen := imprintConfirmApprovalPayload{CommandID: cmd.ID, Namespace: cmd.NamespaceCode, ServerID: cmd.ServerID, Path: payload.Path,
		Scope: scope, Group: normGroup, Zone: zone, Target: normTarget, ReviewedMD5: reviewedMD5, Operator: operator, ClientIP: clientIP}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandImprintConfirm, Resource: "agent-command", ResourceID: fmt.Sprint(cmd.ID),
		IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: fmt.Sprintf("command=%d,status=ready,contentMd5=%s", cmd.ID, reviewedMD5),
		ImpactSummary: fmt.Sprintf("拓印内容将写入 %s/%s@%s:%s", cmd.NamespaceCode, payload.Path, scope, normTarget)},
		map[string]any{"commandId": frozen.CommandID, "namespace": frozen.Namespace, "serverId": frozen.ServerID, "path": frozen.Path,
			"scope": frozen.Scope, "group": frozen.Group, "zone": frozen.Zone, "target": frozen.Target, "reviewedMd5": frozen.ReviewedMD5,
			"operator": frozen.Operator, "clientIP": frozen.ClientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyConfirmImprintInTx 仅在审批许可绑定校验通过后写文件、命令终态、审计与执行回执。
func (s *AgentCommandService) applyConfirmImprintInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit, input imprintConfirmApprovalPayload) (*ImprintConfirmResult, error) {
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, err
	}
	if tx == nil || s.fileSvc == nil || input.CommandID == 0 || input.Operator == "" {
		return nil, apperr.ErrForbidden
	}
	cmd, payload, err := s.requireReadyImprintInTx(tx, input.CommandID)
	if err != nil {
		return nil, err
	}
	if cmd.NamespaceCode != input.Namespace || cmd.ServerID != input.ServerID || payload.Path != input.Path {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if input.ReviewedMD5 == "" || input.ReviewedMD5 != filetree.ContentMD5(cmd.ImprintContent) {
		return nil, apperr.ErrImprintReviewMismatch
	}
	normGroup, normTarget, err := normalizeImprintScope(input.Scope, input.Group, input.Zone, input.Target, cmd.ServerID)
	if err != nil {
		return nil, err
	}
	claimed, err := s.repo.WithTx(tx).UpdateStatusClearImprint(cmd.ID, model.CommandStatusReady, model.CommandStatusDone, fmt.Sprintf("{\"scope\":%q}", input.Scope))
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, apperr.ErrImprintNotReady
	}
	obj, err := s.landImprintInTx(tx, cmd.NamespaceCode, payload.Path, input.Scope, normGroup, normTarget, cmd.ImprintContent, input.Operator, input.ClientIP)
	if err != nil {
		return nil, err
	}
	if err := s.auditRepo.WithTx(tx).Create(&model.AuditLog{NamespaceCode: cmd.NamespaceCode, Operator: input.Operator, Action: model.ActionFileImprint,
		TargetType: model.TargetTypeFile, TargetRef: fmt.Sprintf("%s/%s/%s@%s:%s", cmd.NamespaceCode, normGroup, payload.Path, input.Scope, normTarget),
		Detail: fmt.Sprintf("{\"commandId\":%d,\"scope\":%q,\"version\":%d,\"md5\":%q}", cmd.ID, input.Scope, obj.Version, obj.ContentMD5),
		Result: model.ResultOK, ClientIP: input.ClientIP}); err != nil {
		return nil, err
	}
	return &ImprintConfirmResult{FileID: obj.ID, ScopeLevel: input.Scope, Group: normGroup, Target: normTarget, Version: obj.Version, MD5: obj.ContentMD5}, nil
}

func (s *AgentCommandService) requireReadyImprintInTx(tx *gorm.DB, commandID uint) (*model.AgentCommand, ingestPayload, error) {
	cmd, err := s.repo.WithTx(tx).FindByID(commandID)
	if err != nil {
		return nil, ingestPayload{}, err
	}
	if cmd == nil || cmd.Type != model.CommandTypeIngestPlugins || cmd.Status != model.CommandStatusReady {
		return nil, ingestPayload{}, apperr.ErrImprintNotReady
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.Mode != model.IngestModeImprint {
		return nil, ingestPayload{}, apperr.ErrCommandNotFound
	}
	return cmd, payload, nil
}

func normalizeImprintScope(scope, group, zone, target, sourceServerID string) (string, string, error) {
	scopeTarget := target
	if scope == model.ScopeZone {
		scopeTarget = zone
	}
	normGroup, normTarget, err := normalizeScope(scope, group, scopeTarget)
	if err != nil {
		return "", "", err
	}
	if scope == model.ScopeServer && normTarget != sourceServerID {
		return "", "", apperr.ErrInvalidScope
	}
	return normGroup, normTarget, nil
}

func (s *AgentCommandService) landImprintInTx(tx *gorm.DB, ns, filePath, scope, group, scopeTarget, content, operator, clientIP string) (*model.FileObject, error) {
	if err := validateFileContent(filePath, content); err != nil {
		return nil, err
	}
	files := s.fileSvc.fileRepo.WithTx(tx)
	existing, err := files.FindByIdentity(ns, groupForScope(scope, group), filePath, scope, scopeTarget)
	if err != nil {
		return nil, err
	}
	md5 := filetree.ContentMD5(content)
	if existing == nil {
		obj := &model.FileObject{NamespaceCode: ns, GroupCode: groupForScope(scope, group), Path: filePath, ScopeLevel: scope, ScopeTarget: scopeTarget,
			Content: content, ContentMD5: md5, Version: 1, Enabled: true}
		if err := files.Create(obj); err != nil {
			return nil, err
		}
		rev, err := s.fileSvc.appendRevision(tx, obj.ID, obj.Version, content, md5, nil, operator, imprintComment)
		if err != nil {
			return nil, err
		}
		obj.CurrentRevision = rev.ID
		if err := files.Save(obj); err != nil {
			return nil, err
		}
		if err := s.fileSvc.writeAudit(tx, obj, operator, model.ActionFileCreate, fmt.Sprintf("{\"version\":1,\"md5\":%q}", md5), clientIP); err != nil {
			return nil, err
		}
		return obj, nil
	}
	preVersion := existing.Version
	newVersion := preVersion + 1
	rev, err := s.fileSvc.appendRevision(tx, existing.ID, newVersion, content, md5, nil, operator, imprintComment)
	if err != nil {
		return nil, err
	}
	existing.Content, existing.ContentMD5, existing.Version, existing.CurrentRevision = content, md5, newVersion, rev.ID
	if err := files.Save(existing); err != nil {
		return nil, err
	}
	if err := s.fileSvc.writeAudit(tx, existing, operator, model.ActionFilePublish, fmt.Sprintf("{\"version\":%d,\"md5\":%q}", newVersion, md5), clientIP); err != nil {
		return nil, err
	}
	if err := s.fileSvc.recordReversible(tx, existing, preVersion, operator); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *AgentCommandService) afterConfirmImprint(result *ImprintConfirmResult, operator string) func() {
	return func() {
		obj, err := s.fileSvc.Get(result.FileID)
		if err != nil {
			slog.Warn("拓印提交后读取文件失败", "fileId", result.FileID, "原因", err)
			return
		}
		action := model.ActionFilePublish
		if result.Version == 1 {
			action = model.ActionFileCreate
		}
		s.fileSvc.notify(obj)
		s.fileSvc.exportGit(obj, action, operator)
		slog.Info("拓印确认落库", "fileId", result.FileID, "scope", result.ScopeLevel, "target", result.Target, "version", result.Version)
	}
}

// GetImprintCommand 取拓印命令（任意状态，供前端轮询命令状态至 ready，FR-46）。
// 须为 imprint 模式的 ingest-plugins 命令，否则 ErrCommandNotFound；不含瞬态内容字段供视图安全暴露。
func (s *AgentCommandService) GetImprintCommand(commandID uint) (*model.AgentCommand, error) {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return nil, err
	}
	if cmd == nil || cmd.Type != model.CommandTypeIngestPlugins {
		return nil, apperr.ErrCommandNotFound
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.Mode != model.IngestModeImprint {
		return nil, apperr.ErrCommandNotFound
	}
	return cmd, nil
}

// requireReadyImprint 取命令并校验其为 ready 态的 imprint 模式；返回命令与解析后的载荷。
func (s *AgentCommandService) requireReadyImprint(commandID uint) (*model.AgentCommand, ingestPayload, error) {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return nil, ingestPayload{}, err
	}
	if cmd == nil || cmd.Type != model.CommandTypeIngestPlugins {
		return nil, ingestPayload{}, apperr.ErrCommandNotFound
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.Mode != model.IngestModeImprint {
		return nil, ingestPayload{}, apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusReady {
		return nil, ingestPayload{}, apperr.ErrImprintNotReady
	}
	return cmd, payload, nil
}

// groupForScope 把 scope 归一为 file_object 行上的 group_code（global 用占位）。
func groupForScope(scope, group string) string {
	if scope == model.ScopeGlobal {
		return model.GlobalGroupCode
	}
	return group
}

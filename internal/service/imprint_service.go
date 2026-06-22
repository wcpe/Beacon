package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// imprintComment 是拓印确认落库的版本 / 审计注释（FR-46）。
const imprintComment = "按需拓印回写"

// confirmImprintBeforeClaimHook 是测试注入钩子：在「过自审门、归一并入层后、CAS 认领前」触发，
// 用于确定性复现并发双确认窗口（生产恒为 nil、零开销）。见 TestConfirmImprintClaimBeforeLand。
var confirmImprintBeforeClaimHook func()

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

// RequestImprint 由 admin 触发对某在线实例某文件的按需拓印（FR-46）：事务内建 pending 命令
// （载荷 mode=imprint + path）+ file.imprint-fetch 审计；提交后唤醒该 agent SSE（agent 仍读整棵
// plugins 树回传，落库 vs 转存由 mode 区分，agent 零改动）。在线校验与 SSE 唤醒口径同 RequestReverseFetch。
func (s *AgentCommandService) RequestImprint(ns, serverID, filePath, operator, clientIP string) (*model.AgentCommand, error) {
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
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns,
			Operator:      operator, Action: model.ActionFileImprintFetch,
			TargetType: model.TargetTypeCommand, TargetRef: serverID,
			Detail: fmt.Sprintf(`{"commandId":%d,"path":%q}`, cmd.ID, cleanPath),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		s.notifier.NotifyCommand(ns, serverID)
	}
	slog.Info("触发按需拓印", "namespace", ns, "serverId", serverID, "path", cleanPath,
		"commandId", cmd.ID, "operator", operator)
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
	slog.Info("拓印回传转存待审", "commandId", cmd.ID, "path", clean, "bytes", len(content))
	return nil
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

// ConfirmImprint 确认拓印落库（FR-46）：命令须 ready 且 imprint 模式；**单人自审门**——
// reviewedMd5 须等于命令转存内容 md5（强制看过 diff），否则 412。过门后先 CAS ready→done 认领并清空瞬态
// （赢者独占、挡并发双确认），再复用 FileService.Create（该层 path 不存在）/ Publish（已存在）落该层覆盖
// （FileService 内部事务 + file 审计 + 下发唤醒），最后写 file.imprint 审计。
func (s *AgentCommandService) ConfirmImprint(commandID uint, scope, group, zone, target, reviewedMd5, operator, clientIP string) (*ImprintConfirmResult, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	cmd, payload, err := s.requireReadyImprint(commandID)
	if err != nil {
		return nil, err
	}
	// 自审门：确认的内容必须等于看过 diff 的内容（盲确认 / 内容漂移即拒）。
	if reviewedMd5 == "" || reviewedMd5 != filetree.ContentMD5(cmd.ImprintContent) {
		return nil, apperr.ErrImprintReviewMismatch
	}
	// 归一并校验并入层（zone/server 取对应目标键；group/global 由 normalizeScope 处理）。
	scopeTarget := target
	if scope == model.ScopeZone {
		scopeTarget = zone
	}
	normGroup, normTarget, serr := normalizeScope(scope, group, scopeTarget)
	if serr != nil {
		return nil, serr
	}
	// L：拓印 diff 按「源服有效视角」审，server 层只能落回源服自身——目标为他服（含跨 ns 悬空目标）一律拒，
	// 挡借确认把某服配置落到本 ns 其它服 / 越权目标。源服 serverId 必属命令 ns，以此天然限定在本 ns。
	if scope == model.ScopeServer && normTarget != cmd.ServerID {
		return nil, apperr.ErrInvalidScope
	}

	// B（CAS 前置认领）：先把命令 ready→done 并清空瞬态——赢者独占落库，挡并发双确认重复落库 / 重复下发。
	// 瞬态内容此刻仍在内存 cmd，捕获到局部变量供落库（DB 列被本次更新清空）。
	content := cmd.ImprintContent
	if confirmImprintBeforeClaimHook != nil {
		confirmImprintBeforeClaimHook()
	}
	claimed, cerr := s.repo.UpdateStatusClearImprint(cmd.ID, model.CommandStatusReady, model.CommandStatusDone,
		fmt.Sprintf(`{"scope":%q}`, scope))
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, apperr.ErrImprintNotReady // 被并发认领（已 done/expired），本次让出、不重复落库
	}

	// 已认领，落库（FileService 内部事务 + file 审计 + 下发唤醒）。F：传归一后的 target，
	// 避免 conflict 回退按未归一 target 查不中而误 404（group/global 的多余 target 归一为空）。
	obj, ferr := s.landImprint(cmd.NamespaceCode, payload.Path, scope, group, normTarget, content, operator, clientIP)
	if ferr != nil {
		// 已认领但落库失败：命令已 done、瞬态已清，不回滚（重做请重新触发拓印）；记 ERROR 供排查。
		slog.Error("拓印已认领但落库失败（命令已 done、瞬态已清，需重新触发拓印）", "commandId", cmd.ID, "原因", ferr)
		return nil, ferr
	}

	// 落库成功后写 file.imprint 审计（detail 不含文件内容）。
	if e := s.auditRepo.Create(&model.AuditLog{
		NamespaceCode: cmd.NamespaceCode, Operator: operator, Action: model.ActionFileImprint,
		TargetType: model.TargetTypeFile,
		TargetRef:  fmt.Sprintf("%s/%s/%s@%s:%s", cmd.NamespaceCode, normGroup, payload.Path, scope, normTarget),
		Detail:     fmt.Sprintf(`{"commandId":%d,"scope":%q,"version":%d,"md5":%q}`, cmd.ID, scope, obj.Version, obj.ContentMD5),
		Result:     model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		slog.Warn("拓印确认审计写入失败（已落库）", "commandId", cmd.ID, "原因", e)
	}
	slog.Info("拓印确认落库", "commandId", cmd.ID, "scope", scope, "group", normGroup,
		"target", normTarget, "path", payload.Path, "version", obj.Version)
	return &ImprintConfirmResult{
		FileID: obj.ID, ScopeLevel: scope, Group: normGroup, Target: normTarget,
		Version: obj.Version, MD5: obj.ContentMD5,
	}, nil
}

// landImprint 把拓印内容落为指定层文件覆盖：该层 path 不存在则 Create（首版）、已存在则 Publish（新版本）。
// 复用 FileService 既有事务 + file 审计 + 下发唤醒；ns 来自命令（拓印源实例环境）。
// scopeTarget 须为归一后的目标键（normalizeScope 产物）——group/global 恒为空，否则 conflict 回退会按
// 未归一 target 查不中而误 404（见 ConfirmImprint 调用处 F 修正）。
func (s *AgentCommandService) landImprint(ns, filePath, scope, group, scopeTarget, content, operator, clientIP string) (*model.FileObject, error) {
	obj, cerr := s.fileSvc.Create(CreateFileParams{
		Namespace: ns, Group: group, Path: filePath,
		ScopeLevel: scope, ScopeTarget: scopeTarget,
		Content: content, Operator: operator, Comment: imprintComment, ClientIP: clientIP,
	})
	if cerr == nil {
		return obj, nil
	}
	if !errors.Is(cerr, apperr.ErrFileConflict) {
		return nil, cerr
	}
	// 该层 path 已存在 → 发布新版本（整文件覆盖该层）。
	existing, gerr := s.fileSvc.fileRepo.FindByIdentity(ns, groupForScope(scope, group), filePath, scope, scopeTarget)
	if gerr != nil {
		return nil, gerr
	}
	if existing == nil {
		return nil, apperr.ErrFileNotFound
	}
	return s.fileSvc.Publish(existing.ID, content, operator, imprintComment, clientIP)
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

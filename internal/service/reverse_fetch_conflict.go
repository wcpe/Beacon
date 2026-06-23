package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/model"
)

// reverseFetchReviewComment 是冲突审核 resolve 落库的版本 / 审计注释（FR-59）。
const reverseFetchReviewComment = "在线实例反向抓取（冲突审核）"

// submitContentEnvelope 是 conflict-review 期暂存 submit 回传内容的信封（瞬态，落 submit_content TEXT，FR-59）：
// 全部选定回传内容（path→content）+ 冲突 path 集。resolve / 取消 / 过期后清空。
type submitContentEnvelope struct {
	// 全部选定回传文件（含非冲突），按相对 path → 整文件内容
	Files map[string]string `json:"files"`
	// 冲突 path 集（目标层已有版本的那些 path），稳定记录供冲突清单与 resolve 校验
	Conflicts []string `json:"conflicts"`
}

// ConflictDiffResult 是单个冲突文件的 diff（FR-59）：抓取值 ⟷ 目标已有版本。
type ConflictDiffResult struct {
	Path string
	// 抓取值（agent 回传、暂存于 submit_content）+ md5（resolve overwrite 时回带作自审凭据）
	FetchedContent string
	FetchedMD5     string
	// 目标层已有版本内容 + md5 + 版本号（取自 file_object 当前版本）
	ExistingContent string
	ExistingMD5     string
	Version         int64
}

// ResolveDecision 是冲突文件的逐项处置（FR-59）：overwrite（取抓取、须自审 md5）/ keep（保留已有、跳过）。
type ResolveDecision struct {
	Path string
	// overwrite：取抓取覆盖已有；keep：保留已有跳过该文件
	Action string
	// overwrite 时的自审凭据：须等于该文件抓取内容 md5（盲确认 → 412）
	ReviewedMD5 string
}

// resolve 处置动作。
const (
	ResolveActionOverwrite = "overwrite"
	ResolveActionKeep      = "keep"
)

// detectConflicts 逐文件查目标层是否已有版本（FindByIdentity），返回有冲突的归一 path 集（去重、稳定排序）。
// scope/group/target 来自任务（与落库去向一致，经 normalizeScope 归一）。
func (s *ReverseFetchTaskService) detectConflicts(task *model.ReverseFetchTask, files []ImportFile) ([]string, error) {
	group, scopeTarget, err := normalizeScope(task.Scope, task.GroupCode, task.ScopeTarget)
	if err != nil {
		return nil, err
	}
	conflicts := make([]string, 0)
	for _, f := range files {
		cleanPath, perr := normalizePath(f.Path)
		if perr != nil {
			return nil, perr
		}
		existing, gerr := s.fileSvc.fileRepo.FindByIdentity(task.NamespaceCode, group, cleanPath, task.Scope, scopeTarget)
		if gerr != nil {
			return nil, gerr
		}
		if existing != nil {
			conflicts = append(conflicts, cleanPath)
		}
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

// enterConflictReview 暂存全部回传内容（含非冲突）到 submit_content 并把任务 fetching→conflict-review（FR-59）。
// 命令暂留 fetched（不 done），待 resolve 落库后再终结。返回状态错误供调用方上抛（非冲突时不走此路）。
func (s *ReverseFetchTaskService) enterConflictReview(task *model.ReverseFetchTask, cmd *model.AgentCommand,
	files []ImportFile, conflicts []string) error {
	envelope := submitContentEnvelope{Files: make(map[string]string, len(files)), Conflicts: conflicts}
	for _, f := range files {
		cleanPath, perr := normalizePath(f.Path)
		if perr != nil {
			s.failTask(task, cmd, perr.Error())
			return perr
		}
		envelope.Files[cleanPath] = f.Content
	}
	payload, merr := json.Marshal(envelope)
	if merr != nil {
		s.failTask(task, cmd, "暂存内容序列化失败")
		return apperr.ErrInternal
	}
	note := fmt.Sprintf("%d 个文件与目标已有版本冲突，待人工审核", len(conflicts))
	ok, e := s.taskRepo.EnterConflictReview(task.ID, string(payload), note)
	if e != nil {
		return e
	}
	if !ok {
		return apperr.ErrReverseFetchTaskState // 被并发迁移，非 fetching
	}
	// 命令置 done：回传内容已被控制面接收并暂存，命令使命完成；落库由 resolve 触发（与 submit 命令解耦）。
	if _, e := s.cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone,
		fmt.Sprintf(`{"taskId":%d,"conflicts":%d}`, task.ID, len(conflicts))); e != nil {
		slog.Warn("submit 命令置 done 失败（内容已暂存待审）", "commandId", cmd.ID, "原因", e)
	}
	slog.Info("反向抓取受管任务进入冲突审核", "taskId", task.ID, "commandId", cmd.ID,
		"files", len(files), "conflicts", len(conflicts))
	return nil
}

// Conflicts 取某 conflict-review 任务的冲突 path 清单（FR-59）。任务须 conflict-review。
func (s *ReverseFetchTaskService) Conflicts(taskID uint) ([]string, error) {
	_, env, err := s.requireConflictReview(taskID)
	if err != nil {
		return nil, err
	}
	return env.Conflicts, nil
}

// ConflictDiff 取某冲突文件的 diff（FR-59）：抓取值取自暂存 submit_content；目标已有版本实时查 file_object 当前版本。
// 任务须 conflict-review；path 须在冲突集内，否则 REVERSE_FETCH_CONFLICT_NOT_FOUND。
func (s *ReverseFetchTaskService) ConflictDiff(taskID uint, path string) (*ConflictDiffResult, error) {
	task, env, err := s.requireConflictReview(taskID)
	if err != nil {
		return nil, err
	}
	cleanPath, perr := normalizePath(path)
	if perr != nil {
		return nil, perr
	}
	if !containsString(env.Conflicts, cleanPath) {
		return nil, apperr.ErrReverseFetchConflictNotFound
	}
	fetched, ok := env.Files[cleanPath]
	if !ok {
		return nil, apperr.ErrReverseFetchConflictNotFound
	}
	group, scopeTarget, serr := normalizeScope(task.Scope, task.GroupCode, task.ScopeTarget)
	if serr != nil {
		return nil, serr
	}
	existing, gerr := s.fileSvc.fileRepo.FindByIdentity(task.NamespaceCode, group, cleanPath, task.Scope, scopeTarget)
	if gerr != nil {
		return nil, gerr
	}
	res := &ConflictDiffResult{
		Path:           cleanPath,
		FetchedContent: fetched, FetchedMD5: filetree.ContentMD5(fetched),
	}
	// 已有版本可能在审核期被删（脱离冲突）→ existing 为空、existing 侧留空，前端按 md5 差异呈现。
	if existing != nil {
		res.ExistingContent, res.ExistingMD5, res.Version = existing.Content, existing.ContentMD5, existing.Version
	}
	return res, nil
}

// Resolve 落库冲突审核决定（FR-59）：CAS 认领 conflict-review→ingesting（防并发双 resolve）→ 事务内
// Import 非冲突集 + 逐个 overwrite（Create/Publish 降级）+ 跳过 keep → done、清空 submit_content、审计。
// 每个 overwrite 须 reviewedMd5==该文件抓取 md5（自审门，盲确认 → 412）；冲突集每项须有决定。
func (s *ReverseFetchTaskService) Resolve(taskID uint, decisions []ResolveDecision, operator, clientIP string) (*ImportResult, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	task, env, err := s.requireConflictReview(taskID)
	if err != nil {
		return nil, err
	}
	// 归一决定并过自审门：冲突集每项须恰有一个决定；overwrite 须带正确 reviewedMd5。
	overwrite, derr := s.validateDecisions(env, decisions)
	if derr != nil {
		return nil, derr
	}

	// CAS 认领 conflict-review→ingesting（赢者独占落库，挡并发双 resolve）。
	claimed, cerr := s.taskRepo.ClaimConflictReview(task.ID)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, apperr.ErrReverseFetchTaskState // 被并发认领（已 ingesting / 终结）
	}

	result, lerr := s.landResolved(task, env, overwrite, operator, clientIP)
	if lerr != nil {
		// 已认领但落库失败：任务从 ingesting 终结为 failed、清瞬态；记 ERROR 供排查（重做请重发起抓取）。
		s.failTaskOnly(task.ID, model.ReverseFetchTaskIngesting, lerr.Error())
		slog.Error("冲突审核已认领但落库失败（任务转 failed、瞬态已清，需重新发起抓取）", "taskId", task.ID, "原因", lerr)
		return nil, lerr
	}

	now := time.Now().UTC()
	if ok, e := s.taskRepo.MarkTerminal(task.ID, model.ReverseFetchTaskIngesting, model.ReverseFetchTaskDone,
		fmt.Sprintf("冲突审核落库：覆盖 %d、保留 %d", len(overwrite), len(env.Conflicts)-len(overwrite)), true, now); e != nil {
		return nil, e
	} else if !ok {
		return nil, apperr.ErrReverseFetchTaskState
	}
	if e := s.auditRepo.Create(&model.AuditLog{
		NamespaceCode: task.NamespaceCode, Operator: operator, Action: model.ActionFileReverseFetchIngest,
		TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
		Detail: fmt.Sprintf(`{"taskId":%d,"resolved":true,"overwrite":%d,"keep":%d,"created":%d,"updated":%d}`,
			task.ID, len(overwrite), len(env.Conflicts)-len(overwrite), result.Created, result.Updated),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		slog.Warn("冲突审核入库审计写入失败（已落库）", "taskId", task.ID, "原因", e)
	}
	slog.Info("反向抓取受管任务冲突审核落库完成", "taskId", task.ID,
		"overwrite", len(overwrite), "keep", len(env.Conflicts)-len(overwrite),
		"created", result.Created, "updated", result.Updated, "operator", operator)
	return result, nil
}

// landResolved 落库 resolve 结果：非冲突集 + 确认覆盖的冲突文件合为一份，复用 FileService.Import 在单事务内
// 原子落库（每文件「存在则发新版本、不存在则首发」整文件覆盖）+ 一条 file.import 审计 + 提交后唤醒；
// 保留已有（keep）的冲突文件被排除、不落库。空集（全 keep）则不调 Import、直接 0 落地。
func (s *ReverseFetchTaskService) landResolved(task *model.ReverseFetchTask, env *submitContentEnvelope,
	overwrite map[string]string, operator, clientIP string) (*ImportResult, error) {
	conflictSet := make(map[string]struct{}, len(env.Conflicts))
	for _, p := range env.Conflicts {
		conflictSet[p] = struct{}{}
	}
	files := make([]ImportFile, 0, len(env.Files))
	// 非冲突文件（抓取时目标无版本）：与冲突集互斥，整批纳入。
	for p, content := range env.Files {
		if _, isConflict := conflictSet[p]; !isConflict {
			files = append(files, ImportFile{Path: p, Content: content})
		}
	}
	// 确认覆盖的冲突文件纳入（keep 的不纳入）。
	for p, content := range overwrite {
		files = append(files, ImportFile{Path: p, Content: content})
	}
	sortImportFiles(files)

	// 全 keep 且无非冲突文件 → 无可落库项，直接 0 落地（仍走 done，体现"全部保留已有"决定）。
	if len(files) == 0 {
		return &ImportResult{}, nil
	}
	return s.fileSvc.Import(ImportFilesParams{
		Namespace: task.NamespaceCode, Group: task.GroupCode,
		ScopeLevel: task.Scope, ScopeTarget: task.ScopeTarget,
		Files: files, Operator: operator, Comment: reverseFetchReviewComment, ClientIP: clientIP,
	})
}

// validateDecisions 校验冲突审核决定并归一出确认覆盖集（path→抓取内容）：冲突集每项须恰有一个决定（exactly-once）；
// overwrite 须带等于该文件抓取 md5 的 reviewedMd5（盲确认 → 412）；keep 跳过。决定 path 须在冲突集内。
func (s *ReverseFetchTaskService) validateDecisions(env *submitContentEnvelope, decisions []ResolveDecision) (map[string]string, error) {
	conflictSet := make(map[string]struct{}, len(env.Conflicts))
	for _, p := range env.Conflicts {
		conflictSet[p] = struct{}{}
	}
	overwrite := make(map[string]string)
	decided := make(map[string]struct{}, len(decisions))
	for _, d := range decisions {
		cleanPath, perr := normalizePath(d.Path)
		if perr != nil {
			return nil, perr
		}
		if _, isConflict := conflictSet[cleanPath]; !isConflict {
			return nil, apperr.ErrReverseFetchConflictNotFound // 决定指向非冲突 path
		}
		if _, dup := decided[cleanPath]; dup {
			return nil, apperr.ErrInvalidParam // 同一冲突给了重复决定
		}
		decided[cleanPath] = struct{}{}
		switch d.Action {
		case ResolveActionKeep:
			// 保留已有：跳过该文件、不落库。
		case ResolveActionOverwrite:
			content := env.Files[cleanPath]
			if d.ReviewedMD5 == "" || d.ReviewedMD5 != filetree.ContentMD5(content) {
				return nil, apperr.ErrReverseFetchReviewMismatch // 盲确认 / 内容漂移
			}
			overwrite[cleanPath] = content
		default:
			return nil, apperr.ErrInvalidParam // 非法动作
		}
	}
	// 冲突集每项都须有决定（避免遗漏冲突文件含糊落库）。
	if len(decided) != len(conflictSet) {
		return nil, apperr.ErrInvalidParam
	}
	return overwrite, nil
}

// requireConflictReview 取任务并校验其为 conflict-review 态、解析暂存信封；返回任务与信封。
func (s *ReverseFetchTaskService) requireConflictReview(taskID uint) (*model.ReverseFetchTask, *submitContentEnvelope, error) {
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, nil, err
	}
	if task.Status != model.ReverseFetchTaskConflictReview {
		return nil, nil, apperr.ErrReverseFetchTaskState
	}
	if strings.TrimSpace(task.SubmitContent) == "" {
		return nil, nil, apperr.ErrReverseFetchTaskState // 暂存已清（过期 / 异常），不可审核
	}
	var env submitContentEnvelope
	if json.Unmarshal([]byte(task.SubmitContent), &env) != nil {
		return nil, nil, apperr.ErrInternal
	}
	return task, &env, nil
}

// containsString 判断字符串是否在切片中。
func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// sortImportFiles 按 path 稳定排序导入文件集（落库顺序确定，便于测试与审计稳定）。
func sortImportFiles(files []ImportFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

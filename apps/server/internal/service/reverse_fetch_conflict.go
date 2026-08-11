package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/filetree"
	"github.com/wcpe/Beacon/apps/server/internal/model"
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
	Path string `json:"path"`
	// 抓取值（agent 回传、暂存于 submit_content）+ md5（resolve overwrite 时回带作自审凭据）
	FetchedContent string `json:"fetchedContent"`
	FetchedMD5     string `json:"fetchedMd5"`
	// 目标层已有版本内容 + md5 + 版本号（取自 file_object 当前版本）
	ExistingContent string `json:"existingContent"`
	ExistingMD5     string `json:"existingMd5"`
	Version         int64  `json:"version"`
}

// ConflictDiffBundle 是一次授权消费返回的完整冲突审核正文包；只含冲突路径。
type ConflictDiffBundle struct {
	Items []ConflictDiffResult `json:"items"`
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
// 提交命令完成、回传哈希绑定和待激活授权必须同事务；返回后正文仅能经授权消费。
func (s *ReverseFetchTaskService) enterConflictReview(task *model.ReverseFetchTask, cmd *model.AgentCommand,
	files []ImportFile, conflicts []string) error {
	if s.grants == nil {
		s.failTask(task, cmd, "未装配反向抓取正文授权服务")
		return apperr.ErrForbidden
	}
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
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.taskRepo.WithTx(tx).EnterConflictReview(task.ID, string(payload), note)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrReverseFetchTaskState
		}
		completed, e := s.cmdRepo.WithTx(tx).UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone,
			fmt.Sprintf(`{"taskId":%d,"conflicts":%d}`, task.ID, len(conflicts)))
		if e != nil {
			return e
		}
		if !completed {
			return apperr.ErrReverseFetchTaskState
		}
		return s.grants.WithTx(tx).BindAndActivatePendingCommandFromAgent(cmd.ID,
			authz.OperationAgentCommandReverseSubmit, reverseFetchSHA256(string(payload)), time.Now().UTC())
	})
	if err != nil {
		return err
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

// ConflictDiff 保留旧服务签名但拒绝直读正文；调用方必须消费提交审批创建的授权。
func (s *ReverseFetchTaskService) ConflictDiff(uint, string) (*ConflictDiffResult, error) {
	return nil, apperr.ErrForbidden
}

// ConsumeApprovedConflictDiff 仅允许原提交申请主体一次读取该任务全部冲突差异。
// 单份提交审批只有一个授权，故一次返回整个冲突集，避免多冲突任务无法逐项审核；非冲突正文绝不返回。
func (s *ReverseFetchTaskService) ConsumeApprovedConflictDiff(grantID string, taskID uint, principal auth.Principal) (*ConflictDiffBundle, error) {
	if s == nil || s.grants == nil || grantID == "" || taskID == 0 {
		return nil, apperr.ErrForbidden
	}
	var bundle *ConflictDiffBundle
	err := s.db.Transaction(func(tx *gorm.DB) error {
		task, env, err := s.requireConflictReviewInTx(tx, taskID)
		if err != nil {
			return err
		}
		items, err := s.conflictDiffsInTx(tx, task, env)
		if err != nil {
			return err
		}
		if err := s.grants.WithTx(tx).Consume(grantID, principal, authz.OperationAgentCommandReverseSubmit,
			fmt.Sprintf("agent-command/%d", task.SubmitCommandID), reverseFetchSHA256(task.SubmitContent), time.Now().UTC()); err != nil {
			return err
		}
		bundle = &ConflictDiffBundle{Items: items}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

// conflictDiffsInTx 从当前任务瞬态内容构造全部冲突差异；调用方已在授权消费事务内校验任务状态。
func (s *ReverseFetchTaskService) conflictDiffsInTx(tx *gorm.DB, task *model.ReverseFetchTask, env *submitContentEnvelope) ([]ConflictDiffResult, error) {
	if tx == nil || task == nil || env == nil {
		return nil, apperr.ErrForbidden
	}
	group, scopeTarget, serr := normalizeScope(task.Scope, task.GroupCode, task.ScopeTarget)
	if serr != nil {
		return nil, serr
	}
	paths := append([]string(nil), env.Conflicts...)
	sort.Strings(paths)
	items := make([]ConflictDiffResult, 0, len(paths))
	for _, path := range paths {
		fetched, ok := env.Files[path]
		if !ok {
			return nil, apperr.ErrReverseFetchConflictNotFound
		}
		existing, err := s.fileSvc.fileRepo.WithTx(tx).FindByIdentity(task.NamespaceCode, group, path, task.Scope, scopeTarget)
		if err != nil {
			return nil, err
		}
		item := ConflictDiffResult{Path: path, FetchedContent: fetched, FetchedMD5: filetree.ContentMD5(fetched)}
		// 已有版本可能在审核期被删（脱离冲突）→ existing 为空、existing 侧留空，前端按 md5 差异呈现。
		if existing != nil {
			item.ExistingContent, item.ExistingMD5, item.Version = existing.Content, existing.ContentMD5, existing.Version
		}
		items = append(items, item)
	}
	return items, nil
}

// Resolve 保留旧公开签名但拒绝直执；冲突落库只能经 RequestResolveApproval→worker。
func (s *ReverseFetchTaskService) Resolve(_ uint, _ []ResolveDecision, _, _ string) (*ImportResult, error) {
	return nil, apperr.ErrForbidden
}

// RequestResolveApproval 冻结冲突审核任务、清单、暂存结果与目标版本；批准后执行器才能落库。
func (s *ReverseFetchTaskService) RequestResolveApproval(taskID uint, decisions []ResolveDecision, reason, idempotencyKey,
	operator, clientIP string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil || taskID == 0 || operator == "" || reason == "" || idempotencyKey == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	task, env, err := s.requireConflictReview(taskID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	if s.grants == nil {
		return ApprovalTicketView{}, apperr.ErrForbidden
	}
	if err := s.grants.RequireConsumedCommandGrant(task.SubmitCommandID, principal,
		authz.OperationAgentCommandReverseSubmit, reverseFetchSHA256(task.SubmitContent), time.Now().UTC()); err != nil {
		return ApprovalTicketView{}, err
	}
	if _, err := s.validateDecisions(env, decisions); err != nil {
		return ApprovalTicketView{}, err
	}
	decisions = canonicalResolveDecisions(decisions)
	snapshot, err := s.resolveSnapshot(s.db, task, env)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	payload := reverseFetchResolveApprovalPayload{
		TaskID: task.ID, ManifestSHA256: reverseFetchSHA256(task.Manifest), OutputSHA256: reverseFetchSHA256(task.SubmitContent),
		Conflicts: snapshot, Decisions: decisions, Operator: operator, ClientIP: clientIP,
	}
	req, err := s.approval.Request(authz.Operation{
		Kind: authz.OperationAgentCommandReverseResolve, Resource: "reverse-fetch-task", ResourceID: fmt.Sprint(taskID),
		IdempotencyKey: idempotencyKey, Reason: reason,
		PreconditionSummary: "执行前校验受管任务、扫描清单、暂存结果和冲突目标版本未变化",
		ImpactSummary:       fmt.Sprintf("冲突审核：%d 项冲突处置", len(snapshot)),
	}, map[string]any{"taskId": payload.TaskID, "manifestSha256": payload.ManifestSHA256,
		"outputSha256": payload.OutputSHA256, "conflicts": payload.Conflicts,
		"decisions": payload.Decisions, "operator": operator, "clientIP": clientIP}, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

// applyResolveInTx 在审批许可绑定校验通过后重新校验冻结事实、落库并终结任务。
func (s *ReverseFetchTaskService) applyResolveInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit, p reverseFetchResolveApprovalPayload) (*model.ReverseFetchTask, *ImportResult, error) {
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, nil, err
	}
	if tx == nil || p.TaskID == 0 || p.Operator == "" {
		return nil, nil, apperr.ErrForbidden
	}
	task, env, err := s.requireConflictReviewInTx(tx, p.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if reverseFetchSHA256(task.Manifest) != p.ManifestSHA256 || reverseFetchSHA256(task.SubmitContent) != p.OutputSHA256 {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	snapshot, err := s.resolveSnapshot(tx, task, env)
	if err != nil {
		return nil, nil, err
	}
	if !sameResolveSnapshot(snapshot, p.Conflicts) {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	overwrite, err := s.validateDecisions(env, p.Decisions)
	if err != nil {
		return nil, nil, err
	}
	claimed, err := s.taskRepo.WithTx(tx).ClaimConflictReview(task.ID)
	if err != nil {
		return nil, nil, err
	}
	if !claimed {
		return nil, nil, apperr.ErrReverseFetchTaskState
	}
	result, err := s.landResolvedInTx(tx, task, env, overwrite, p.Operator, p.ClientIP)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if ok, err := s.taskRepo.WithTx(tx).MarkTerminal(task.ID, model.ReverseFetchTaskIngesting, model.ReverseFetchTaskDone,
		fmt.Sprintf("冲突审核落库：覆盖 %d、保留 %d", len(overwrite), len(env.Conflicts)-len(overwrite)), true, now); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, apperr.ErrReverseFetchTaskState
	}
	if err := s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: task.NamespaceCode, Operator: p.Operator, Action: model.ActionFileReverseFetchIngest,
		TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
		Detail: fmt.Sprintf(`{"taskId":%d,"resolved":true,"overwrite":%d,"keep":%d,"created":%d,"updated":%d}`,
			task.ID, len(overwrite), len(env.Conflicts)-len(overwrite), result.Created, result.Updated),
		Result: model.ResultOK, ClientIP: p.ClientIP,
	}); err != nil {
		return nil, nil, err
	}
	task.Status = model.ReverseFetchTaskDone
	task.SubmitContent = ""
	return task, result, nil
}

func (s *ReverseFetchTaskService) afterResolveCommit(task *model.ReverseFetchTask, result *ImportResult, operator string) {
	if task == nil || result == nil {
		return
	}
	if result.Created+result.Updated > 0 {
		s.fileSvc.afterImport(ImportFilesParams{
			Namespace: task.NamespaceCode, Group: task.GroupCode, ScopeLevel: task.Scope, ScopeTarget: task.ScopeTarget,
			Files: make([]ImportFile, result.Created+result.Updated), Operator: operator, Comment: reverseFetchReviewComment,
		}, result)
	}
	s.recordFetchReversible(task, result, operator)
	slog.Info("反向抓取受管任务冲突审核落库完成", "taskId", task.ID,
		"created", result.Created, "updated", result.Updated, "operator", operator)
}

// landResolvedInTx 落库 resolve 结果：非冲突集 + 确认覆盖的冲突文件合为一份，复用文件导入核心在调用方事务内
// 原子落库（每文件「存在则发新版本、不存在则首发」整文件覆盖）+ 一条 file.import 审计 + 提交后唤醒；
// 保留已有（keep）的冲突文件被排除、不落库。空集（全 keep）则不调 Import、直接 0 落地。
func (s *ReverseFetchTaskService) landResolvedInTx(tx *gorm.DB, task *model.ReverseFetchTask, env *submitContentEnvelope,
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
	return s.fileSvc.applyImportInTx(tx, ImportFilesParams{
		Namespace: task.NamespaceCode, Group: task.GroupCode,
		ScopeLevel: task.Scope, ScopeTarget: task.ScopeTarget,
		Files: files, Operator: operator, Comment: reverseFetchReviewComment, ClientIP: clientIP,
	})
}

type reverseFetchResolveConflictSnapshot struct {
	Path            string `json:"path"`
	FetchedMD5      string `json:"fetchedMd5"`
	ExistingMD5     string `json:"existingMd5"`
	ExistingVersion int64  `json:"existingVersion"`
	ExistingPresent bool   `json:"existingPresent"`
}

// resolveSnapshot 只冻结内容哈希、版本与路径；不得把抓取或已有文件正文放入审批事实。
func (s *ReverseFetchTaskService) resolveSnapshot(db *gorm.DB, task *model.ReverseFetchTask, env *submitContentEnvelope) ([]reverseFetchResolveConflictSnapshot, error) {
	if db == nil || task == nil || env == nil {
		return nil, apperr.ErrForbidden
	}
	group, scopeTarget, err := normalizeScope(task.Scope, task.GroupCode, task.ScopeTarget)
	if err != nil {
		return nil, err
	}
	paths := append([]string(nil), env.Conflicts...)
	sort.Strings(paths)
	items := make([]reverseFetchResolveConflictSnapshot, 0, len(paths))
	repo := s.fileSvc.fileRepo.WithTx(db)
	for _, path := range paths {
		content, ok := env.Files[path]
		if !ok {
			return nil, apperr.ErrApprovalTargetChanged
		}
		existing, err := repo.FindByIdentity(task.NamespaceCode, group, path, task.Scope, scopeTarget)
		if err != nil {
			return nil, err
		}
		item := reverseFetchResolveConflictSnapshot{Path: path, FetchedMD5: filetree.ContentMD5(content)}
		if existing != nil {
			item.ExistingPresent, item.ExistingMD5, item.ExistingVersion = true, existing.ContentMD5, existing.Version
		}
		items = append(items, item)
	}
	return items, nil
}

func sameResolveSnapshot(left, right []reverseFetchResolveConflictSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func reverseFetchSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}

func (s *ReverseFetchTaskService) requireConflictReviewInTx(tx *gorm.DB, taskID uint) (*model.ReverseFetchTask, *submitContentEnvelope, error) {
	task, err := s.taskRepo.WithTx(tx).GetByID(taskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, apperr.ErrReverseFetchTaskNotFound
	}
	if task.Status != model.ReverseFetchTaskConflictReview || strings.TrimSpace(task.SubmitContent) == "" {
		return nil, nil, apperr.ErrReverseFetchTaskState
	}
	var env submitContentEnvelope
	if json.Unmarshal([]byte(task.SubmitContent), &env) != nil {
		return nil, nil, apperr.ErrInternal
	}
	return task, &env, nil
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

// canonicalResolveDecisions 在 validateDecisions 成功后按稳定 path 冻结处置集合，避免提交顺序影响载荷哈希和幂等语义。
func canonicalResolveDecisions(decisions []ResolveDecision) []ResolveDecision {
	canonical := make([]ResolveDecision, len(decisions))
	for i, decision := range decisions {
		canonical[i] = decision
		canonical[i].Path, _ = normalizePath(decision.Path)
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].Path < canonical[j].Path
	})
	return canonical
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

// sortImportFiles 按 path 稳定排序导入文件集（落库顺序确定，便于测试与审计稳定）。
func sortImportFiles(files []ImportFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

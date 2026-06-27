package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// DefaultUndoWindowHours 是可撤回时间窗的默认值（小时）：reversible 账目创建超过此时长由清理器置 expired（FR-116）。
// 默认值入应用层常量、不硬编码散落；可经设置 store 热改（ADR-0038，白名单 key undo.window-hours）。
const DefaultUndoWindowHours = 24

// undoConfigComment / undoFileComment 是撤回回滚落库的版本注释（无敏感内容）。
const (
	undoConfigComment = "撤回发布（FR-116）"
	undoFileComment   = "撤回下发（FR-116）"
)

// pubPushPayload 是 publish/push 撤回的反向快照：撤回 = 把目标对象回滚到操作前版本。
type pubPushPayload struct {
	// 被操作的 config_item（publish）/ file_object（push）id
	ItemID uint `json:"itemId"`
	// 操作前版本号（撤回时回滚到此版本）
	PreVersion int64 `json:"preVersion"`
}

// fetchUpdatedItem 是 fetch 撤回中被覆盖更新的单个受管项快照。
type fetchUpdatedItem struct {
	ID         uint  `json:"id"`
	PreVersion int64 `json:"preVersion"`
}

// fetchPayload 是 fetch（反向抓取 ingest）撤回的反向快照：
// 撤回 = 软删被该次 ingest 新建的受管项 + 把被覆盖更新的受管项回滚到 ingest 前版本。
type fetchPayload struct {
	// 触发该次 ingest 的受管任务 id（追溯用）
	TaskID uint `json:"taskId"`
	// 被该次 ingest 新建的受管项 id 列表（撤回时软删）
	Created []uint `json:"created"`
	// 被该次 ingest 覆盖更新的受管项 + 覆盖前版本（撤回时回滚到 preVersion）
	Updated []fetchUpdatedItem `json:"updated"`
}

// RecordPublishParams 是发布 / 下发记一条可逆账目的入参（在大操作事务内调用）。
type RecordPublishParams struct {
	Namespace   string
	OpType      string // model.ReversibleOpPublish / ReversibleOpPush
	Scope       string
	ScopeTarget string
	// 目标对象 id 与操作前版本（撤回回滚到此版本）
	ItemID     uint
	PreVersion int64
	// 关联正向产物引用（如 config_item/file_object 标识串），供被覆盖判定与追溯
	ForwardRef string
	// 人类可读摘要（无敏感内容）
	Summary  string
	Operator string
}

// RecordFetchParams 是反向抓取 ingest 记一条可逆账目的入参（在 ingest 事务后调用）。
type RecordFetchParams struct {
	Namespace    string
	Scope        string
	ScopeTarget  string
	TaskID       uint
	CreatedIDs   []uint
	UpdatedItems []ImportUpdatedItem
	ForwardRef   string
	Summary      string
	Operator     string
}

// UndoNotifier 是撤回提交后唤醒长轮询的窄接口（由 ChangeNotifier 实现）。
type UndoNotifier interface {
	NotifyConfigChange(ns, scopeLevel, group, scopeTarget string)
	NotifyFileChange(ns, scopeLevel, group, scopeTarget string)
}

// ReversibleOperationService 编排配置操作级撤回子系统（FR-116，见 ADR-0051）：
// 记可逆账目（与正向写同事务）、撤回（幂等 + 并发安全 + 过期 / 被覆盖双闸 + 多表写事务原子 + 提交后唤醒）。
// 撤回复用既有版本回滚原语（ConfigService/FileService 的 *InTx 回滚核），本服务只做编排，不重新发明配置存储。
type ReversibleOperationService struct {
	db        *gorm.DB
	repo      *repository.ReversibleOperationRepository
	configSvc *ConfigService
	fileSvc   *FileService
	auditRepo *repository.AuditLogRepository
	settings  *SettingsService // 可撤回窗口 N 小时从设置 store 读、热生效（FR-61）
	notifier  UndoNotifier     // 可选，事务提交后唤醒受影响长轮询
}

// NewReversibleOperationService 构造服务。
func NewReversibleOperationService(db *gorm.DB, repo *repository.ReversibleOperationRepository,
	configSvc *ConfigService, fileSvc *FileService, auditRepo *repository.AuditLogRepository,
	settings *SettingsService) *ReversibleOperationService {
	return &ReversibleOperationService{db: db, repo: repo, configSvc: configSvc, fileSvc: fileSvc, auditRepo: auditRepo, settings: settings}
}

// SetNotifier 注入长轮询唤醒器（启动时装配；未注入则撤回后不唤醒）。
func (s *ReversibleOperationService) SetNotifier(n UndoNotifier) { s.notifier = n }

// WindowHours 返回当前可撤回窗口（小时）：从设置 store 读、热生效；未注入设置则用默认常量。
func (s *ReversibleOperationService) WindowHours() int {
	if s.settings == nil {
		return DefaultUndoWindowHours
	}
	return s.settings.GetInt(SettingUndoWindowHours)
}

// RecordPublishInTx 在给定（大操作的）事务内记一条 publish/push 可逆账目，并把同 scope 旧 reversible 账目置 superseded。
// 与正向写同事务——操作与其可逆账目原子，不存在"操作成功但没记账→无法撤回"的窗口（ADR-0051 决策 4）。
func (s *ReversibleOperationService) RecordPublishInTx(tx *gorm.DB, p RecordPublishParams) error {
	if !model.IsValidReversibleOpType(p.OpType) || p.OpType == model.ReversibleOpFetch {
		return apperr.ErrInvalidParam
	}
	payload, err := json.Marshal(pubPushPayload{ItemID: p.ItemID, PreVersion: p.PreVersion})
	if err != nil {
		return err
	}
	return s.recordInTx(tx, p.Namespace, p.OpType, p.Scope, p.ScopeTarget, p.ForwardRef, string(payload), p.Summary, p.Operator)
}

// RecordFetch 记一条 fetch 可逆账目（在 ingest 落库事务提交后调用，best-effort）：
// fetch 的反向快照在 ingest 落库后才齐全（created/updated 的 id 与 preVersion），故与 ingest 同事务记账由
// ReceiveSubmitIngest 在其事务后补记；落库失败仅 WARN 不阻断（账目缺失只致该次无法撤回，不损正向 ingest 结果）。
func (s *ReversibleOperationService) RecordFetch(p RecordFetchParams) error {
	if len(p.CreatedIDs) == 0 && len(p.UpdatedItems) == 0 {
		return nil // 空 ingest 不记账（无可撤内容）
	}
	updated := make([]fetchUpdatedItem, 0, len(p.UpdatedItems))
	for _, u := range p.UpdatedItems {
		updated = append(updated, fetchUpdatedItem(u))
	}
	payload, err := json.Marshal(fetchPayload{TaskID: p.TaskID, Created: p.CreatedIDs, Updated: updated})
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.recordInTx(tx, p.Namespace, model.ReversibleOpFetch, p.Scope, p.ScopeTarget, p.ForwardRef, string(payload), p.Summary, p.Operator)
	})
}

// recordInTx 在事务内插一条 reversible 账目并把同 scope 旧 reversible 账目置 superseded（防脏撤回，ADR-0051 决策 8）。
func (s *ReversibleOperationService) recordInTx(tx *gorm.DB, ns, opType, scope, scopeTarget, forwardRef, payload, summary, operator string) error {
	op := &model.ReversibleOperation{
		NamespaceCode: ns, OpType: opType, Scope: scope, ScopeTarget: scopeTarget,
		ForwardRef: forwardRef, Status: model.ReversibleStatusReversible,
		InversePayload: payload, Summary: summary, Operator: operator,
	}
	if err := s.repo.WithTx(tx).Create(op); err != nil {
		return err
	}
	// 同一被操作对象（forward_ref）上的旧 reversible 账目被本次操作覆盖 → 置 superseded（排除刚建的本条）。
	if _, err := s.repo.WithTx(tx).SupersedeActiveByForwardRef(ns, opType, forwardRef, op.ID, time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

// Undo 撤回一条可逆操作（FR-116，见 ADR-0051 决策 3/5/6/7）：单事务内 ——
// CAS 抢占 reversible→reversed（幂等闸 + 并发串行化）→ 按 op 类型执行反向回滚（复用 *InTx 回滚核）→ 写 config.undo-* 审计。
// 提交成功后唤醒受影响长轮询（事务内不做下发 IO）。
//   - 重复撤回：第二次抢不到 CAS，若已 reversed 则幂等返回成功；若 expired/superseded 则返回明确错误。
func (s *ReversibleOperationService) Undo(id uint, operator, clientIP string) (*model.ReversibleOperation, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	op, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, apperr.ErrReversibleOpNotFound
	}
	// 快速幂等 / 双闸短路（无需进事务）：已撤回幂等成功；过期 / 被覆盖明确拒。
	switch op.Status {
	case model.ReversibleStatusReversed:
		return op, nil // 幂等成功：已撤回，不再二次回滚
	case model.ReversibleStatusExpired:
		return nil, apperr.ErrReversibleOpExpired
	case model.ReversibleStatusSuperseded:
		return nil, apperr.ErrReversibleOpSuperseded
	}

	// 撤回后需唤醒的对象（事务内收集，提交成功后唤醒——事务内不做下发 IO，ADR-0051 决策 5）。
	var configToNotify *model.ConfigItem
	var filesToNotify []*model.FileObject

	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// CAS 抢占 reversible→reversed：抢到才执行回滚——串行化同一行的并发撤回，杜绝双撤回（ADR-0051 决策 6/7）。
		ok, e := s.repo.WithTx(tx).MarkReversed(op.ID, operator, now)
		if e != nil {
			return e
		}
		if !ok {
			// 抢不到：被并发撤回 / 过期 / 覆盖。回查状态决定幂等成功还是报错（在同事务内读，避免脏读）。
			cur, e2 := s.repo.WithTx(tx).FindByID(op.ID)
			if e2 != nil {
				return e2
			}
			return statusToUndoOutcome(cur)
		}
		// 抢到 CAS：按 op 类型执行反向回滚（payload 仍为抢占前读到的 op，MarkReversed 只清库内瞬态、不动内存副本）。
		switch op.OpType {
		case model.ReversibleOpPublish:
			item, e := s.undoPublishInTx(tx, op, operator)
			if e != nil {
				return e
			}
			configToNotify = item
		case model.ReversibleOpPush:
			obj, e := s.undoPushInTx(tx, op, operator)
			if e != nil {
				return e
			}
			filesToNotify = append(filesToNotify, obj)
		case model.ReversibleOpFetch:
			objs, e := s.undoFetchInTx(tx, op, operator, now)
			if e != nil {
				return e
			}
			filesToNotify = objs
		default:
			return apperr.ErrInvalidParam
		}
		return s.writeUndoAudit(tx, op, operator, clientIP)
	})
	if err != nil {
		// errAlreadyReversed 是事务内回查判定为"已被并发撤回"的哨兵：回滚事务后对外返回幂等成功。
		if err == errAlreadyReversed {
			cur, _ := s.repo.FindByID(op.ID)
			if cur != nil {
				return cur, nil
			}
			return op, nil
		}
		return nil, err
	}

	// 提交成功后唤醒受影响长轮询（复用既有"唤醒即重算比对 md5"，真变才下发）。
	if s.notifier != nil {
		if configToNotify != nil {
			s.notifier.NotifyConfigChange(configToNotify.NamespaceCode, configToNotify.ScopeLevel, configToNotify.GroupCode, configToNotify.ScopeTarget)
		}
		for _, obj := range filesToNotify {
			s.notifier.NotifyFileChange(obj.NamespaceCode, obj.ScopeLevel, obj.GroupCode, obj.ScopeTarget)
		}
	}
	op.Status = model.ReversibleStatusReversed
	op.ReversedBy = operator
	op.ReversedAt = now
	slog.Info("撤回配置操作", "id", op.ID, "类型", op.OpType, "scope", op.Scope, "操作人", operator)
	return op, nil
}

// errAlreadyReversed 是事务内回查发现账目已被并发撤回的哨兵错误：用于回滚本事务后对外返回幂等成功。
var errAlreadyReversed = fmt.Errorf("reversible-op already reversed")

// statusToUndoOutcome 把"抢不到 CAS 后回查到的状态"映射为撤回结果：
// reversed → errAlreadyReversed（幂等成功）；expired/superseded → 对应明确错误；其它（已不存在等）→ 状态错。
func statusToUndoOutcome(cur *model.ReversibleOperation) error {
	if cur == nil {
		return apperr.ErrReversibleOpNotFound
	}
	switch cur.Status {
	case model.ReversibleStatusReversed:
		return errAlreadyReversed
	case model.ReversibleStatusExpired:
		return apperr.ErrReversibleOpExpired
	case model.ReversibleStatusSuperseded:
		return apperr.ErrReversibleOpSuperseded
	default:
		return apperr.ErrReversibleOpState
	}
}

// undoPublishInTx 撤回一次发布：把 config_item 回滚到发布前版本（复用 ConfigService.RollbackInTx）。
func (s *ReversibleOperationService) undoPublishInTx(tx *gorm.DB, op *model.ReversibleOperation, operator string) (*model.ConfigItem, error) {
	var p pubPushPayload
	if err := json.Unmarshal([]byte(op.InversePayload), &p); err != nil {
		return nil, apperr.ErrReversibleOpState
	}
	item, err := s.configSvc.GetInTx(tx, p.ItemID)
	if err != nil {
		return nil, err
	}
	return s.configSvc.RollbackInTx(tx, item, p.PreVersion, operator, undoConfigComment)
}

// undoPushInTx 撤回一次下发：把 file_object 回滚到下发前版本（复用 FileService.RollbackInTx）。
func (s *ReversibleOperationService) undoPushInTx(tx *gorm.DB, op *model.ReversibleOperation, operator string) (*model.FileObject, error) {
	var p pubPushPayload
	if err := json.Unmarshal([]byte(op.InversePayload), &p); err != nil {
		return nil, apperr.ErrReversibleOpState
	}
	obj, err := s.fileSvc.GetInTx(tx, p.ItemID)
	if err != nil {
		return nil, err
	}
	return s.fileSvc.RollbackInTx(tx, obj, p.PreVersion, operator, undoFileComment)
}

// undoFetchInTx 撤销一次 ingest 纳管（ADR-0051 决策 3）：
// 被该次 ingest 新建的受管项软删、被覆盖更新的受管项回滚到 ingest 前版本（不删磁盘文件）。
// 返回受影响（需唤醒）的文件对象。被新建项软删后从覆盖链脱落，亦需唤醒——其软删前快照一并收集。
func (s *ReversibleOperationService) undoFetchInTx(tx *gorm.DB, op *model.ReversibleOperation, operator string, now time.Time) ([]*model.FileObject, error) {
	var p fetchPayload
	if err := json.Unmarshal([]byte(op.InversePayload), &p); err != nil {
		return nil, apperr.ErrReversibleOpState
	}
	var affected []*model.FileObject
	// 被新建项：先取其当前快照（供唤醒 scope），再软删。已被并发软删 / 删除则跳过（幂等）。
	for _, id := range p.Created {
		obj, err := s.fileSvc.GetInTx(tx, id)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if err := s.fileSvc.SoftDeleteInTx(tx, id, now); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		affected = append(affected, obj)
	}
	// 被覆盖项：回滚到 ingest 前版本。已被删除则跳过（幂等）。
	for _, u := range p.Updated {
		obj, err := s.fileSvc.GetInTx(tx, u.ID)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		rolled, err := s.fileSvc.RollbackInTx(tx, obj, u.PreVersion, operator, undoFileComment)
		if err != nil {
			return nil, err
		}
		affected = append(affected, rolled)
	}
	return affected, nil
}

// isNotFound 判断错误是否为受管项不存在（撤回幂等：被并发删除的项跳过、不让整次撤回失败）。
func isNotFound(err error) bool {
	if ae, ok := err.(*apperr.Error); ok {
		return ae == apperr.ErrFileNotFound || ae == apperr.ErrConfigNotFound
	}
	return false
}

// writeUndoAudit 在事务内写一条撤回审计（config.undo-*，detail 仅记 id / 类型 / scope，不含文件内容）。
func (s *ReversibleOperationService) writeUndoAudit(tx *gorm.DB, op *model.ReversibleOperation, operator, clientIP string) error {
	action := undoActionFor(op.OpType)
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: op.NamespaceCode,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeReversibleOp,
		TargetRef:     fmt.Sprintf("%d", op.ID),
		Detail:        fmt.Sprintf(`{"reversibleOpId":%d,"opType":%q,"scope":%q,"scopeTarget":%q}`, op.ID, op.OpType, op.Scope, op.ScopeTarget),
		Result:        model.ResultOK,
		ClientIP:      clientIP,
	})
}

// undoActionFor 把可逆操作类型映射为撤回审计 action。
func undoActionFor(opType string) string {
	switch opType {
	case model.ReversibleOpPublish:
		return model.ActionConfigUndoPublish
	case model.ReversibleOpPush:
		return model.ActionConfigUndoPush
	case model.ReversibleOpFetch:
		return model.ActionConfigUndoFetch
	default:
		return model.ActionConfigUndoPublish
	}
}

// List 列出可逆操作账目（供工作台操作日志，FR-116）。
func (s *ReversibleOperationService) List(f repository.ReversibleOperationFilter) ([]model.ReversibleOperation, error) {
	return s.repo.List(f)
}

// ExpireStale 把创建早于 before 仍 reversible 的账目标 expired 并清空反向快照瞬态（清理器调用）。
func (s *ReversibleOperationService) ExpireStale(before time.Time) (int64, error) {
	return s.repo.ExpireStale(before, time.Now().UTC())
}

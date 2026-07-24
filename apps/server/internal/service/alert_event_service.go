package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 告警事件分页默认与上限（FR-89）。
const (
	defaultAlertEventPageSize = 20
	maxAlertEventPageSize     = 200
)

// AlertActiveKey 按 (namespace, serverId) 定位一台实例的活跃告警计数（FR-157）。
// 与健康计算轮的实例事实 (HealthFact.Namespace, HealthFact.ServerID) 同键，供 activeAlerts 因子接真。
type AlertActiveKey struct {
	Namespace string
	ServerID  string
}

// AlertEventService 提供告警事件的持久化、查询与处理工作流（FR-89 留痕，FR-157 处理工作流，见 ADR-0041/ADR-0064）。
// Record 供告警扇出的持久化通道调用落库（新行默认 status=open）；List 供管理台「事件」页只读查询；
// Handle 确认 / 标记已处理并写审计；ActiveCounts 批量供健康计算轮取当前活跃告警数（severe：禁逐实例查库）。
type AlertEventService struct {
	db        *gorm.DB
	repo      *repository.AlertEventRepository
	auditRepo *repository.AuditLogRepository
}

// NewAlertEventService 构造服务。db + auditRepo 供处理工作流在同事务内原子更新状态并写审计（FR-157）。
func NewAlertEventService(db *gorm.DB, repo *repository.AlertEventRepository, auditRepo *repository.AuditLogRepository) *AlertEventService {
	return &AlertEventService{db: db, repo: repo, auditRepo: auditRepo}
}

// Record 落库一条告警事件；未显式指定处理状态时默认 open（新告警即待处理，FR-157）。
// created_at 交由 GORM 全局 NowFunc 统一填 UTC（不在此设时间，保与全表一致）。
func (s *AlertEventService) Record(e *model.AlertEvent) error {
	if e.Status == "" {
		e.Status = model.AlertEventStatusOpen
	}
	return s.repo.Create(e)
}

// List 分页查询告警事件；规整 page/size 后委托仓库（时间倒序）。
func (s *AlertEventService) List(f repository.AlertEventFilter) ([]model.AlertEvent, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 {
		f.Size = defaultAlertEventPageSize
	}
	if f.Size > maxAlertEventPageSize {
		f.Size = maxAlertEventPageSize
	}
	return s.repo.List(f)
}

// Handle 处理一条告警事件（FR-157，见 ADR-0064）：按动作推进状态（acknowledge→acknowledged / resolve→resolved），
// 记录处理人 / 处理时刻 / 处理说明，并在同事务内写专项审计（含操作者 / 事件 id / 动作 / 原因）。
// 事件不存在 → ErrAlertEventNotFound；动作非法 → ErrAlertActionInvalid。返回更新后的事件。
func (s *AlertEventService) Handle(id uint, action, handleNote, operator, clientIP string) (*model.AlertEvent, error) {
	status, auditAction, err := resolveAlertAction(action)
	if err != nil {
		return nil, err
	}

	var updated *model.AlertEvent
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		e, err := repo.Get(id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrAlertEventNotFound
			}
			return err
		}
		now := time.Now().UTC()
		e.Status = status
		e.HandledBy = operator
		e.HandledAt = &now
		e.HandleNote = handleNote
		if err := repo.Save(e); err != nil {
			return err
		}
		updated = e
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: e.Namespace,
			Operator:      operator,
			Action:        auditAction,
			TargetType:    model.TargetTypeAlertEvent,
			TargetRef:     strconv.FormatUint(uint64(id), 10),
			Detail:        alertHandleAuditDetail(status, handleNote),
			Result:        model.ResultOK,
			ClientIP:      clientIP,
		})
	})
	if txErr != nil {
		return nil, txErr
	}
	return updated, nil
}

// ActiveCounts 一次性批量取各实例当前活跃（open）告警数，键为 (namespace, serverId)（FR-157）。
// 供健康计算轮每轮取一次注入 activeAlerts 因子——严禁在逐实例循环里查库（testing-and-quality §3 / 规则 §17）。
func (s *AlertEventService) ActiveCounts() (map[AlertActiveKey]int, error) {
	rows, err := s.repo.ActiveCounts()
	if err != nil {
		return nil, err
	}
	counts := make(map[AlertActiveKey]int, len(rows))
	for _, r := range rows {
		counts[AlertActiveKey{Namespace: r.Namespace, ServerID: r.ServerID}] = r.Count
	}
	return counts, nil
}

// resolveAlertAction 把处理动作映射为目标状态与审计动作；非法动作返回 ErrAlertActionInvalid。
// 兼容两种入参措辞：动词 acknowledge/resolve（ADR-0064）与目标状态 acknowledged/resolved（前端契约 HandleAlertBody.status）。
func resolveAlertAction(action string) (status, auditAction string, err error) {
	switch action {
	case "acknowledge", model.AlertEventStatusAcknowledged:
		return model.AlertEventStatusAcknowledged, model.ActionAlertEventAcknowledge, nil
	case "resolve", model.AlertEventStatusResolved:
		return model.AlertEventStatusResolved, model.ActionAlertEventResolve, nil
	default:
		return "", "", apperr.ErrAlertActionInvalid
	}
}

// alertHandleAuditDetail 组装处理审计 detail（json 文本）：目标状态 + 处置说明。
// 说明为运维自填的处置原因，非凭据，原样记录供追溯。
func alertHandleAuditDetail(status, handleNote string) string {
	raw, _ := json.Marshal(map[string]string{"status": status, "note": handleNote})
	return string(raw)
}

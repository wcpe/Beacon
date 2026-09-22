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
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
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
	// healthQuery 供详情聚合读取该服近期健康真源（FR-230）；未装配时降级为仅时间线。
	healthQuery *HealthQueryService
}

// SetHealthQuery 装配健康查询服务，供告警详情内嵌「该服近期状态」（FR-230）。
func (s *AlertEventService) SetHealthQuery(q *HealthQueryService) {
	s.healthQuery = q
}

// ResolveAlertRole 解析实例的分级角色（proxy / lobby / backend，FR-231），供告警分级通道查控制面权威事实。
// namespace 无 node / 查不到 → 空串（通道按 backend 规则安全降级）；DB 未装配亦回空串。
func (s *AlertEventService) ResolveAlertRole(namespace, serverID string) string {
	if s.db == nil || serverID == "" {
		return ""
	}
	var server model.Server
	if err := s.db.
		Joins("JOIN namespace ON namespace.id = server.namespace_id").
		Select("server.kind, server.lobby_cluster_id").
		Where("namespace.code = ? AND server.server_id = ?", namespace, serverID).
		First(&server).Error; err != nil {
		return ""
	}
	lobbyID := uint(0)
	if server.LobbyClusterID != nil {
		lobbyID = *server.LobbyClusterID
	}
	return alert.RoleOf(server.Kind, lobbyID)
}

// OverrideAlertLevel 人工升降一条告警的级别（FR-231）：写 severity_override/overridden_by/overridden_at，
// 并在同事务写专项审计（含新旧级别 + 操作者）。事件不存在 → ErrAlertEventNotFound；级别非法 → ErrInvalidParam。
func (s *AlertEventService) OverrideAlertLevel(id uint, level, operator, clientIP string) (*model.AlertEvent, error) {
	if level != model.AlertLevelInfo && level != model.AlertLevelWarning && level != model.AlertLevelCritical {
		return nil, apperr.ErrInvalidParam
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
		oldLevel := e.Level
		now := time.Now().UTC()
		e.SeverityOverride = level
		e.OverriddenBy = operator
		e.OverriddenAt = &now
		if err := repo.Save(e); err != nil {
			return err
		}
		updated = e
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: e.Namespace,
			Operator:      operator,
			Action:        model.ActionAlertEventLevelOverridden,
			TargetType:    model.TargetTypeAlertEvent,
			TargetRef:     strconv.FormatUint(uint64(id), 10),
			Detail:        alertLevelOverrideAuditDetail(oldLevel, level),
			Result:        model.ResultOK,
			ClientIP:      clientIP,
		})
	})
	if txErr != nil {
		return nil, txErr
	}
	return updated, nil
}

// alertLevelOverrideAuditDetail 组装人工改级审计 detail（json 文本）：原级别 → 新级别。
func alertLevelOverrideAuditDetail(oldLevel, newLevel string) string {
	raw, _ := json.Marshal(map[string]string{"oldLevel": oldLevel, "newLevel": newLevel})
	return string(raw)
}

// 告警详情时间线窗口（FR-230 §7 已定默认）：最近 24h 内最多 20 条。
const (
	alertContextTimelineLimit       = 20
	alertContextTimelineWindowHours = 24
)

// AlertContextServer 是告警详情内嵌的「该服近期状态」（取健康既有真源，不复制存储）。
type AlertContextServer struct {
	ServerID string `json:"serverId"`
	// Online 表示该服当前「非失联」（可达）；与 ServerView.Online（active 身份即在线）口径不同，勿直接互比。
	Online      bool     `json:"online"`
	Level       string   `json:"level"`
	Score       int      `json:"score"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
	SampledAtMs int64    `json:"sampledAtMs"`
}

// AlertContextResult 是告警详情聚合结果：该服近期状态 + 该服 / 该 namespace 的告警时间线。
type AlertContextResult struct {
	Server              *AlertContextServer
	Timeline            []model.AlertEvent
	TimelineLimit       int
	TimelineWindowHours int
}

// Context 聚合一条告警的「该服近期状态 + 告警时间线」（FR-230）：状态实时查健康真源（域外 / 已归档降级为 nil），
// 时间线实时查 alert_event（按 serverId；无 serverId 的集群级告警退化为按 namespace）。观测范围循 FR-213。
func (s *AlertEventService) Context(id uint, scope ObservationScope) (*AlertContextResult, error) {
	e, err := s.repo.Get(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrAlertEventNotFound
		}
		return nil, err
	}
	res := &AlertContextResult{TimelineLimit: alertContextTimelineLimit, TimelineWindowHours: alertContextTimelineWindowHours}
	if e.ServerID != "" && s.healthQuery != nil {
		if detail, herr := s.healthQuery.HealthDetailInScope(e.ServerID, scope); herr == nil {
			// Online 口径：该服当前「非失联」（健康视图 reasons 不含 lost）。
			// 注意与列表 ServerView.Online（存在 active 身份即在线）语义不同——此处表达「告警视角下该服是否可达」。
			res.Server = &AlertContextServer{
				ServerID: e.ServerID, Online: !containsString(detail.Reasons, healthview.ReasonLost),
				Level: detail.Level, Score: detail.Score, Schedulable: detail.Schedulable,
				Reasons: detail.Reasons, SampledAtMs: detail.SampledAtMs,
			}
		}
	}
	from := time.Now().UTC().Add(-time.Duration(alertContextTimelineWindowHours) * time.Hour)
	// 时间线始终锁定该告警所属 namespace；serverId 非空时再收敛到该台。
	// 与观测范围叠加（Scoped）：受限下不回退、不越界；无 serverId 的集群级告警按 namespace 退化。
	f := repository.AlertEventFilter{
		Namespace:      e.Namespace,
		NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All,
		From: from, Page: 1, Size: alertContextTimelineLimit,
	}
	if e.ServerID != "" {
		f.ServerID = e.ServerID
	}
	items, _, err := s.repo.List(f)
	if err != nil {
		return nil, err
	}
	res.Timeline = items
	return res, nil
}

// containsString 判断字符串切片是否含目标值。
func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// NewAlertEventService 构造服务。db + auditRepo 供处理工作流在同事务内原子更新状态并写审计（FR-157）。
func NewAlertEventService(db *gorm.DB, repo *repository.AlertEventRepository, auditRepo *repository.AuditLogRepository) *AlertEventService {
	return &AlertEventService{db: db, repo: repo, auditRepo: auditRepo}
}

// Record 落库一条告警事件；未显式指定处理状态时默认 open（新告警即待处理，FR-157）。
// FR-232 收敛：health-transition 类按收敛键 (namespace, serverId, type, toStatus) 合并——存在未恢复行时
// 只 occurrence_count+1、刷新 last_at、取最高级（不插新行、不把 acknowledged 回退 open）；否则插新行。
// created_at 交由 GORM 全局 NowFunc 统一填 UTC（不在此设时间，保与全表一致）。
func (s *AlertEventService) Record(e *model.AlertEvent) error {
	if e.Status == "" {
		e.Status = model.AlertEventStatusOpen
	}
	if e.OccurrenceCount <= 0 {
		e.OccurrenceCount = 1
	}
	if e.Type == model.AlertEventTypeHealthTransition {
		existing, err := s.repo.FindUnresolvedByDedupKey(e.Namespace, e.ServerID, e.Type, e.ToStatus)
		if err == nil && existing != nil {
			return s.mergeOccurrence(existing, e)
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	now := time.Now().UTC()
	e.LastAt = &now
	return s.repo.Create(e)
}

// mergeOccurrence 把再次触发的同类告警并入既有未恢复行（FR-232）：计数 +1、刷新 last_at、取最高级。
// 状态保持不变（已 acknowledged 的条目再触发不回退 open）；人工覆盖过级别的条目不因自动合并改级。
func (s *AlertEventService) mergeOccurrence(existing, incoming *model.AlertEvent) error {
	now := time.Now().UTC()
	existing.OccurrenceCount++
	existing.LastAt = &now
	if existing.SeverityOverride == "" {
		existing.Level = maxAlertLevel(existing.Level, incoming.Level)
	}
	return s.repo.Save(existing)
}

// maxAlertLevel 取两个级别中更高者（FR-231 合并时取最高级）；未知级别视作最低。
func maxAlertLevel(a, b string) string {
	if alertLevelRank(b) > alertLevelRank(a) {
		return b
	}
	return a
}

// alertLevelRank 级别严重度排序权重（info < warning < critical）。
func alertLevelRank(level string) int {
	switch level {
	case model.AlertLevelCritical:
		return 3
	case model.AlertLevelWarning:
		return 2
	case model.AlertLevelInfo:
		return 1
	default:
		return 0
	}
}

// AutoResolveAlerts 在实例恢复 online 时把其全部未恢复告警自动置为 resolved（FR-232），
// handled_by=system、note 标明自动消解，使 UI 能区分系统自动消解 vs 人工已处理。返回受影响行数。
// 一条批量 UPDATE，不逐条写审计（量大；自动消解语义由 note 表达）。
func (s *AlertEventService) AutoResolveAlerts(namespace, serverID string) (int64, error) {
	if namespace == "" || serverID == "" {
		return 0, nil
	}
	return s.repo.AutoResolveByServer(namespace, serverID, time.Now().UTC(), "实例恢复 online，自动消解")
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

// HandleBatch 按过滤条件批量处理「未处理（open）」告警（FR-229）：一条 UPDATE 仅影响 open 行，
// 同事务内写一条批量审计（条件 + 命中数 + 操作者）。返回受影响行数；已非 open 的行不变，故重复执行幂等。
func (s *AlertEventService) HandleBatch(f repository.AlertEventFilter, action, note, operator, clientIP string) (int64, error) {
	status, err := resolveAlertActionStatus(action)
	if err != nil {
		return 0, err
	}
	var affected int64
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		n, err := s.repo.WithTx(tx).HandleBatch(f, status, operator, note, time.Now().UTC())
		if err != nil {
			return err
		}
		affected = n
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator:   operator,
			Action:     model.ActionAlertEventBatchHandled,
			TargetType: model.TargetTypeAlertEvent,
			TargetRef:  "batch",
			Detail:     alertBatchHandleAuditDetail(f, status, affected, note),
			Result:     model.ResultOK,
			ClientIP:   clientIP,
		})
	})
	if txErr != nil {
		return 0, txErr
	}
	return affected, nil
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

// resolveAlertActionStatus 只要目标状态（不产审计动作），供批量处理复用动作归一。
func resolveAlertActionStatus(action string) (string, error) {
	status, _, err := resolveAlertAction(action)
	return status, err
}

// alertBatchHandleAuditDetail 组装批量处理审计 detail（json 文本）：筛选条件 + 目标状态 + 命中数 + 说明。
func alertBatchHandleAuditDetail(f repository.AlertEventFilter, status string, affected int64, note string) string {
	raw, _ := json.Marshal(map[string]any{
		"filter": map[string]any{
			"type": f.Type, "level": f.Level, "namespace": f.Namespace,
			"namespaceCodes": f.NamespaceCodes, "scoped": f.Scoped,
			"from": f.From, "to": f.To,
		},
		"status": status, "affected": affected, "note": note,
	})
	return string(raw)
}

// alertHandleAuditDetail 组装处理审计 detail（json 文本）：目标状态 + 处置说明。
// 说明为运维自填的处置原因，非凭据，原样记录供追溯。
func alertHandleAuditDetail(status, handleNote string) string {
	raw, _ := json.Marshal(map[string]string{"status": status, "note": handleNote})
	return string(raw)
}

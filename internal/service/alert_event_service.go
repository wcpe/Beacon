package service

import (
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// 告警事件分页默认与上限（FR-89）。
const (
	defaultAlertEventPageSize = 20
	maxAlertEventPageSize     = 200
)

// AlertEventService 提供告警事件的持久化与查询（FR-89，见 ADR-0041）。
// Record 供告警扇出的持久化通道调用落库；List 供管理台「事件」页只读查询。
type AlertEventService struct {
	repo *repository.AlertEventRepository
}

// NewAlertEventService 构造服务。
func NewAlertEventService(repo *repository.AlertEventRepository) *AlertEventService {
	return &AlertEventService{repo: repo}
}

// Record 落库一条告警事件。created_at 交由 GORM 全局 NowFunc 统一填 UTC（不在此设时间，保与全表一致）。
func (s *AlertEventService) Record(e *model.AlertEvent) error {
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

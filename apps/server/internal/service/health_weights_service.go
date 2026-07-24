package service

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// SettingKeyHealthWeights 是健康权重配置在运维设置 store 的镜像 key（FR-147，spec §4.4）。
// 专用 key、不进标量白名单：回放真源是 health_weights_rev 版本表，设置行仅为「当前值」镜像。
const SettingKeyHealthWeights = "health.weights"

// HealthWeightsSnapshot 是当前生效的权重配置快照（含版本号）。
// 热更采用整对象原子替换（指针交换）：计算轮读到的配置内部一致，无半新半旧（§4.7）。
type HealthWeightsSnapshot struct {
	Rev    int
	Config HealthWeightsConfig
}

// HealthWeightsService 编排健康权重配置的版本化存储与热更（FR-147，见 §3.3/§4.4）：
// 启动载入最新 rev 到内存（表空则种子 rev=1 默认配置）；Update 校验 → 事务内写设置镜像 +
// 插入新 rev + 写审计 → 提交后内存原子替换（健康计算下一轮即生效）。
type HealthWeightsService struct {
	db          *gorm.DB
	repo        *repository.HealthWeightsRepository
	settingRepo *repository.SettingRepository
	auditRepo   *repository.AuditLogRepository

	mu      sync.RWMutex
	current *HealthWeightsSnapshot
}

// NewHealthWeightsService 构造服务并载入当前配置：表空先种子默认配置 rev=1（operator=system），
// 保证任何被快照 / 决策引用的 rev 均可回放。载入失败返回错误由上层 fail-fast。
func NewHealthWeightsService(db *gorm.DB, repo *repository.HealthWeightsRepository,
	settingRepo *repository.SettingRepository, auditRepo *repository.AuditLogRepository) (*HealthWeightsService, error) {
	s := &HealthWeightsService{db: db, repo: repo, settingRepo: settingRepo, auditRepo: auditRepo}
	latest, err := repo.Latest()
	if err != nil {
		return nil, err
	}
	if latest == nil {
		if latest, err = s.seedDefault(); err != nil {
			return nil, err
		}
		slog.Info("健康权重配置表为空，已种子默认配置", "rev", latest.Rev)
	}
	snapshot, err := snapshotFromRow(latest)
	if err != nil {
		return nil, err
	}
	s.current = snapshot
	return s, nil
}

// seedDefault 种子插入 §4.4 全默认配置（rev=1、operator=system）并同步设置镜像（单事务）。
func (s *HealthWeightsService) seedDefault() (*model.HealthWeightsRev, error) {
	raw, err := json.Marshal(DefaultHealthWeightsConfig())
	if err != nil {
		return nil, err
	}
	var row *model.HealthWeightsRev
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		if row, txErr = s.repo.WithTx(tx).InsertNext(string(raw), "system"); txErr != nil {
			return txErr
		}
		_, txErr = s.settingRepo.WithTx(tx).Upsert(SettingKeyHealthWeights, string(raw), model.SettingValueTypeString)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// Current 返回当前生效配置快照（值拷贝，读端无锁竞争负担）。
func (s *HealthWeightsService) Current() HealthWeightsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.current
}

// Update 全量替换权重配置：应用层校验 → 事务内写设置镜像 + 插入新 rev + 写审计 →
// 提交成功后内存原子替换（热更下一轮健康计算生效）。非法配置回 ErrInvalidHealthWeights，无半写。
func (s *HealthWeightsService) Update(cfg HealthWeightsConfig, operator, clientIP string) error {
	if err := validateHealthWeightsConfig(cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var row *model.HealthWeightsRev
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if _, txErr := s.settingRepo.WithTx(tx).Upsert(SettingKeyHealthWeights, string(raw), model.SettingValueTypeString); txErr != nil {
			return txErr
		}
		var txErr error
		if row, txErr = s.repo.WithTx(tx).InsertNext(string(raw), operator); txErr != nil {
			return txErr
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionHealthWeightsUpdate,
			TargetType: model.TargetTypeHealthWeights, TargetRef: SettingKeyHealthWeights,
			Detail: string(raw), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.current = &HealthWeightsSnapshot{Rev: row.Rev, Config: cfg}
	s.mu.Unlock()
	slog.Info("健康权重配置已热更", "rev", row.Rev, "operator", operator)
	return nil
}

// HealthWeightsRevView 是单个版本的对外视图（json 形状对齐 contracts HealthWeightsRev）。
type HealthWeightsRevView struct {
	Rev       int                 `json:"rev"`
	Config    HealthWeightsConfig `json:"config"`
	Operator  string              `json:"operator"`
	CreatedAt time.Time           `json:"createdAt"`
}

// HealthWeightsOverview 是 GET /admin/v2/settings/health-weights 的响应视图
// （json 形状对齐 contracts HealthWeightsResponse：当前配置 + 历史 rev 升序列表）。
type HealthWeightsOverview struct {
	Current HealthWeightsRevView   `json:"current"`
	History []HealthWeightsRevView `json:"history"`
}

// Overview 组装当前配置 + 全部历史版本（rev 升序，current 为最新一行）。
func (s *HealthWeightsService) Overview() (*HealthWeightsOverview, error) {
	rows, err := s.repo.ListAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		// 构造期已种子，此处为防御性兜底（如被人工清表）。
		return nil, apperr.ErrInternal
	}
	history := make([]HealthWeightsRevView, 0, len(rows))
	for i := range rows {
		view, viewErr := revViewFromRow(&rows[i])
		if viewErr != nil {
			return nil, viewErr
		}
		history = append(history, view)
	}
	return &HealthWeightsOverview{Current: history[len(history)-1], History: history}, nil
}

// snapshotFromRow 把版本行反序列化为内存快照。
func snapshotFromRow(row *model.HealthWeightsRev) (*HealthWeightsSnapshot, error) {
	var cfg HealthWeightsConfig
	if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
		return nil, err
	}
	return &HealthWeightsSnapshot{Rev: row.Rev, Config: cfg}, nil
}

// revViewFromRow 把版本行转为对外视图（config json 展开为嵌套对象）。
func revViewFromRow(row *model.HealthWeightsRev) (HealthWeightsRevView, error) {
	var cfg HealthWeightsConfig
	if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
		return HealthWeightsRevView{}, err
	}
	return HealthWeightsRevView{Rev: row.Rev, Config: cfg, Operator: row.Operator, CreatedAt: row.CreatedAt}, nil
}

// validateHealthWeightsConfig 按 §4.4 校验配置：权重全非负、good/bad 边界有序、
// connSoftLimit>0、alertPenalty≥0、0<degradedMin<healthyMin≤100；违规回 ErrInvalidHealthWeights。
func validateHealthWeightsConfig(cfg HealthWeightsConfig) error {
	w, n, l := cfg.Weights, cfg.Normalize, cfg.Levels
	valid := w.TPS >= 0 && w.CPU >= 0 && w.Capacity >= 0 && w.Conn >= 0 && w.Latency >= 0 && w.Alert >= 0 &&
		n.TPSGood > n.TPSBad &&
		n.CPUGood < n.CPUBad &&
		n.CapGood < n.CapBad &&
		n.LatGoodMs < n.LatBadMs &&
		n.ConnSoftLimit > 0 &&
		n.AlertPenalty >= 0 &&
		l.DegradedMin > 0 && l.DegradedMin < l.HealthyMin && l.HealthyMin <= 100
	if !valid {
		return apperr.ErrInvalidHealthWeights
	}
	return nil
}

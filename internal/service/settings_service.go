package service

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/httpx"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/pkg/log"
	"github.com/wcpe/Beacon/internal/repository"
)

// SettingsService 编排运维设置 store（FR-61，见 ADR-0038）：热改项真源由 config.yml 移到 DB store。
// 持进程内内存缓存（启动 GetAll 载入，RWMutex），消费者高频读走缓存、不每次打 DB；
// Update 校验白名单 + 类型 / 范围 → Upsert → 刷缓存 → 审计（detail 仅 key + 新值，绝不含密钥）→ log.level 即时改日志级别。
type SettingsService struct {
	db        *gorm.DB
	repo      *repository.SettingRepository
	auditRepo *repository.AuditLogRepository

	mu    sync.RWMutex
	cache map[string]string // key → 字符串化值；缺则取白名单默认
}

// NewSettingsService 构造服务并从库载入全量缓存（启动装配）。
// 载入失败返回错误由上层 fail-fast（设置读不上来则消费者只能用默认，宁可启动期暴露）。
func NewSettingsService(db *gorm.DB, repo *repository.SettingRepository, auditRepo *repository.AuditLogRepository) (*SettingsService, error) {
	s := &SettingsService{db: db, repo: repo, auditRepo: auditRepo, cache: make(map[string]string)}
	all, err := repo.GetAll()
	if err != nil {
		return nil, err
	}
	for _, item := range all {
		s.cache[item.Key] = item.Value
	}
	return s, nil
}

// GetInt 取 int 型设置值（走缓存，缺 / 解析失败则用白名单默认）。
func (s *SettingsService) GetInt(key string) int {
	raw, ok := s.cachedOrDefault(key)
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		// 理论上缓存值已过校验，解析失败仅防御性兜底：回退到 config 默认。
		if def, dok := s.defaultValue(key); dok {
			if dv, derr := strconv.Atoi(def); derr == nil {
				return dv
			}
		}
		return 0
	}
	return v
}

// GetBool 取 bool 型设置值（走缓存，缺 / 解析失败则用白名单默认）。
func (s *SettingsService) GetBool(key string) bool {
	raw, ok := s.cachedOrDefault(key)
	if !ok {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		if def, dok := s.defaultValue(key); dok {
			if dv, derr := strconv.ParseBool(def); derr == nil {
				return dv
			}
		}
		return false
	}
	return v
}

// GetString 取 string 型设置值（走缓存，缺则用白名单默认）。
func (s *SettingsService) GetString(key string) string {
	raw, _ := s.cachedOrDefault(key)
	return raw
}

// cachedOrDefault 取缓存值，缺则回退白名单默认；key 不在白名单返回 ("", false)。
func (s *SettingsService) cachedOrDefault(key string) (string, bool) {
	if _, ok := settingMetaFor(key); !ok {
		return "", false
	}
	s.mu.RLock()
	v, hit := s.cache[key]
	s.mu.RUnlock()
	if hit {
		return v, true
	}
	return s.defaultValue(key)
}

// defaultValue 取某 key 的白名单默认（从内置 config 默认派生），key 不在白名单返回 ("", false)。
func (s *SettingsService) defaultValue(key string) (string, bool) {
	meta, ok := settingMetaFor(key)
	if !ok {
		return "", false
	}
	return meta.defaultFromConfig(config.Default()), true
}

// Update 更新单个热改项：白名单 + 类型 / 范围 / 枚举校验 → Upsert → 刷缓存 → 审计 → log.level 即时改日志级别。
// 非白名单 key → ErrSettingKeyNotAllowed；非法值 → ErrSettingValueInvalid。
func (s *SettingsService) Update(key, value, operator, clientIP string) error {
	meta, ok := settingMetaFor(key)
	if !ok {
		return apperr.ErrSettingKeyNotAllowed
	}
	// 含凭据项「未改密码」语义（FR-98，见 ADR-0047）：前端回显的是脱敏值，若用户原样提交脱敏占位
	// （等于当前值的脱敏形态），视为「未改」——保留 store 原值不覆盖、不入审计，避免把脱敏占位写成真值。
	if isSecretSettingKey(key) {
		current, _ := s.cachedOrDefault(key)
		if value == httpx.RedactURLCredentials(current) && value != current {
			return nil
		}
	}
	if err := validateSettingValue(meta, value); err != nil {
		return err
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 在外层事务内复用 tx 做 Upsert（经 WithTx），避免嵌套开事务在单连接池下死锁。
		if _, e := s.repo.WithTx(tx).Upsert(key, value, meta.valueType); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionSettingsUpdate,
			TargetType: model.TargetTypeSettings, TargetRef: key,
			Detail: settingAuditDetail(key, value), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.cache[key] = value
	s.mu.Unlock()

	// log.level 走 atomic 级别 setter 即时生效（不重建 logger）。
	if key == SettingLogLevel {
		log.SetLevel(value)
	}
	slog.Info("运维设置已更新", "key", key, "operator", operator)
	return nil
}

// SeedFromConfig 首启种子：对每个热改 key，store 无该 key 才用 config.yml 值 Upsert（已有以 store 为准，不覆盖）。
// 之后 store 为热改项真源、config.yml 仅作出厂默认 / 首启种子（FR-61，见 ADR-0038 决策 5）。
func (s *SettingsService) SeedFromConfig(cfg config.Config) error {
	keys := make([]string, 0, len(settingsWhitelist))
	for k := range settingsWhitelist {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 稳定顺序（仅便于日志 / 测试可读，无功能意义）
	for _, key := range keys {
		s.mu.RLock()
		_, hit := s.cache[key]
		s.mu.RUnlock()
		if hit {
			continue // store 已有该 key，不覆盖
		}
		meta := settingsWhitelist[key]
		value := meta.defaultFromConfig(cfg)
		if _, err := s.repo.Upsert(key, value, meta.valueType); err != nil {
			return err
		}
		s.mu.Lock()
		s.cache[key] = value
		s.mu.Unlock()
		slog.Info("首启种子写入设置", "key", key)
	}
	return nil
}

// SettingView 是单个热改项对外视图（供前端 FR-62）：当前值 + 类型 + 默认 + 说明 + isStartup=false。
type SettingView struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ValueType string `json:"valueType"`
	Default   string `json:"default"`
	Desc      string `json:"desc"`
	IsStartup bool   `json:"isStartup"` // 恒 false：白名单内皆热改项；启动 / 安全项绝不进 store / API
}

// List 返回全部热改项当前值 + 类型 + 默认 + 说明（按 key 字典序，供前端稳定渲染）。
func (s *SettingsService) List() []SettingView {
	keys := make([]string, 0, len(settingsWhitelist))
	for k := range settingsWhitelist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	views := make([]SettingView, 0, len(keys))
	for _, key := range keys {
		meta := settingsWhitelist[key]
		value, _ := s.cachedOrDefault(key)
		// 含凭据项回前端时脱敏（FR-98，见 ADR-0047）：落库存原值供运行，对外只回显脱敏值。
		if isSecretSettingKey(key) {
			value = httpx.RedactURLCredentials(value)
		}
		views = append(views, SettingView{
			Key: key, Value: value, ValueType: meta.valueType,
			Default: meta.defaultFromConfig(config.Default()), Desc: meta.desc, IsStartup: false,
		})
	}
	return views
}

// validateSettingValue 按白名单元数据校验值：int 解析 + 范围、bool 解析、string 枚举（nil 不限）。
func validateSettingValue(meta settingMeta, value string) error {
	switch meta.valueType {
	case model.SettingValueTypeInt:
		v, err := strconv.Atoi(value)
		if err != nil {
			return apperr.ErrSettingValueInvalid
		}
		if v < meta.min || v > meta.max {
			return apperr.ErrSettingValueInvalid
		}
	case model.SettingValueTypeBool:
		if _, err := strconv.ParseBool(value); err != nil {
			return apperr.ErrSettingValueInvalid
		}
	case model.SettingValueTypeString:
		if meta.enumOK != nil && !meta.enumOK(value) {
			return apperr.ErrSettingValueInvalid
		}
	default:
		return apperr.ErrSettingValueInvalid
	}
	return nil
}

// settingAuditDetail 组装设置更新审计 detail（json 文本）：仅 key + 新值。
// 多数 key 白名单不含密钥（auth.* / agent-token / git-export token 绝不进 store），value 直记安全；
// 但含凭据项（如 update.proxy-url，FR-98 见 ADR-0047）的 value 可能含 user:pass，须脱敏后再记。
func settingAuditDetail(key, value string) string {
	recorded := value
	if isSecretSettingKey(key) {
		recorded = httpx.RedactURLCredentials(value)
	}
	raw, _ := json.Marshal(map[string]string{"key": key, "value": recorded})
	return string(raw)
}

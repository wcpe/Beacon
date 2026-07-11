package service

import (
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// newTestHealthWeightsDB 打开内存 sqlite 并迁移权重版本 / 设置 / 审计三表（单测快路，不依赖 MySQL）。
func newTestHealthWeightsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:hw_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	// 单连接：sqlite shared-cache 下避免并发写 "table is locked"。
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.HealthWeightsRev{}, &model.Setting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newTestHealthWeightsService 装配权重服务（空表 → 构造期种子 rev=1 默认配置）。
func newTestHealthWeightsService(t *testing.T) (*HealthWeightsService, *gorm.DB) {
	t.Helper()
	db := newTestHealthWeightsDB(t)
	svc, err := NewHealthWeightsService(db, repository.NewHealthWeightsRepository(db),
		repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("装配权重服务失败: %v", err)
	}
	return svc, db
}

// TestValidateHealthWeightsConfigMatrix 穷举校验矩阵：每条规则逐一违反均应拒绝（§4.4 校验）。
func TestValidateHealthWeightsConfigMatrix(t *testing.T) {
	if err := validateHealthWeightsConfig(DefaultHealthWeightsConfig()); err != nil {
		t.Fatalf("默认配置应合法: %v", err)
	}
	违规 := []struct {
		名称 string
		变换 func(*HealthWeightsConfig)
	}{
		{"权重tps为负", func(c *HealthWeightsConfig) { c.Weights.TPS = -1 }},
		{"权重cpu为负", func(c *HealthWeightsConfig) { c.Weights.CPU = -0.5 }},
		{"权重capacity为负", func(c *HealthWeightsConfig) { c.Weights.Capacity = -1 }},
		{"权重conn为负", func(c *HealthWeightsConfig) { c.Weights.Conn = -1 }},
		{"权重latency为负", func(c *HealthWeightsConfig) { c.Weights.Latency = -1 }},
		{"权重alert为负", func(c *HealthWeightsConfig) { c.Weights.Alert = -1 }},
		{"tpsGood不大于tpsBad", func(c *HealthWeightsConfig) { c.Normalize.TPSGood = c.Normalize.TPSBad }},
		{"cpuGood不小于cpuBad", func(c *HealthWeightsConfig) { c.Normalize.CPUGood = c.Normalize.CPUBad }},
		{"capGood不小于capBad", func(c *HealthWeightsConfig) { c.Normalize.CapGood = 0.96 }},
		{"latGood不小于latBad", func(c *HealthWeightsConfig) { c.Normalize.LatGoodMs = 500 }},
		{"connSoftLimit为0", func(c *HealthWeightsConfig) { c.Normalize.ConnSoftLimit = 0 }},
		{"connSoftLimit为负", func(c *HealthWeightsConfig) { c.Normalize.ConnSoftLimit = -1 }},
		{"alertPenalty为负", func(c *HealthWeightsConfig) { c.Normalize.AlertPenalty = -1 }},
		{"degradedMin为0", func(c *HealthWeightsConfig) { c.Levels.DegradedMin = 0 }},
		{"degradedMin不小于healthyMin", func(c *HealthWeightsConfig) { c.Levels.DegradedMin = 80 }},
		{"healthyMin超100", func(c *HealthWeightsConfig) { c.Levels.HealthyMin = 101 }},
	}
	for _, c := range 违规 {
		t.Run(c.名称, func(t *testing.T) {
			cfg := DefaultHealthWeightsConfig()
			c.变换(&cfg)
			if err := validateHealthWeightsConfig(cfg); !errors.Is(err, apperr.ErrInvalidHealthWeights) {
				t.Fatalf("应拒绝为 invalid_health_weights，实际 %v", err)
			}
		})
	}
}

// TestHealthWeightsSeedOnEmpty 校验空表构造期种子：rev=1、operator=system、内存为默认配置、设置镜像同步。
func TestHealthWeightsSeedOnEmpty(t *testing.T) {
	svc, db := newTestHealthWeightsService(t)
	cur := svc.Current()
	if cur.Rev != 1 || cur.Config != DefaultHealthWeightsConfig() {
		t.Fatalf("种子后当前配置应为默认 rev=1，实际 %+v", cur)
	}
	var row model.HealthWeightsRev
	if err := db.First(&row, "rev = ?", 1).Error; err != nil {
		t.Fatalf("种子行应存在: %v", err)
	}
	if row.Operator != "system" {
		t.Fatalf("种子 operator 应为 system，实际 %s", row.Operator)
	}
	var setting model.Setting
	if err := db.First(&setting, "setting_key = ?", SettingKeyHealthWeights).Error; err != nil {
		t.Fatalf("设置镜像应存在: %v", err)
	}
	if setting.Value != row.Config {
		t.Fatal("设置镜像应与 rev 行 config 一致")
	}
	// 再次构造（表非空）不重复种子。
	svc2, err := NewHealthWeightsService(db, repository.NewHealthWeightsRepository(db),
		repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("二次装配失败: %v", err)
	}
	if svc2.Current().Rev != 1 {
		t.Fatalf("表非空不应重复种子，rev 应仍为 1，实际 %d", svc2.Current().Rev)
	}
}

// TestHealthWeightsUpdateFlow 校验 Update：新 rev 递增 + 审计 + 镜像 + 内存热更；非法配置整体拒绝无半写。
func TestHealthWeightsUpdateFlow(t *testing.T) {
	svc, db := newTestHealthWeightsService(t)

	cfg := DefaultHealthWeightsConfig()
	cfg.Weights.TPS = 35
	cfg.Weights.CPU = 15
	if err := svc.Update(cfg, "ops-chen", "127.0.0.1"); err != nil {
		t.Fatalf("合法更新失败: %v", err)
	}
	cur := svc.Current()
	if cur.Rev != 2 || cur.Config.Weights.TPS != 35 {
		t.Fatalf("热更后应 rev=2 且配置生效，实际 %+v", cur)
	}
	var auditCount int64
	if err := db.Model(&model.AuditLog{}).
		Where("action = ?", model.ActionHealthWeightsUpdate).Count(&auditCount).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("应有 1 条权重热更审计，实际 %d", auditCount)
	}
	overview, err := svc.Overview()
	if err != nil {
		t.Fatalf("Overview 失败: %v", err)
	}
	if overview.Current.Rev != 2 || len(overview.History) != 2 || overview.History[0].Rev != 1 {
		t.Fatalf("Overview 应 current=rev2、历史升序 2 条，实际 %+v", overview)
	}
	if overview.Current.Config != cfg {
		t.Fatal("Overview 回读配置应与提交一致")
	}

	// 非法配置：拒绝且无半写（rev 不增、审计不增、内存不变）。
	bad := DefaultHealthWeightsConfig()
	bad.Levels.DegradedMin = 90
	if err := svc.Update(bad, "ops-chen", "127.0.0.1"); !errors.Is(err, apperr.ErrInvalidHealthWeights) {
		t.Fatalf("非法配置应拒绝，实际 %v", err)
	}
	var revCount int64
	if err := db.Model(&model.HealthWeightsRev{}).Count(&revCount).Error; err != nil {
		t.Fatalf("查 rev 行数失败: %v", err)
	}
	if revCount != 2 {
		t.Fatalf("非法更新不应产生新 rev，行数应 2，实际 %d", revCount)
	}
	if svc.Current().Rev != 2 {
		t.Fatal("非法更新不应改内存配置")
	}
}

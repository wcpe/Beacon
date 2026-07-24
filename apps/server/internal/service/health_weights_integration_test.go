//go:build integration

package service

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestHealthWeightsUpdateRoundTripMySQL 真 MySQL：种子 → PUT 后 rev 递增 + 审计行存在 +
// GET 回读一致 + 非法配置被拒且无半写（rev / 审计 / 设置镜像均不变）。
func TestHealthWeightsUpdateRoundTripMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr147_weights")

	svc, err := NewHealthWeightsService(db, repository.NewHealthWeightsRepository(db),
		repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("装配权重服务失败: %v", err)
	}
	if svc.Current().Rev != 1 {
		t.Fatalf("空表应种子 rev=1，实际 %d", svc.Current().Rev)
	}

	// PUT 合法配置：rev 递增、审计入库、回读一致。
	cfg := DefaultHealthWeightsConfig()
	cfg.Weights.TPS = 40
	cfg.Levels.HealthyMin = 85
	if err := svc.Update(cfg, "it-ops", "10.0.0.1"); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	overview, err := svc.Overview()
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if overview.Current.Rev != 2 || overview.Current.Config != cfg || overview.Current.Operator != "it-ops" {
		t.Fatalf("回读应 rev=2 且与提交一致，实际 %+v", overview.Current)
	}
	if len(overview.History) != 2 || overview.History[0].Rev != 1 || overview.History[0].Operator != "system" {
		t.Fatalf("历史应含种子 rev=1，实际 %+v", overview.History)
	}
	var auditCount int64
	if err := db.Model(&model.AuditLog{}).
		Where("action = ? AND operator = ?", model.ActionHealthWeightsUpdate, "it-ops").Count(&auditCount).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("应有 1 条权重热更审计，实际 %d", auditCount)
	}
	var mirror model.Setting
	if err := db.First(&mirror, "setting_key = ?", SettingKeyHealthWeights).Error; err != nil {
		t.Fatalf("设置镜像应存在: %v", err)
	}

	// 非法配置：拒绝且无半写。
	bad := DefaultHealthWeightsConfig()
	bad.Normalize.ConnSoftLimit = 0
	if err := svc.Update(bad, "it-ops", "10.0.0.1"); !errors.Is(err, apperr.ErrInvalidHealthWeights) {
		t.Fatalf("非法配置应拒绝，实际 %v", err)
	}
	var revCount int64
	if err := db.Model(&model.HealthWeightsRev{}).Count(&revCount).Error; err != nil {
		t.Fatalf("查 rev 行数失败: %v", err)
	}
	if revCount != 2 {
		t.Fatalf("非法更新后 rev 行数应仍为 2，实际 %d", revCount)
	}
	var mirrorAfter model.Setting
	if err := db.First(&mirrorAfter, "setting_key = ?", SettingKeyHealthWeights).Error; err != nil {
		t.Fatalf("查设置镜像失败: %v", err)
	}
	if mirrorAfter.Value != mirror.Value {
		t.Fatal("非法更新不应改设置镜像")
	}
}

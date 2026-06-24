//go:build integration

package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// TestRepoSoftDeleteSetEnabledRowsAffected 集成验证 M2 直接守卫：
// repo SoftDelete / SetEnabled 命中 0 行（目标已被并发软删）即返回 not-found，而非静默 nil。
// 这是「幽灵审计」防线的源头：批量调用方据此回滚，不写实际未发生的审计。
func TestRepoSoftDeleteSetEnabledRowsAffected(t *testing.T) {
	cfg, _, db := newStack(t)
	repo := repository.NewConfigItemRepository(db, noEncryptCipher())

	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "ghost.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "x: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}

	// 先软删一次（命中 1 行，成功）
	if err := repo.SoftDelete(item.ID, time.Now().UTC()); err != nil {
		t.Fatalf("首次软删应成功: %v", err)
	}
	// 再软删同一 id（已不在 active，命中 0 行）→ not-found
	if err := repo.SoftDelete(item.ID, time.Now().UTC()); !errors.Is(err, apperr.ErrConfigNotFound) {
		t.Fatalf("软删已删项应返回 CONFIG_NOT_FOUND，实际 %v", err)
	}
	// 置态同一已删 id（命中 0 行）→ not-found
	if err := repo.SetEnabled(item.ID, true); !errors.Is(err, apperr.ErrConfigNotFound) {
		t.Fatalf("置态已删项应返回 CONFIG_NOT_FOUND，实际 %v", err)
	}
}

// TestConfigBatchMutateRollbackNoAudit 集成验证 M1+M2 叠加：批量含不存在 id 时整批回滚——
// 已存在项未被改动、且不写任何审计（全成或全不成，不留幽灵审计）。
func TestConfigBatchMutateRollbackNoAudit(t *testing.T) {
	cfg, _, db := newStack(t)

	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "rollback.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "x: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}

	// 建项产生一条 config.create 审计，先记基线，验证批量失败后审计数不增。
	var before int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigDisable).Count(&before)

	// 批量禁用含不存在 id（999999）→ 整批 404（FindByIDs 预取数 < 去重 id 数）
	err = cfg.BatchSetEnabled([]uint{item.ID, 999999}, false, "bob", "")
	if !errors.Is(err, apperr.ErrConfigNotFound) {
		t.Fatalf("批中含不存在 id 应返回 CONFIG_NOT_FOUND，实际 %v", err)
	}

	// 已存在项未被改动：仍 enabled（默认建项为 true）
	got, err := cfg.Get(item.ID)
	if err != nil {
		t.Fatalf("取回配置失败: %v", err)
	}
	if !got.Enabled {
		t.Fatal("整批回滚后已存在项不应被禁用")
	}

	// 不写任何 config.disable 审计（回滚后无幽灵审计）
	var after int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigDisable).Count(&after)
	if after != before {
		t.Fatalf("整批回滚不应写 config.disable 审计，前 %d 后 %d", before, after)
	}
}

// TestConfigBatchMutateDedupIDs 集成验证去重：重复 id 不绕过存在性校验、按去重后实际项执行。
func TestConfigBatchMutateDedupIDs(t *testing.T) {
	cfg, _, db := newStack(t)

	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "dedup.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "x: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}

	// 同一 id 重复三次 → 去重后只一项，批量禁用成功、只记一条审计
	if err := cfg.BatchSetEnabled([]uint{item.ID, item.ID, item.ID}, false, "bob", ""); err != nil {
		t.Fatalf("去重批量禁用应成功，实际 %v", err)
	}
	var n int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigDisable).Count(&n)
	if n != 1 {
		t.Fatalf("去重后应只记 1 条 config.disable 审计，实际 %d", n)
	}
	got, _ := cfg.Get(item.ID)
	if got.Enabled {
		t.Fatal("去重批量禁用后项应为 disabled")
	}
}

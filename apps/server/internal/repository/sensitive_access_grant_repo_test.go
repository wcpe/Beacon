package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestSensitiveAccessGrantConsumeOnce 验证授权只允许原申请主体在有效期内成功消费一次。
func TestSensitiveAccessGrantConsumeOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_grant?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	repo := NewSensitiveAccessGrantRepository(db)
	now := time.Now().UTC()
	grant := &model.SensitiveAccessGrant{
		GrantID: "sag_test", ApprovalRequestID: "apr_test", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice",
		Operation: "agent.command.tail_logs", TargetRef: "prod/server-a", ContentVersionHash: "hash", MaxUses: 1,
		ExpiresAt: now.Add(time.Minute), Status: model.SensitiveAccessGrantStatusActive,
	}
	if err := repo.Create(grant); err != nil {
		t.Fatalf("创建授权失败：%v", err)
	}
	if err := repo.Consume(grant.GrantID, auth.HumanPrincipal("bob"), now); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("非原申请主体应拒绝：%v", err)
	}
	if err := repo.Consume(grant.GrantID, auth.HumanPrincipal("alice"), now); err != nil {
		t.Fatalf("首次消费应成功：%v", err)
	}
	if err := repo.Consume(grant.GrantID, auth.HumanPrincipal("alice"), now); !errors.Is(err, apperr.ErrSensitiveAccessConsumed) {
		t.Fatalf("重复消费应拒绝：%v", err)
	}
}

// TestSensitiveAccessGrantActivatesFrozenTarget 验证激活必须匹配冻结的审批、操作、目标与版本哈希。
func TestSensitiveAccessGrantActivatesFrozenTarget(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_activate?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	repo := NewSensitiveAccessGrantRepository(db)
	grant := &model.SensitiveAccessGrant{
		GrantID: "sag_pending", ApprovalRequestID: "apr_pending", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice",
		Operation: "agent.command.fs_browse", TargetRef: "prod/server-a", ContentVersionHash: "v1", MaxUses: 1,
		Status: model.SensitiveAccessGrantStatusPending,
	}
	if err := repo.Create(grant); err != nil {
		t.Fatalf("创建待激活授权失败：%v", err)
	}
	if _, err := repo.Activate(grant.ApprovalRequestID, grant.Operation, grant.TargetRef, "other", time.Now().UTC().Add(time.Minute)); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("版本哈希不符不得激活：%v", err)
	}
	activated, err := repo.Activate(grant.ApprovalRequestID, grant.Operation, grant.TargetRef, grant.ContentVersionHash, time.Now().UTC().Add(time.Minute))
	if err != nil || !activated {
		t.Fatalf("冻结目标匹配应激活，activated=%v err=%v", activated, err)
	}
}

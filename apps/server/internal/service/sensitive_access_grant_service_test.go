package service

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
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestSensitiveAccessGrantServiceBindsFrozenFacts 验证 service 不允许借其它目标或主体激活、消费授权。
func TestSensitiveAccessGrantServiceBindsFrozenFacts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_service?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	repo := repository.NewSensitiveAccessGrantRepository(db)
	grant := &model.SensitiveAccessGrant{GrantID: "sag_service", ApprovalRequestID: "apr_service", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", Operation: "agent.command.tail_logs", TargetRef: "prod/server-a", ContentVersionHash: "v1", MaxUses: 1, Status: model.SensitiveAccessGrantStatusPending}
	if err := repo.Create(grant); err != nil {
		t.Fatalf("创建授权失败：%v", err)
	}
	svc := NewSensitiveAccessGrantService(repo)
	now := time.Now().UTC()
	if err := svc.Activate(grant.ApprovalRequestID, auth.HumanPrincipal("bob"), grant.Operation, grant.TargetRef, grant.ContentVersionHash, now); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("他人不得激活：%v", err)
	}
	if err := svc.Activate(grant.ApprovalRequestID, auth.HumanPrincipal("alice"), grant.Operation, grant.TargetRef, grant.ContentVersionHash, now); err != nil {
		t.Fatalf("原申请人激活失败：%v", err)
	}
	if err := svc.Consume(grant.GrantID, auth.HumanPrincipal("alice"), grant.Operation, "prod/server-b", grant.ContentVersionHash, now.Add(time.Second)); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("目标漂移不得消费：%v", err)
	}
	if err := svc.Consume(grant.GrantID, auth.HumanPrincipal("alice"), grant.Operation, grant.TargetRef, grant.ContentVersionHash, now.Add(time.Second)); err != nil {
		t.Fatalf("冻结事实匹配应消费成功：%v", err)
	}
}

// TestSensitiveAccessGrantServiceWithTx 验证审批适配器可把授权激活纳入同一事务。
func TestSensitiveAccessGrantServiceWithTx(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_service_tx?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	repo := repository.NewSensitiveAccessGrantRepository(db)
	grant := &model.SensitiveAccessGrant{GrantID: "sag_service_tx", ApprovalRequestID: "apr_service_tx", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", Operation: "message.payload.read", TargetRef: "message/msg-42", ContentVersionHash: "v1", MaxUses: 1, Status: model.SensitiveAccessGrantStatusPending}
	if err := repo.Create(grant); err != nil {
		t.Fatalf("创建授权失败：%v", err)
	}
	svc := NewSensitiveAccessGrantService(repo)
	now := time.Now().UTC()
	rollback := errors.New("强制回滚")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := svc.WithTx(tx).Activate(grant.ApprovalRequestID, auth.HumanPrincipal("alice"), grant.Operation, grant.TargetRef, grant.ContentVersionHash, now); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("事务应回滚：%v", err)
	}
	stored, err := repo.FindByID(grant.GrantID)
	if err != nil || stored.Status != model.SensitiveAccessGrantStatusPending {
		t.Fatalf("回滚后不应激活授权：grant=%+v err=%v", stored, err)
	}
}

// TestSensitiveAccessGrantServiceCreatesPendingInTx 验证申请期可在同一事务创建唯一 pending 授权。
func TestSensitiveAccessGrantServiceCreatesPendingInTx(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_service_pending?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	svc := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	var grant *model.SensitiveAccessGrant
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		grant, err = svc.WithTx(tx).CreatePending("apr_pending", auth.PrincipalKindHuman, "alice", "message.payload.read", "message/msg-42", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		return err
	}); err != nil {
		t.Fatalf("创建 pending grant 失败：%v", err)
	}
	if grant == nil || grant.GrantID == "" || grant.Status != model.SensitiveAccessGrantStatusPending || grant.MaxUses != 1 {
		t.Fatalf("pending grant 字段不完整：%+v", grant)
	}
	if _, err := svc.CreatePending("apr_pending", auth.PrincipalKindHuman, "alice", "message.payload.read", "message/msg-42", grant.ContentVersionHash); err == nil {
		t.Fatal("同一审批请求不得创建第二份授权")
	}
}

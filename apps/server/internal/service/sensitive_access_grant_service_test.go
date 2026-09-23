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
	if err := svc.Consume(grant.GrantID, auth.HumanPrincipal("alice"), grant.Operation, "prod/server-b", grant.ContentVersionHash, now.Add(time.Second)); !errors.Is(err, apperr.ErrSensitiveAccessTargetDrift) {
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

// TestSensitiveAccessGrantServiceDistinguishesFailures 验证授权服务的四类前置失败各自返回
// 可区分的错误码，而不是统一的泛化 403。
//
// 设计意图核查（为何不是「有意的不泄露」）：
//  1. 同文件内的 createPending 对同类参数校验已返回 ErrInvalidParam(400)，Activate/Consume 却报 403——同类不同码，是漏改而非设计；
//  2. repo 层与 Consume 的后续路径本就返回 ErrSensitiveAccessExpired/Consumed/WrongPrincipal 三种精确码，
//     即系统已选择「对原申请主体精确告知授权状态」；
//  3. 外部调用方（如 POST /admin/v2/assets/pair-read/grants/{grantId}/consume）拿到 403 会去查权限，
//     而真实原因可能是 grantId 拼错或审批后目标漂移。
//
// 仍保留的收敛：「授权不存在」与「已过期」HTTP 状态码同为 410，但响应体 code 仍区分处置方向
// （见 ErrSensitiveAccessNotFound 注释）。
func TestSensitiveAccessGrantServiceDistinguishesFailures(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sensitive_access_distinguish?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移授权表失败：%v", err)
	}
	repo := repository.NewSensitiveAccessGrantRepository(db)
	svc := NewSensitiveAccessGrantService(repo)
	now := time.Now().UTC()

	// 装配缺失（nil 服务）→ 500，而非把服务端故障甩给调用方。
	var nilSvc *SensitiveAccessGrantService
	if err := nilSvc.Consume("sag_x", auth.HumanPrincipal("alice"), "op", "ref", "hash", now); !errors.Is(err, apperr.ErrInternal) {
		t.Fatalf("未装配应报 INTERNAL，实际 %v", err)
	}

	// 缺参数 → 400，而非 403。
	if err := svc.Consume("sag_x", auth.HumanPrincipal("alice"), "", "ref", "hash", now); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 operation 应报 INVALID_PARAM，实际 %v", err)
	}

	// 授权不存在 → 410（与「已失效」同码，保留防枚举）。
	if err := svc.Consume("sag_missing", auth.HumanPrincipal("alice"), "op", "ref", "hash", now); !errors.Is(err, apperr.ErrSensitiveAccessNotFound) {
		t.Fatalf("授权不存在应报 NOT_FOUND(410)，实际 %v", err)
	}

	// 冻结事实漂移 → 409，指明须重新提审。
	grant := &model.SensitiveAccessGrant{GrantID: "sag_drift", ApprovalRequestID: "apr_drift", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", Operation: "op", TargetRef: "prod/server-a", ContentVersionHash: "v1", MaxUses: 1, Status: model.SensitiveAccessGrantStatusActive, ExpiresAt: now.Add(time.Minute)}
	if err := repo.Create(grant); err != nil {
		t.Fatalf("创建授权失败：%v", err)
	}
	if err := svc.Consume(grant.GrantID, auth.HumanPrincipal("alice"), "op", "prod/server-OTHER", "v1", now); !errors.Is(err, apperr.ErrSensitiveAccessTargetDrift) {
		t.Fatalf("目标漂移应报 TARGET_DRIFT(409)，实际 %v", err)
	}

	// 以上四类都不得是泛化 403。
	for _, e := range []error{apperr.ErrInternal, apperr.ErrInvalidParam, apperr.ErrSensitiveAccessNotFound, apperr.ErrSensitiveAccessTargetDrift} {
		if e == apperr.ErrForbidden {
			t.Fatal("不应复用泛化 FORBIDDEN")
		}
	}
}

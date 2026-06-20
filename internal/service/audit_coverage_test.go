package service

import (
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"beacon/internal/model"
	"beacon/internal/repository"
)

// newAuditTestDB 打开内存 sqlite 并迁移审计相关表（无需 MySQL DSN，单测快路）。
func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestNamespaceCreateWritesAudit 守护 FR-7/FR-30：建环境在同事务内产一条 namespace.create 审计，
// operator / clientIP 落库，detail 仅记环境名、不含敏感数据。
func TestNamespaceCreateWritesAudit(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewNamespaceService(db, repository.NewNamespaceRepository(db), repository.NewAuditLogRepository(db))

	ns, err := svc.Create("staging", "预发布", "alice", "203.0.113.1")
	if err != nil {
		t.Fatalf("建环境应成功，实际 %v", err)
	}
	if ns.Code != "staging" {
		t.Fatalf("应回显 code=staging，实际 %q", ns.Code)
	}

	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionNamespaceCreate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条 namespace.create 审计，实际 %d", len(logs))
	}
	got := logs[0]
	if got.Operator != "alice" {
		t.Fatalf("审计 operator 应为 alice，实际 %q", got.Operator)
	}
	if got.TargetType != model.TargetTypeNamespace || got.TargetRef != "staging" {
		t.Fatalf("审计 target 应为 namespace/staging，实际 %s/%s", got.TargetType, got.TargetRef)
	}
	if got.NamespaceCode != "staging" {
		t.Fatalf("审计 namespaceCode 应为 staging，实际 %q", got.NamespaceCode)
	}
	if got.ClientIP != "203.0.113.1" {
		t.Fatalf("审计 clientIp 应落库，实际 %q", got.ClientIP)
	}
	if got.Result != model.ResultOK {
		t.Fatalf("审计 result 应为 ok，实际 %q", got.Result)
	}
	if !strings.Contains(got.Detail, "预发布") {
		t.Fatalf("审计 detail 应含环境名，实际 %q", got.Detail)
	}
}

// TestNamespaceCreateConflictNoAudit 边界：重复 code 冲突时不应留下审计（事务未提交业务写、也无审计）。
func TestNamespaceCreateConflictNoAudit(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewNamespaceService(db, repository.NewNamespaceRepository(db), repository.NewAuditLogRepository(db))

	if _, err := svc.Create("dup", "环境", "alice", "10.0.0.1"); err != nil {
		t.Fatalf("首次建环境应成功，实际 %v", err)
	}
	if _, err := svc.Create("dup", "再次", "bob", "10.0.0.2"); err == nil {
		t.Fatal("重复 code 应返回冲突错误")
	}

	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", model.ActionNamespaceCreate).Count(&n).Error; err != nil {
		t.Fatalf("计数审计失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("冲突不应产生额外审计，应恒为 1 条，实际 %d", n)
	}
}

// TestAuthAuditNoSecretLeak 守护安全底线：登录 / 登出审计的 detail 严禁含口令 / 令牌。
func TestAuthAuditNoSecretLeak(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewAuthAuditService(repository.NewAuditLogRepository(db))

	if err := svc.RecordLogin("admin", "203.0.113.2"); err != nil {
		t.Fatalf("记登录审计应成功，实际 %v", err)
	}
	if err := svc.RecordLogout("admin", "203.0.113.2"); err != nil {
		t.Fatalf("记登出审计应成功，实际 %v", err)
	}

	cases := []struct {
		action string
	}{{model.ActionAuthLogin}, {model.ActionAuthLogout}}
	for _, c := range cases {
		var logs []model.AuditLog
		if err := db.Where("action = ?", c.action).Find(&logs).Error; err != nil {
			t.Fatalf("查 %s 审计失败: %v", c.action, err)
		}
		if len(logs) != 1 {
			t.Fatalf("应有 1 条 %s 审计，实际 %d", c.action, len(logs))
		}
		got := logs[0]
		if got.Operator != "admin" || got.TargetType != model.TargetTypeAuth {
			t.Fatalf("%s 审计 operator/targetType 不符：%s/%s", c.action, got.Operator, got.TargetType)
		}
		for _, secret := range []string{"password", "token", "secret"} {
			if strings.Contains(got.Detail, secret) {
				t.Fatalf("%s 审计 detail 不得含敏感字样 %q，实际 %q", c.action, secret, got.Detail)
			}
		}
	}
}

package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
)

// newAuditTestDB 打开内存 sqlite 并迁移 audit_log，供过滤/分页单测（不依赖 MySQL/DSN）。
func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("迁移 audit_log 失败: %v", err)
	}
	// 清表，避免共享内存库残留串扰
	if err := db.Exec("DELETE FROM audit_log").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

// seed 追加一条审计（指定 operator/action/createdAt）。
func seed(t *testing.T, r *AuditLogRepository, operator, action string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: action,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: model.ResultOK, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditListFilterByOperator 验证 FR-30 新增的「按操作者」过滤维度。
func TestAuditListFilterByOperator(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seed(t, r, "alice", model.ActionConfigCreate, base)
	seed(t, r, "bob", model.ActionConfigPublish, base.Add(time.Minute))
	seed(t, r, "alice", model.ActionConfigPublish, base.Add(2*time.Minute))

	// 仅按 operator=alice 过滤 → 2 条（Page/Size 由调用方/服务层规整，单测直连仓库须显式给定）
	items, total, err := r.List(AuditFilter{Operator: "alice", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("operator=alice 应 2 条，实际 total=%d len=%d", total, len(items))
	}
	for _, it := range items {
		if it.Operator != "alice" {
			t.Fatalf("结果含非 alice 操作者: %s", it.Operator)
		}
	}

	// operator 与 action 叠加过滤 → alice 的 publish 仅 1 条
	_, both, err := r.List(AuditFilter{Operator: "alice", Action: model.ActionConfigPublish})
	if err != nil {
		t.Fatalf("叠加查询失败: %v", err)
	}
	if both != 1 {
		t.Fatalf("operator=alice & action=publish 应 1 条，实际 %d", both)
	}

	// 空 operator 不过滤 → 全量 3 条
	_, all, _ := r.List(AuditFilter{})
	if all != 3 {
		t.Fatalf("空过滤应返回全量 3 条，实际 %d", all)
	}
}

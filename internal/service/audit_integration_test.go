//go:build integration

package service_test

import (
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
)

// TestAuditList 集成验证：审计按 action/targetType 过滤、分页、时间倒序。
func TestAuditList(t *testing.T) {
	db := testDB(t)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	cfg := service.NewConfigService(db, cr, repository.NewConfigRevisionRepository(db, noEncryptCipher()), ar)
	zone := service.NewZoneService(db, asg, nil, ar, runtime.NewRegistry())
	audit := service.NewAuditService(ar)

	// 产生若干审计：建（config.create）→ 发布（config.publish）→ 指派（zone.assign）
	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML, Content: "k: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	if _, err := cfg.Publish(item.ID, "k: 2\n", "bob", "", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if _, err := zone.Assign("prod", "s1", "area1", "zoneA", "carol", "", ""); err != nil {
		t.Fatalf("指派失败: %v", err)
	}

	// 全量（prod）至少 3 条，且时间倒序：最新一条为 zone.assign
	all, total, err := audit.List(repository.AuditFilter{Namespace: "prod"})
	if err != nil || total < 3 {
		t.Fatalf("全量审计应 >=3，实际 total=%d err=%v", total, err)
	}
	if all[0].Action != model.ActionZoneAssign {
		t.Fatalf("时间倒序首条应为 zone.assign，实际 %s", all[0].Action)
	}

	// 按 action 过滤
	if _, pt, _ := audit.List(repository.AuditFilter{Action: model.ActionConfigPublish}); pt != 1 {
		t.Fatalf("config.publish 审计应 1 条，实际 %d", pt)
	}
	// 按 operator 过滤（FR-30 新增维度）：bob 仅发布 1 次
	if _, ot, _ := audit.List(repository.AuditFilter{Operator: "bob"}); ot != 1 {
		t.Fatalf("operator=bob 审计应 1 条，实际 %d", ot)
	}
	// operator 与 action 叠加：bob 的 config.publish 1 条
	if _, bt, _ := audit.List(repository.AuditFilter{Operator: "bob", Action: model.ActionConfigPublish}); bt != 1 {
		t.Fatalf("operator=bob & config.publish 应 1 条，实际 %d", bt)
	}
	// 按 targetType 过滤
	if _, zt, _ := audit.List(repository.AuditFilter{TargetType: model.TargetTypeZone}); zt != 1 {
		t.Fatalf("zone 类型审计应 1 条，实际 %d", zt)
	}
	// 分页 size=1：当页 1 条，total 不变
	page1, pTotal, _ := audit.List(repository.AuditFilter{Namespace: "prod", Page: 1, Size: 1})
	if len(page1) != 1 || pTotal != total {
		t.Fatalf("分页 size=1 应返回 1 条且 total=%d，实际 len=%d total=%d", total, len(page1), pTotal)
	}
}

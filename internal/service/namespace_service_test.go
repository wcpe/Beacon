package service

import (
	"errors"
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// stubInstanceCounter 是注册表计数器的测试替身：按 namespace 返回预置实例条目数。
type stubInstanceCounter struct {
	counts map[string]int
}

func (s stubInstanceCounter) CountByNamespace(ns string) int { return s.counts[ns] }

// newNamespaceTestDB 打开**每个用例独立**的内存 sqlite 并迁移 namespace CRUD 守卫所需的全部表。
// 用 t.Name() 作库名 + 单连接，避免与同包其它用例共享一个内存库导致数据串扰（按全局动作计数审计时尤甚）。
func newNamespaceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}
	// 单连接保活：内存库随连接存活，多连接会各自见到空库
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.Namespace{}, &model.AuditLog{}, &model.ZoneAssignment{}, &model.ConfigItem{},
		&model.FileObject{}, &model.FileOverrideSet{},
	); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newNamespaceService 用给定注册表计数器装配服务（多数用例无在线实例，传空 counter）。
func newNamespaceService(db *gorm.DB, counter instanceCounter) *NamespaceService {
	return NewNamespaceService(
		db,
		repository.NewNamespaceRepository(db),
		repository.NewZoneAssignmentRepository(db),
		repository.NewConfigItemRepository(db, nil),
		repository.NewFileObjectRepository(db),
		repository.NewFileOverrideSetRepository(db),
		counter,
		repository.NewAuditLogRepository(db),
	)
}

// emptyCounter 是无在线实例的计数器替身。
func emptyCounter() instanceCounter { return stubInstanceCounter{counts: map[string]int{}} }

// seedNamespace 预置一个环境，供改名 / 删除用例复用。
func seedNamespace(t *testing.T, svc *NamespaceService, code, name string) {
	t.Helper()
	if _, err := svc.Create(code, name, "seed", "10.0.0.1"); err != nil {
		t.Fatalf("预置环境 %q 应成功，实际 %v", code, err)
	}
}

// auditCount 统计某动作的审计条数。
func auditCount(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("计数审计失败: %v", err)
	}
	return n
}

// TestNamespaceUpdateRenamesAndAudits 守护 FR-53：改名成功落库且产一条 namespace.update 审计。
func TestNamespaceUpdateRenamesAndAudits(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "prod", "生产")

	ns, err := svc.Update("prod", "生产环境", "alice", "203.0.113.5")
	if err != nil {
		t.Fatalf("改名应成功，实际 %v", err)
	}
	if ns.Name != "生产环境" || ns.Code != "prod" {
		t.Fatalf("应回显 code=prod name=生产环境，实际 %s/%s", ns.Code, ns.Name)
	}

	var got model.Namespace
	if err := db.Where("code = ?", "prod").First(&got).Error; err != nil {
		t.Fatalf("查环境失败: %v", err)
	}
	if got.Name != "生产环境" {
		t.Fatalf("库内 name 应已更新为 生产环境，实际 %q", got.Name)
	}

	if n := auditCount(t, db, model.ActionNamespaceUpdate); n != 1 {
		t.Fatalf("应有 1 条 namespace.update 审计，实际 %d", n)
	}
}

// TestNamespaceUpdateNotFound 边界：改不存在的环境返回 NOT_FOUND 且不留审计。
func TestNamespaceUpdateNotFound(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())

	_, err := svc.Update("ghost", "随便", "alice", "10.0.0.2")
	if !errors.Is(err, apperr.ErrNamespaceNotFound) {
		t.Fatalf("应返回 ErrNamespaceNotFound，实际 %v", err)
	}
	if n := auditCount(t, db, model.ActionNamespaceUpdate); n != 0 {
		t.Fatalf("改不存在环境不应留审计，实际 %d", n)
	}
}

// TestNamespaceDeleteEmptyAllowed 正常路径：环境内无实例 / 无 zone / 无配置时可删，记一条 namespace.delete。
func TestNamespaceDeleteEmptyAllowed(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "scratch", "临时")

	if err := svc.Delete("scratch", "alice", "203.0.113.6"); err != nil {
		t.Fatalf("空环境应可删，实际 %v", err)
	}

	var n int64
	if err := db.Model(&model.Namespace{}).Where("code = ?", "scratch").Count(&n).Error; err != nil {
		t.Fatalf("计数环境失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("环境应已被硬删，实际仍剩 %d 行", n)
	}
	if c := auditCount(t, db, model.ActionNamespaceDelete); c != 1 {
		t.Fatalf("应有 1 条 namespace.delete 审计，实际 %d", c)
	}
}

// TestNamespaceDeleteNotFound 边界：删不存在的环境返回 NOT_FOUND 且不留审计。
func TestNamespaceDeleteNotFound(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())

	err := svc.Delete("ghost", "alice", "10.0.0.3")
	if !errors.Is(err, apperr.ErrNamespaceNotFound) {
		t.Fatalf("应返回 ErrNamespaceNotFound，实际 %v", err)
	}
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("删不存在环境不应留审计，实际 %d", n)
	}
}

// TestNamespaceDeleteBlockedByInstances 守卫①：该环境下有已注册实例则禁删（专门错误），且不留审计、不删行。
func TestNamespaceDeleteBlockedByInstances(t *testing.T) {
	db := newNamespaceTestDB(t)
	counter := stubInstanceCounter{counts: map[string]int{"prod": 2}}
	svc := newNamespaceService(db, counter)
	seedNamespace(t, svc, "prod", "生产")

	err := svc.Delete("prod", "alice", "10.0.0.4")
	if !errors.Is(err, apperr.ErrNamespaceHasInstances) {
		t.Fatalf("有实例应返回 ErrNamespaceHasInstances，实际 %v", err)
	}
	assertNamespaceKept(t, db, "prod")
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("拒删不应留 delete 审计，实际 %d", n)
	}
}

// TestNamespaceDeleteBlockedByAssignments 守卫②：该环境下有未软删 zone 指派则禁删（专门错误）。
func TestNamespaceDeleteBlockedByAssignments(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "prod", "生产")
	if _, err := repository.NewZoneAssignmentRepository(db).
		Upsert("prod", "srv-1", "gA", "z1", ""); err != nil {
		t.Fatalf("预置 zone 指派失败: %v", err)
	}

	err := svc.Delete("prod", "alice", "10.0.0.5")
	if !errors.Is(err, apperr.ErrNamespaceHasAssignments) {
		t.Fatalf("有 zone 指派应返回 ErrNamespaceHasAssignments，实际 %v", err)
	}
	assertNamespaceKept(t, db, "prod")
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("拒删不应留 delete 审计，实际 %d", n)
	}
}

// TestNamespaceDeleteBlockedByConfigs 守卫③：该环境下有未软删配置项则禁删（专门错误）。
func TestNamespaceDeleteBlockedByConfigs(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "prod", "生产")
	if err := repository.NewConfigItemRepository(db, nil).Create(&model.ConfigItem{
		NamespaceCode: "prod",
		GroupCode:     model.GlobalGroupCode,
		DataID:        "mysql.yml",
		ScopeLevel:    model.ScopeGlobal,
		Content:       "k: v",
		ContentMD5:    "x",
	}); err != nil {
		t.Fatalf("预置配置项失败: %v", err)
	}

	err := svc.Delete("prod", "alice", "10.0.0.6")
	if !errors.Is(err, apperr.ErrNamespaceHasConfigs) {
		t.Fatalf("有配置应返回 ErrNamespaceHasConfigs，实际 %v", err)
	}
	assertNamespaceKept(t, db, "prod")
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("拒删不应留 delete 审计，实际 %d", n)
	}
}

// TestNamespaceDeleteBlockedByFiles 守卫④：该环境下有未软删文件树（通道B）则禁删（专门错误），不留审计、不删行。
func TestNamespaceDeleteBlockedByFiles(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "prod", "生产")
	if err := repository.NewFileObjectRepository(db).Create(&model.FileObject{
		NamespaceCode: "prod",
		GroupCode:     model.GlobalGroupCode,
		Path:          "ui/main.allin",
		ScopeLevel:    model.ScopeGlobal,
		Content:       "x",
		ContentMD5:    "x",
	}); err != nil {
		t.Fatalf("预置文件对象失败: %v", err)
	}

	err := svc.Delete("prod", "alice", "10.0.0.7")
	if !errors.Is(err, apperr.ErrNamespaceHasFiles) {
		t.Fatalf("有文件树应返回 ErrNamespaceHasFiles，实际 %v", err)
	}
	assertNamespaceKept(t, db, "prod")
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("拒删不应留 delete 审计，实际 %d", n)
	}
}

// TestNamespaceDeleteBlockedByOverrideSets 守卫⑤：该环境下有未软删覆盖集（FR-15）则禁删（专门错误），不留审计、不删行。
func TestNamespaceDeleteBlockedByOverrideSets(t *testing.T) {
	db := newNamespaceTestDB(t)
	svc := newNamespaceService(db, emptyCounter())
	seedNamespace(t, svc, "prod", "生产")
	if err := repository.NewFileOverrideSetRepository(db).Create(&model.FileOverrideSet{
		NamespaceCode: "prod",
		GroupCode:     model.GlobalGroupCode,
		Name:          "AllinCore",
		ScopeLevel:    model.ScopeGlobal,
		TargetRoot:    "plugins/AllinCore",
	}); err != nil {
		t.Fatalf("预置覆盖集失败: %v", err)
	}

	err := svc.Delete("prod", "alice", "10.0.0.8")
	if !errors.Is(err, apperr.ErrNamespaceHasOverrideSets) {
		t.Fatalf("有覆盖集应返回 ErrNamespaceHasOverrideSets，实际 %v", err)
	}
	assertNamespaceKept(t, db, "prod")
	if n := auditCount(t, db, model.ActionNamespaceDelete); n != 0 {
		t.Fatalf("拒删不应留 delete 审计，实际 %d", n)
	}
}

// assertNamespaceKept 断言被拒删的环境仍在库中。
func assertNamespaceKept(t *testing.T, db *gorm.DB, code string) {
	t.Helper()
	var n int64
	if err := db.Model(&model.Namespace{}).Where("code = ?", code).Count(&n).Error; err != nil {
		t.Fatalf("计数环境失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("被拒删的环境 %q 应仍在库，实际剩 %d 行", code, n)
	}
}

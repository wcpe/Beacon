package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// newEnvTestDB 打开每个用例独立的内存 sqlite 并迁移 env 相关表（env / env_namespace / namespace / audit_log）。
// 用 t.Name() 作库名 + 单连接，避免同包用例共享内存库串扰（按全局动作计数审计时尤甚）。
func newEnvTestDB(t *testing.T) *gorm.DB {
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
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Env{}, &model.EnvNamespace{}, &model.Namespace{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newEnvService 装配 env 服务。
func newEnvService(db *gorm.DB) *EnvService {
	return NewEnvService(db, repository.NewEnvRepository(db), repository.NewNamespaceRepository(db), repository.NewAuditLogRepository(db))
}

// seedEnvNamespace 直插一个 namespace（code == name），返回其 id 供映射用例引用。
func seedEnvNamespace(t *testing.T, db *gorm.DB, code string) uint {
	t.Helper()
	ns := &model.Namespace{Code: code, Name: code}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("预置 namespace %q 失败: %v", code, err)
	}
	return ns.ID
}

// countAudits 按动作计审计条数。
func countAudits(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("计数审计 %q 失败: %v", action, err)
	}
	return n
}

// mustAppErr 断言 err 为携带指定 code / status 的 apperr。
func mustAppErr(t *testing.T, err error, code string, status int) *apperr.Error {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("应为 apperr，实际 %v", err)
	}
	if ae.Code != code || ae.Status != status {
		t.Fatalf("应为 code=%s status=%d，实际 code=%s status=%d", code, status, ae.Code, ae.Status)
	}
	return ae
}

// TestEnvCreateAndList 建 env 并列出：返回含空映射摘要；写一条 env.create 审计。
func TestEnvCreateAndList(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)

	view, err := svc.Create("生产", "生产展示维度", "alice", "203.0.113.1")
	if err != nil {
		t.Fatalf("建 env 应成功，实际 %v", err)
	}
	if view.ID == 0 || view.Name != "生产" || view.NamespaceCount != 0 || view.Namespaces == nil {
		t.Fatalf("建 env 视图不符：%+v", view)
	}

	views, err := svc.List()
	if err != nil {
		t.Fatalf("列 env 应成功，实际 %v", err)
	}
	if len(views) != 1 || views[0].Name != "生产" || len(views[0].Namespaces) != 0 {
		t.Fatalf("列 env 结果不符：%+v", views)
	}

	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionEnvCreate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条 env.create 审计，实际 %d", len(logs))
	}
	got := logs[0]
	if got.Operator != "alice" || got.TargetType != model.TargetTypeEnv || got.TargetRef != fmt.Sprintf("%d", view.ID) {
		t.Fatalf("审计元数据不符：%+v", got)
	}
	if got.ClientIP != "203.0.113.1" || got.Result != model.ResultOK || !strings.Contains(got.Detail, "生产") {
		t.Fatalf("审计内容不符：%+v", got)
	}
}

// TestEnvCreateEmptyNameRejected 边界：名为空返回参数错误、不落审计。
func TestEnvCreateEmptyNameRejected(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)

	if _, err := svc.Create("   ", "", "alice", "10.0.0.1"); err == nil {
		t.Fatal("空名应返回参数错误")
	} else {
		_ = mustAppErr(t, err, apperr.ErrInvalidParam.Code, 400)
	}
	if n := countAudits(t, db, model.ActionEnvCreate); n != 0 {
		t.Fatalf("空名不应产生审计，实际 %d", n)
	}
}

// TestEnvCreateDuplicateRejected 边界：同名 env 冲突返回 409，且不留额外审计。
func TestEnvCreateDuplicateRejected(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)

	if _, err := svc.Create("dup", "", "alice", "10.0.0.1"); err != nil {
		t.Fatalf("首次建 env 应成功，实际 %v", err)
	}
	if _, err := svc.Create("dup", "", "bob", "10.0.0.2"); err == nil {
		t.Fatal("同名应返回冲突")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvConflict.Code, 409)
	}
	if n := countAudits(t, db, model.ActionEnvCreate); n != 1 {
		t.Fatalf("冲突不应产生额外审计，应恒为 1，实际 %d", n)
	}
}

// TestEnvUpdate 改 env 名 / 描述：局部更新 + 写 env.update 审计；不存在 404；撞名 409。
func TestEnvUpdate(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)

	created, err := svc.Create("测试", "旧描述", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 env 应成功，实际 %v", err)
	}
	other, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建第二个 env 应成功，实际 %v", err)
	}

	newName := "预发布"
	newDesc := "新描述"
	updated, err := svc.Update(created.ID, &newName, &newDesc, "bob", "10.0.0.2")
	if err != nil {
		t.Fatalf("更新 env 应成功，实际 %v", err)
	}
	if updated.Name != "预发布" || updated.Description != "新描述" {
		t.Fatalf("更新结果不符：%+v", updated)
	}
	if n := countAudits(t, db, model.ActionEnvUpdate); n != 1 {
		t.Fatalf("应有 1 条 env.update 审计，实际 %d", n)
	}

	// 只改描述（name=nil 不动名）
	descOnly := "仅改描述"
	partial, err := svc.Update(created.ID, nil, &descOnly, "bob", "10.0.0.2")
	if err != nil {
		t.Fatalf("仅改描述应成功，实际 %v", err)
	}
	if partial.Name != "预发布" || partial.Description != "仅改描述" {
		t.Fatalf("仅改描述结果不符：%+v", partial)
	}

	// 不存在
	if _, err := svc.Update(9999, &newName, nil, "bob", "10.0.0.2"); err == nil {
		t.Fatal("更新不存在 env 应 404")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvNotFound.Code, 404)
	}

	// 撞另一个 env 的名
	conflictName := "生产"
	if _, err := svc.Update(created.ID, &conflictName, nil, "bob", "10.0.0.2"); err == nil {
		t.Fatal("改名撞名应 409")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvConflict.Code, 409)
	}
	_ = other
}

// TestEnvSetNamespacesReplaceSemantics 整体替换语义：先删后插、幂等；每次写 env.set-namespaces 审计。
func TestEnvSetNamespacesReplaceSemantics(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)
	ns1 := seedEnvNamespace(t, db, "prod")
	ns2 := seedEnvNamespace(t, db, "test")
	ns3 := seedEnvNamespace(t, db, "staging")

	env, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 env 应成功，实际 %v", err)
	}

	// 初次映射 {ns1, ns2}
	view, err := svc.SetNamespaces(env.ID, []uint{ns1, ns2}, "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("设置映射应成功，实际 %v", err)
	}
	if view.NamespaceCount != 2 {
		t.Fatalf("映射应 2 个，实际 %d", view.NamespaceCount)
	}
	if n := mappingCount(t, db, env.ID); n != 2 {
		t.Fatalf("DB 映射行应 2，实际 %d", n)
	}

	// 整体替换为 {ns2, ns3}：ns1 移除、ns3 新增
	if _, err := svc.SetNamespaces(env.ID, []uint{ns2, ns3}, "alice", "10.0.0.1"); err != nil {
		t.Fatalf("替换映射应成功，实际 %v", err)
	}
	got := mappedNamespaceIDs(t, db, env.ID)
	if len(got) != 2 || !got[ns2] || !got[ns3] || got[ns1] {
		t.Fatalf("替换后映射应为 {ns2,ns3}，实际 %v", got)
	}

	// 幂等：同集合再设一次，仍 2 行、不报错
	if _, err := svc.SetNamespaces(env.ID, []uint{ns2, ns3}, "alice", "10.0.0.1"); err != nil {
		t.Fatalf("幂等替换应成功，实际 %v", err)
	}
	if n := mappingCount(t, db, env.ID); n != 2 {
		t.Fatalf("幂等后映射行应恒 2，实际 %d", n)
	}

	// 置空映射
	empty, err := svc.SetNamespaces(env.ID, []uint{}, "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("置空映射应成功，实际 %v", err)
	}
	if empty.NamespaceCount != 0 {
		t.Fatalf("置空后应 0 映射，实际 %d", empty.NamespaceCount)
	}

	if n := countAudits(t, db, model.ActionEnvSetNamespaces); n != 4 {
		t.Fatalf("应有 4 条 env.set-namespaces 审计，实际 %d", n)
	}
}

// TestEnvSetNamespacesDedup 去重：重复 namespace id 归一，不产生重复映射行。
func TestEnvSetNamespacesDedup(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)
	ns1 := seedEnvNamespace(t, db, "prod")
	ns2 := seedEnvNamespace(t, db, "test")

	env, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 env 应成功，实际 %v", err)
	}
	view, err := svc.SetNamespaces(env.ID, []uint{ns1, ns1, ns2, ns2}, "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("设置映射应成功，实际 %v", err)
	}
	if view.NamespaceCount != 2 {
		t.Fatalf("去重后应 2 个映射，实际 %d", view.NamespaceCount)
	}
}

// TestEnvSetNamespacesConflictIndicatesParty 一个 namespace 至多属一个 env：被他 env 占用返回 409 且指明冲突方。
func TestEnvSetNamespacesConflictIndicatesParty(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)
	ns1 := seedEnvNamespace(t, db, "prod")

	envA, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 envA 应成功，实际 %v", err)
	}
	envB, err := svc.Create("测试", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 envB 应成功，实际 %v", err)
	}
	if _, err := svc.SetNamespaces(envA.ID, []uint{ns1}, "alice", "10.0.0.1"); err != nil {
		t.Fatalf("envA 映射 ns1 应成功，实际 %v", err)
	}

	// envB 想抢占 ns1 → 409，message 指明冲突方（namespace 名 prod + 占用 env 名 生产）
	_, err = svc.SetNamespaces(envB.ID, []uint{ns1}, "bob", "10.0.0.2")
	if err == nil {
		t.Fatal("抢占已占用 namespace 应 409")
	}
	ae := mustAppErr(t, err, apperr.ErrEnvNamespaceConflict.Code, 409)
	if !strings.Contains(ae.Message, "prod") || !strings.Contains(ae.Message, "生产") {
		t.Fatalf("冲突 message 应指明冲突方（prod / env 生产），实际 %q", ae.Message)
	}

	// envB 的映射未被改动（事务未执行）
	if n := mappingCount(t, db, envB.ID); n != 0 {
		t.Fatalf("冲突后 envB 不应有映射，实际 %d", n)
	}
	// 冲突不产生 envB 的映射审计
	if n := countAudits(t, db, model.ActionEnvSetNamespaces); n != 1 {
		t.Fatalf("冲突不应新增映射审计，应恒为 1，实际 %d", n)
	}
}

// TestEnvSetNamespacesUnknownRejected 边界：待映射 namespace 不存在 400；env 不存在 404。
func TestEnvSetNamespacesUnknownRejected(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)
	ns1 := seedEnvNamespace(t, db, "prod")

	env, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 env 应成功，实际 %v", err)
	}
	if _, err := svc.SetNamespaces(env.ID, []uint{ns1, 9999}, "alice", "10.0.0.1"); err == nil {
		t.Fatal("含不存在 namespace 应 400")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvNamespaceNotFound.Code, 400)
	}
	if _, err := svc.SetNamespaces(9999, []uint{ns1}, "alice", "10.0.0.1"); err == nil {
		t.Fatal("env 不存在应 404")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvNotFound.Code, 404)
	}
}

// TestEnvDeleteCascadesMappings 删 env 级联删除映射：删后被占 namespace 可归其他 env；写 env.delete 审计；不存在 404。
func TestEnvDeleteCascadesMappings(t *testing.T) {
	db := newEnvTestDB(t)
	svc := newEnvService(db)
	ns1 := seedEnvNamespace(t, db, "prod")
	ns2 := seedEnvNamespace(t, db, "test")

	envA, err := svc.Create("生产", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 envA 应成功，实际 %v", err)
	}
	if _, err := svc.SetNamespaces(envA.ID, []uint{ns1, ns2}, "alice", "10.0.0.1"); err != nil {
		t.Fatalf("envA 映射应成功，实际 %v", err)
	}

	if err := svc.Delete(envA.ID, "bob", "10.0.0.2"); err != nil {
		t.Fatalf("删 envA 应成功，实际 %v", err)
	}
	// env 行与其映射行都不在
	if e, err := repository.NewEnvRepository(db).FindByID(envA.ID); err != nil || e != nil {
		t.Fatalf("envA 应已删除，实际 e=%v err=%v", e, err)
	}
	if n := mappingCount(t, db, envA.ID); n != 0 {
		t.Fatalf("envA 映射应级联删除，实际残留 %d", n)
	}
	if n := countAudits(t, db, model.ActionEnvDelete); n != 1 {
		t.Fatalf("应有 1 条 env.delete 审计，实际 %d", n)
	}

	// 释放后 ns1 可归 envB（无冲突）
	envB, err := svc.Create("测试", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 envB 应成功，实际 %v", err)
	}
	if _, err := svc.SetNamespaces(envB.ID, []uint{ns1}, "alice", "10.0.0.1"); err != nil {
		t.Fatalf("释放后 ns1 应可归 envB，实际 %v", err)
	}

	// 删不存在
	if err := svc.Delete(9999, "bob", "10.0.0.2"); err == nil {
		t.Fatal("删不存在 env 应 404")
	} else {
		_ = mustAppErr(t, err, apperr.ErrEnvNotFound.Code, 404)
	}
}

// mappingCount 计某 env 的映射行数。
func mappingCount(t *testing.T, db *gorm.DB, envID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.EnvNamespace{}).Where("env_id = ?", envID).Count(&n).Error; err != nil {
		t.Fatalf("计数映射失败: %v", err)
	}
	return n
}

// mappedNamespaceIDs 取某 env 当前映射的 namespace id 集合。
func mappedNamespaceIDs(t *testing.T, db *gorm.DB, envID uint) map[uint]bool {
	t.Helper()
	var rows []model.EnvNamespace
	if err := db.Where("env_id = ?", envID).Find(&rows).Error; err != nil {
		t.Fatalf("查映射失败: %v", err)
	}
	out := map[uint]bool{}
	for i := range rows {
		out[rows[i].NamespaceID] = true
	}
	return out
}

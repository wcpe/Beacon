package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// newServerTagTestService 建最小可用的内存库与 v2 服务（namespace + server + server_tag + audit）。
func newServerTagTestService(t *testing.T) (*V2ControlPlaneService, *gorm.DB, model.Namespace) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:server_tag?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{}, &model.Server{}, &model.AgentIdentity{}, &model.ServerTag{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	for _, table := range []string{"namespace", "bc_cluster", "region", "zone", "server", "agent_identity", "server_tag", "audit_log"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", table, err)
		}
	}
	ns := model.Namespace{Code: "prod", Name: "prod", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	return NewV2ControlPlaneService(db), db, ns
}

// TestFR227ServerTagsCRUDAndFilter 校验 FR-227：标签按 key 增改 / 删除、列表交集筛选、写审计与校验拒绝。
func TestFR227ServerTagsCRUDAndFilter(t *testing.T) {
	svc, db, ns := newServerTagTestService(t)
	s1 := model.Server{NamespaceID: ns.ID, ServerID: "game-1", Kind: model.ServerKindBackend}
	s2 := model.Server{NamespaceID: ns.ID, ServerID: "game-2", Kind: model.ServerKindBackend}
	for _, s := range []*model.Server{&s1, &s2} {
		if err := db.Create(s).Error; err != nil {
			t.Fatalf("建 server 失败: %v", err)
		}
	}

	// 按 key 增改（game-1 两个标签，game-2 一个）
	if _, err := svc.SetServerTags(SetServerTagsParams{ServerID: "game-1", Tags: map[string]string{"env": "beta", "tier": "core"}, Operator: "admin"}); err != nil {
		t.Fatalf("设置 game-1 标签失败: %v", err)
	}
	if _, err := svc.SetServerTags(SetServerTagsParams{ServerID: "game-2", Tags: map[string]string{"env": "beta"}, Operator: "admin"}); err != nil {
		t.Fatalf("设置 game-2 标签失败: %v", err)
	}

	// 交集筛选：env=beta 命中 2 台；env=beta & tier=core 只命中 game-1
	_, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, Tags: map[string]string{"env": "beta"}})
	if err != nil {
		t.Fatalf("按 env 过滤失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("env=beta 应命中 2 台，实际 %d", total)
	}
	views, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, Tags: map[string]string{"env": "beta", "tier": "core"}})
	if err != nil {
		t.Fatalf("按 env+tier 交集过滤失败: %v", err)
	}
	if total != 1 || len(views) != 1 || views[0].ServerID != "game-1" {
		t.Fatalf("env+tier 交集应只命中 game-1，实际 total=%d views=%+v", total, views)
	}
	if len(views[0].Tags) != 2 || views[0].Tags[0].Key != "env" {
		t.Fatalf("视图应带排序标签，实际 %+v", views[0].Tags)
	}

	// 重复 key 覆盖而非新增行
	if _, err := svc.SetServerTags(SetServerTagsParams{ServerID: "game-2", Tags: map[string]string{"env": "prod"}, Operator: "admin"}); err != nil {
		t.Fatalf("覆盖 game-2 标签失败: %v", err)
	}
	var cnt int64
	db.Model(&model.ServerTag{}).Where("server_pk = ?", s2.ID).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("覆盖同一 key 应仍为 1 行，实际 %d", cnt)
	}

	// 写操作落审计
	var audit model.AuditLog
	if err := db.Where("action = ? AND target_ref = ?", model.ActionServerTagUpdated, "game-2").Order("id ASC").First(&audit).Error; err != nil {
		t.Fatalf("标签写未落审计: %v", err)
	}
	if audit.Detail == "" {
		t.Fatalf("标签审计缺少 key/值明细")
	}

	// 删除单个标签后交集不再命中
	if _, err := svc.DeleteServerTag(DeleteServerTagParams{ServerID: "game-1", TagKey: "tier", Operator: "admin"}); err != nil {
		t.Fatalf("删除 game-1 tier 失败: %v", err)
	}
	_, total, err = svc.ListServers(ListServersParams{NamespaceID: ns.ID, Tags: map[string]string{"tier": "core"}})
	if err != nil {
		t.Fatalf("删除后过滤失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("删除 tier=core 后应 0 命中，实际 %d", total)
	}

	// 非法 key 拒绝
	if _, err := svc.SetServerTags(SetServerTagsParams{ServerID: "game-2", Tags: map[string]string{"bad key!": "x"}, Operator: "admin"}); err != apperr.ErrInvalidParam {
		t.Fatalf("非法 key 应 ErrInvalidParam，实际 %v", err)
	}
	// 不存在的 server → 404 语义
	if _, err := svc.SetServerTags(SetServerTagsParams{ServerID: "nope", Tags: map[string]string{"a": "b"}, Operator: "admin"}); err != apperr.ErrInstanceNotFound {
		t.Fatalf("未知 server 应 ErrInstanceNotFound，实际 %v", err)
	}
}

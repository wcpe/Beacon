package service_test

import (
	"testing"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/store"
)

// fakePublishRecorder 记录发布计数自增次数，用于断言 promote 是否计入。
type fakePublishRecorder struct {
	count int
}

func (f *fakePublishRecorder) IncConfigPublish() { f.count++ }

// graySqliteStack 用内存 sqlite 装配 ConfigService + ConfigGrayService（不连 MySQL，作纯逻辑单测）。
func graySqliteStack(t *testing.T) (*service.ConfigService, *service.ConfigGrayService) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })

	cipher := noEncryptCipher()
	configRepo := repository.NewConfigItemRepository(db, cipher)
	revRepo := repository.NewConfigRevisionRepository(db, cipher)
	grayRepo := repository.NewConfigGrayRepository(db, cipher)
	auditRepo := repository.NewAuditLogRepository(db)

	cfgSvc := service.NewConfigService(db, configRepo, revRepo, auditRepo)
	graySvc := service.NewConfigGrayService(db, cfgSvc, configRepo, grayRepo, auditRepo)
	return cfgSvc, graySvc
}

// TestGrayPromoteIncrementsPublishCounter 灰度 promote 走发布路径，应自增发布计数（FR-30）。
func TestGrayPromoteIncrementsPublishCounter(t *testing.T) {
	cfgSvc, graySvc := graySqliteStack(t)
	rec := &fakePublishRecorder{}
	graySvc.SetMetrics(rec)

	item, err := cfgSvc.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "v: stable\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置项失败: %v", err)
	}
	if _, err := graySvc.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}

	if _, err := graySvc.Promote(item.ID, "bob", "晋升", ""); err != nil {
		t.Fatalf("晋升失败: %v", err)
	}
	if rec.count != 1 {
		t.Fatalf("promote 应自增发布计数 1 次，实际 %d", rec.count)
	}
}

// TestGrayPromoteWithoutMetricsNoPanic 未注入计数器时 promote 不应 panic（可选注入语义）。
func TestGrayPromoteWithoutMetricsNoPanic(t *testing.T) {
	cfgSvc, graySvc := graySqliteStack(t)

	item, err := cfgSvc.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "v: stable\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置项失败: %v", err)
	}
	if _, err := graySvc.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}
	if _, err := graySvc.Promote(item.ID, "bob", "晋升", ""); err != nil {
		t.Fatalf("未注入计数器时晋升应正常: %v", err)
	}
}

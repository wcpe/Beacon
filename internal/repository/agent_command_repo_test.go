package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/model"
)

// newCommandTestDB 打开内存 sqlite 并迁移 agent_command，供仓库单测（不依赖 MySQL/DSN）。
func newCommandTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatalf("迁移 agent_command 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM agent_command").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

func mkPending(ns, serverID string) *model.AgentCommand {
	return &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeIngestPlugins, Payload: `{"scope":"group","group":"area1"}`,
		Status: model.CommandStatusPending, Operator: "admin",
	}
}

// TestCommandCreateAndFindByID 建后可按主键查回，字段往返一致。
func TestCommandCreateAndFindByID(t *testing.T) {
	repo := NewAgentCommandRepository(newCommandTestDB(t))
	cmd := mkPending("prod", "lobby-1")
	if err := repo.Create(cmd); err != nil {
		t.Fatalf("建命令失败: %v", err)
	}
	if cmd.ID == 0 {
		t.Fatal("建后应回填自增 ID")
	}
	got, err := repo.FindByID(cmd.ID)
	if err != nil || got == nil {
		t.Fatalf("按 ID 查命令失败: %v / %v", err, got)
	}
	if got.ServerID != "lobby-1" || got.Type != model.CommandTypeIngestPlugins || got.Status != model.CommandStatusPending {
		t.Fatalf("命令字段不一致: %+v", got)
	}
	miss, err := repo.FindByID(99999)
	if err != nil || miss != nil {
		t.Fatalf("不存在命令应返回 (nil,nil)，实际 %v / %v", miss, err)
	}
}

// TestCommandFindOldestPending 只取该目标的最早 pending；非 pending / 他目标不返回。
func TestCommandFindOldestPending(t *testing.T) {
	repo := NewAgentCommandRepository(newCommandTestDB(t))
	// 先建一条已 done 的（不应被取）
	done := mkPending("prod", "lobby-1")
	done.Status = model.CommandStatusDone
	_ = repo.Create(done)
	// 再建两条 pending（id 递增，取最早那条）
	first := mkPending("prod", "lobby-1")
	_ = repo.Create(first)
	second := mkPending("prod", "lobby-1")
	_ = repo.Create(second)
	// 他目标的 pending 不应串
	other := mkPending("prod", "lobby-2")
	_ = repo.Create(other)

	got, err := repo.FindOldestPending("prod", "lobby-1")
	if err != nil || got == nil {
		t.Fatalf("应取到最早 pending: %v / %v", got, err)
	}
	if got.ID != first.ID {
		t.Fatalf("应取最早一条 pending（id=%d），实际 id=%d", first.ID, got.ID)
	}

	none, err := repo.FindOldestPending("prod", "lobby-3")
	if err != nil || none != nil {
		t.Fatalf("无 pending 应返回 (nil,nil)，实际 %v / %v", none, err)
	}
}

// TestCommandUpdateStatusCAS 前态相符才迁移、幂等；前态不符 no-op。
func TestCommandUpdateStatusCAS(t *testing.T) {
	repo := NewAgentCommandRepository(newCommandTestDB(t))
	cmd := mkPending("prod", "lobby-1")
	_ = repo.Create(cmd)

	// pending → fetched（命中）
	ok, err := repo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, "")
	if err != nil || !ok {
		t.Fatalf("pending→fetched 应命中: %v / %v", ok, err)
	}
	// 再以 pending 为前态迁移（前态已变，应 no-op）
	ok, err = repo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusDone, "")
	if err != nil || ok {
		t.Fatalf("前态不符应 no-op（幂等防重）: %v / %v", ok, err)
	}
	// fetched → done 带结果摘要（命中）
	ok, err = repo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone, `{"files":3}`)
	if err != nil || !ok {
		t.Fatalf("fetched→done 应命中: %v / %v", ok, err)
	}
	got, _ := repo.FindByID(cmd.ID)
	if got.Status != model.CommandStatusDone || got.ResultDetail != `{"files":3}` {
		t.Fatalf("终态 / 结果摘要不一致: %+v", got)
	}
}

// TestCommandExpireStale 仅把早于阈值的 pending/fetched 标 expired，done 不动。
func TestCommandExpireStale(t *testing.T) {
	db := newCommandTestDB(t)
	repo := NewAgentCommandRepository(db)
	old := time.Now().Add(-2 * time.Hour)
	// 两条陈旧 pending/fetched + 一条陈旧 done（done 不应被标）
	stalePending := mkPending("prod", "a")
	_ = repo.Create(stalePending)
	staleFetched := mkPending("prod", "b")
	staleFetched.Status = model.CommandStatusFetched
	_ = repo.Create(staleFetched)
	staleDone := mkPending("prod", "c")
	staleDone.Status = model.CommandStatusDone
	_ = repo.Create(staleDone)
	// 把三条的 created_at 改早（绕过 gorm 自动时间）
	if err := db.Model(&model.AgentCommand{}).Where("1 = 1").Update("created_at", old).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	// 一条新鲜 pending（不应被标）
	fresh := mkPending("prod", "d")
	_ = repo.Create(fresh)

	n, err := repo.ExpireStale(time.Now().Add(-1 * time.Hour))
	if err != nil {
		t.Fatalf("ExpireStale 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应标 2 条陈旧 pending/fetched 为 expired，实际 %d", n)
	}
	gotDone, _ := repo.FindByID(staleDone.ID)
	if gotDone.Status != model.CommandStatusDone {
		t.Fatalf("陈旧 done 不应被标 expired，实际 %s", gotDone.Status)
	}
	gotFresh, _ := repo.FindByID(fresh.ID)
	if gotFresh.Status != model.CommandStatusPending {
		t.Fatalf("新鲜 pending 不应被标 expired，实际 %s", gotFresh.Status)
	}
}

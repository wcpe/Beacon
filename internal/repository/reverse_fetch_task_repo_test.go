package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
)

// newRFTaskRepoTestDB 打开内存 sqlite 并迁移 reverse_fetch_task，供仓库单测（不依赖 MySQL/DSN）。
func newRFTaskRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ReverseFetchTask{}); err != nil {
		t.Fatalf("迁移 reverse_fetch_task 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM reverse_fetch_task").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

func mkScanning(ns, serverID string) *model.ReverseFetchTask {
	return &model.ReverseFetchTask{
		NamespaceCode: ns, ServerID: serverID,
		Scope: model.ScopeGroup, GroupCode: "area1",
		Status: model.ReverseFetchTaskScanning, Operator: "admin",
	}
}

// TestRFTaskActiveUniqueConstraint 守护单实例互斥：同 (ns, serverId) 第二条活跃任务被 active 哨兵唯一键挡下。
func TestRFTaskActiveUniqueConstraint(t *testing.T) {
	db := newRFTaskRepoTestDB(t)
	repo := NewReverseFetchTaskRepository(db)

	first := mkScanning("prod", "s1")
	if err := repo.Create(first); err != nil {
		t.Fatalf("首条活跃任务应建成功: %v", err)
	}
	// 第二条活跃任务（同 ns+serverId，active_at 同为哨兵）→ 唯一键冲突
	err := repo.Create(mkScanning("prod", "s1"))
	if err == nil {
		t.Fatal("同实例第二条活跃任务应被唯一键挡下")
	}

	// 把第一条终结（active_at 置真实时间）后，可再建活跃任务（历史并存）
	if ok, e := repo.MarkTerminal(first.ID, model.ReverseFetchTaskScanning, model.ReverseFetchTaskDone, "", false, time.Now().UTC()); e != nil || !ok {
		t.Fatalf("终结首条应命中: ok=%v err=%v", ok, e)
	}
	if err := repo.Create(mkScanning("prod", "s1")); err != nil {
		t.Fatalf("终结后同实例应可再建活跃任务: %v", err)
	}
}

// TestRFTaskFindActiveByServer 只命中非终态任务；终态后不再命中。
func TestRFTaskFindActiveByServer(t *testing.T) {
	db := newRFTaskRepoTestDB(t)
	repo := NewReverseFetchTaskRepository(db)
	_ = repo.Create(mkScanning("prod", "s1"))

	active, err := repo.FindActiveByServer("prod", "s1")
	if err != nil || active == nil {
		t.Fatalf("应命中活跃任务: %v / %v", active, err)
	}
	if _, e := repo.MarkTerminal(active.ID, model.ReverseFetchTaskScanning, model.ReverseFetchTaskCancelled, "", true, time.Now().UTC()); e != nil {
		t.Fatalf("终结失败: %v", e)
	}
	again, err := repo.FindActiveByServer("prod", "s1")
	if err != nil || again != nil {
		t.Fatalf("终结后不应再命中活跃任务，实际 %v / %v", again, err)
	}
}

// TestRFTaskStateMachineCAS CAS 迁移：仅前态匹配才迁移；SaveManifest/SaveSelected 按预期前态。
func TestRFTaskStateMachineCAS(t *testing.T) {
	db := newRFTaskRepoTestDB(t)
	repo := NewReverseFetchTaskRepository(db)
	task := mkScanning("prod", "s1")
	_ = repo.Create(task)

	// SaveManifest 仅在 scanning 命中 → pending-review
	ok, err := repo.SaveManifest(task.ID, `{"files":[]}`, 3, 1, 0)
	if err != nil || !ok {
		t.Fatalf("SaveManifest 应命中: ok=%v err=%v", ok, err)
	}
	// 再次 SaveManifest（已非 scanning）→ 不命中
	if ok, _ := repo.SaveManifest(task.ID, `{}`, 0, 0, 0); ok {
		t.Fatal("非 scanning 态 SaveManifest 不应命中")
	}
	// SaveSelected 仅在 pending-review 命中 → fetching
	if ok, _ := repo.SaveSelected(task.ID, `["a.yml"]`, 1, 99); !ok {
		t.Fatal("SaveSelected 应命中")
	}
	got, _ := repo.GetByID(task.ID)
	if got.Status != model.ReverseFetchTaskFetching || got.SubmitCommandID != 99 || got.SelectedCount != 1 {
		t.Fatalf("提交后状态 / 计数错误，实际 %+v", got)
	}
	// UpdateStatus 错误前态不命中
	if ok, _ := repo.UpdateStatus(task.ID, model.ReverseFetchTaskScanning, model.ReverseFetchTaskIngesting); ok {
		t.Fatal("错误前态 UpdateStatus 不应命中")
	}
}

// TestRFTaskExpireStaleRepo 仅过期陈旧非终态任务、清空清单、置 active 时间。
func TestRFTaskExpireStaleRepo(t *testing.T) {
	db := newRFTaskRepoTestDB(t)
	repo := NewReverseFetchTaskRepository(db)
	task := mkScanning("prod", "s1")
	task.Manifest = `{"files":[{"path":"a"}]}`
	_ = repo.Create(task)
	if err := db.Model(&model.ReverseFetchTask{}).Where("id = ?", task.ID).
		Update("created_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := repo.ExpireStale(time.Now().Add(-1*time.Hour), time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
	got, _ := repo.GetByID(task.ID)
	if got.Status != model.ReverseFetchTaskExpired || got.Manifest != "" {
		t.Fatalf("过期后应 expired 且清单清空，实际 status=%s manifestLen=%d", got.Status, len(got.Manifest))
	}
}

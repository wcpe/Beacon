package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
)

func newRevOpTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:revop_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.ReversibleOperation{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func seedRevOp(t *testing.T, repo *ReversibleOperationRepository, forwardRef string) *model.ReversibleOperation {
	t.Helper()
	op := &model.ReversibleOperation{
		NamespaceCode: "prod", OpType: model.ReversibleOpPublish,
		Scope: model.ScopeGlobal, ScopeTarget: "", ForwardRef: forwardRef,
		Status: model.ReversibleStatusReversible, InversePayload: `{"itemId":1,"preVersion":1}`,
		Summary: "测试", Operator: "alice",
	}
	if err := repo.Create(op); err != nil {
		t.Fatalf("建账目失败: %v", err)
	}
	return op
}

// MarkReversed 是 CAS：仅 reversible 命中、翻转后清空快照；二次调用不命中（幂等闸）。
func TestRevOpRepo_MarkReversed_CAS(t *testing.T) {
	repo := NewReversibleOperationRepository(newRevOpTestDB(t))
	op := seedRevOp(t, repo, "ref-1")
	now := time.Now().UTC()

	ok, err := repo.MarkReversed(op.ID, "bob", now)
	if err != nil || !ok {
		t.Fatalf("首次 MarkReversed 应命中: ok=%v err=%v", ok, err)
	}
	got, _ := repo.FindByID(op.ID)
	if got.Status != model.ReversibleStatusReversed || got.ReversedBy != "bob" || got.InversePayload != "" {
		t.Fatalf("翻转后应 reversed + 回填 bob + 清空快照, got %+v", got)
	}
	// 二次 CAS 不命中（已非 reversible）
	ok2, _ := repo.MarkReversed(op.ID, "carol", now)
	if ok2 {
		t.Fatalf("二次 MarkReversed 不应命中（幂等闸）")
	}
}

// SupersedeActiveByForwardRef 只覆盖同 forward_ref 的旧 reversible，排除自身，不误伤其它对象。
func TestRevOpRepo_SupersedeByForwardRef(t *testing.T) {
	repo := NewReversibleOperationRepository(newRevOpTestDB(t))
	old := seedRevOp(t, repo, "ref-A")
	other := seedRevOp(t, repo, "ref-B") // 不同对象，不应被覆盖
	cur := seedRevOp(t, repo, "ref-A")   // 同对象的新账目（排除自身）

	n, err := repo.SupersedeActiveByForwardRef("prod", model.ReversibleOpPublish, "ref-A", cur.ID, time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("应只覆盖 1 条同对象旧账目: n=%d err=%v", n, err)
	}
	gotOld, _ := repo.FindByID(old.ID)
	if gotOld.Status != model.ReversibleStatusSuperseded || gotOld.InversePayload != "" {
		t.Fatalf("旧账目应 superseded + 清空快照, got %+v", gotOld)
	}
	gotOther, _ := repo.FindByID(other.ID)
	if gotOther.Status != model.ReversibleStatusReversible {
		t.Fatalf("不同对象账目不应被覆盖, got %s", gotOther.Status)
	}
	gotCur, _ := repo.FindByID(cur.ID)
	if gotCur.Status != model.ReversibleStatusReversible {
		t.Fatalf("当前账目（排除自身）不应被覆盖, got %s", gotCur.Status)
	}
}

// ExpireStale 只过期 before 之前的 reversible，清空快照；reversed / 新建的不动。
func TestRevOpRepo_ExpireStale(t *testing.T) {
	repo := NewReversibleOperationRepository(newRevOpTestDB(t))
	stale := seedRevOp(t, repo, "ref-1")
	fresh := seedRevOp(t, repo, "ref-2")

	// 把 stale 的 created_at 手工拨早到 2 小时前
	repo.db.Model(&model.ReversibleOperation{}).Where("id = ?", stale.ID).
		Update("created_at", time.Now().UTC().Add(-2*time.Hour))

	n, err := repo.ExpireStale(time.Now().UTC().Add(-time.Hour), time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("应只过期 1 条陈旧账目: n=%d err=%v", n, err)
	}
	gotStale, _ := repo.FindByID(stale.ID)
	if gotStale.Status != model.ReversibleStatusExpired || gotStale.InversePayload != "" {
		t.Fatalf("陈旧账目应 expired + 清空快照, got %+v", gotStale)
	}
	gotFresh, _ := repo.FindByID(fresh.ID)
	if gotFresh.Status != model.ReversibleStatusReversible {
		t.Fatalf("新建账目不应被过期, got %s", gotFresh.Status)
	}
}

// List 按过滤返回、最新在前。
func TestRevOpRepo_List(t *testing.T) {
	repo := NewReversibleOperationRepository(newRevOpTestDB(t))
	seedRevOp(t, repo, "ref-1")
	seedRevOp(t, repo, "ref-2")

	all, err := repo.List(ReversibleOperationFilter{Namespace: "prod"})
	if err != nil || len(all) != 2 {
		t.Fatalf("应列出 2 条: len=%d err=%v", len(all), err)
	}
	if all[0].ID < all[1].ID {
		t.Fatalf("应最新在前（id 倒序）, got %d,%d", all[0].ID, all[1].ID)
	}
	none, _ := repo.List(ReversibleOperationFilter{Namespace: "other"})
	if len(none) != 0 {
		t.Fatalf("不同环境应过滤为空, got %d", len(none))
	}
}

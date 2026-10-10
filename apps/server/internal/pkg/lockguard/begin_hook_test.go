package lockguard

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestBeginHookDetectsLockHeldTransaction 锁定「持锁开事务」被守卫捕获。
//
// 回归背景（code review P1）：守卫的六个探针挂在 gorm 的处理器上
// （create/query/update/delete/row/raw），，而 gorm **没有 begin 处理器**——
// `db.Transaction(...)` 直接调 `ConnPool.BeginTx`、不经任何回调。于是
// 「持锁 → 开事务 → 卡在取连接」这条路径探针看不到，而它恰是 P0 事故的原始形态
// （环的每一步都以开事务开始）。修复：store 在 `BeginTx` 取连接**之前**抛出
// 「事务即将开启」事件，lockguard 在该事件里做持锁判定。
//
// 判别力：去掉 store 的 begin 钩子（或 lockguard 的注册）即变红。
func TestBeginHookDetectsLockHeldTransaction(t *testing.T) {
	db := openGuardTestDB(t)

	var mu Mutex
	w, err := Attach(db, &mu)
	if err != nil {
		t.Fatalf("挂探针失败: %v", err)
	}
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	// —— 反：不持锁开事务 → 零违规（证明不误报）——
	if err := db.Transaction(func(*gorm.DB) error { return nil }); err != nil {
		t.Fatalf("开事务失败: %v", err)
	}
	if v := w.Violations(); v != 0 {
		t.Fatalf("不持锁开事务被误报为违规 %d 次", v)
	}

	// —— 正：持锁开事务 → 必报违规（证明有牙）——
	// 注意此处**不执行任何语句**，只开事务——正是探针覆盖不到的形态。
	mu.Lock()
	err = db.Transaction(func(*gorm.DB) error { return nil })
	mu.Unlock()
	if err != nil {
		t.Fatalf("持锁开事务失败: %v", err)
	}
	if v := w.Violations(); v == 0 {
		t.Fatal("持锁开事务未被捕获——begin 钩子未生效（P1 回归：gorm 无 begin 处理器）")
	}
	if site := w.Site(); site == "" {
		t.Error("违规未记录调用点，不便定位回归")
	} else {
		t.Logf("✓ 捕获到持锁开事务，调用点：%s", site)
	}
}

var _ = model.ChangeOrder{} // 保持与 openGuardTestDB 的 model 依赖一致

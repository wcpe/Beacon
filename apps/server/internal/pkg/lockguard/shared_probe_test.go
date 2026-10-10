package lockguard

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestMultipleAttachShareSameProbes 锁定「同一 *gorm.DB 上多次 Attach 都能观测到 DB 访问」。
//
// 回归背景：gorm 的 sortCallbacks 对**同名**回调只保留第一个（callbacks.go 的
// `getRIndex(sorted, c.name) == -1` 判据），第二位同名注册会被记 WARN 并静默吞掉。
// 而生产装配恰好在同一个 *gorm.DB 上挂两份守卫（交付编排器 @ main.go 与归档服务 @ main.go），
// 若各自 Register 则第二份永不执行、其计数恒为 0——「两份守卫各自覆盖两个服务」会失真。
//
// 本用例是该缺陷的判别器：修复前第二份 Watcher 的 Accesses 恒为 0（必红）。
func TestMultipleAttachShareSameProbes(t *testing.T) {
	db := openGuardTestDB(t)

	var mu1, mu2 Mutex
	w1, err := Attach(db, &mu1)
	if err != nil {
		t.Fatalf("第一次 Attach 失败: %v", err)
	}
	w2, err := Attach(db, &mu2)
	if err != nil {
		t.Fatalf("第二次 Attach 失败: %v", err)
	}

	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	// 一次不持锁的 DB 访问：两份 Watcher 都应观察到（探针被触发）。
	var n int64
	if err := db.Model(&model.ChangeOrder{}).Count(&n).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}

	if w1.Accesses() == 0 {
		t.Error("第一份 Watcher 未观察到 DB 访问")
	}
	if w2.Accesses() == 0 {
		t.Error("第二份 Watcher 未观察到 DB 访问——同名回调被 gorm 吞掉（见用例注释）")
	}

	// 反向：第二份 Watcher 必须仍按**自己**的 guards 判违规。
	// 只持有 mu2 时做 DB 访问 → w2 报违规、w1 不报（证明并入观测集不是把两份混为一谈）。
	mu2.Lock()
	if err := db.Model(&model.ChangeOrder{}).Count(&n).Error; err != nil {
		mu2.Unlock()
		t.Fatalf("持锁查询失败: %v", err)
	}
	mu2.Unlock()

	if w2.Violations() == 0 {
		t.Error("第二份 Watcher 未按自己的锁集合判违规——并入观测集后判定失配")
	}
	if w1.Violations() != 0 {
		t.Errorf("第一份 Watcher 误报了不属于它观测范围的违规 %d 次", w1.Violations())
	}
}

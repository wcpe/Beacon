package lockguard

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// —— 守卫的判别力与精确归属 ——
//
// 本包把「DB IO 一律在锁外」（.claude/rules/testing-and-quality.md §3）从人肉纪律升级为机器判据，
// 因此**它自身的正确性必须先被证明**：一个永远不报违规的探针，会让所有依赖它的「零违规」断言
// 变成假绿。故本组用例的第一条就是自校准（先证有牙、再判零违规）——这是 ca64cb3f 确立的纪律。

// openGuardTestDB 打开一个内存 sqlite 测试库（无需 AutoMigrate：探针在取连接前触发，
// 用的是与业务等价的语句形态）。
func openGuardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:lockguard_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	t.Cleanup(func() { store.Close(db) })
	if err := db.AutoMigrate(&model.ChangeOrder{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// TestGuardDetectsViolationAndStaysSilentWhenOutOfLock 是自校准用例，同时覆盖正反两面：
//
//	① 校准-反：不持锁做 DB 访问 → 零违规（探针不误报）；
//	② 校准-正：持锁做 DB 访问 → 必报违规（探针有牙）；
//	③ 观测开关关闭时不计数（生产默认关，不产生噪声与开销）。
//
// 没有 ②，「零违规」无法排除「探针根本没接线」；没有 ①，则可能是一个把一切都判违规的假阳性探针。
func TestGuardDetectsViolationAndStaysSilentWhenOutOfLock(t *testing.T) {
	db := openGuardTestDB(t)
	var mu Mutex
	w, err := Attach(db, &mu)
	if err != nil {
		t.Fatalf("挂探针失败: %v", err)
	}
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	// ① 校准-反：不持锁的 DB 访问必须零违规，且探针确实被触发（判据非空洞）。
	var n int64
	if err := db.Model(&model.ChangeOrder{}).Count(&n).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if w.Accesses() == 0 {
		t.Fatal("探针未接线：不持锁的查询没有触发任何处理器回调，后续「零违规」不具证明力")
	}
	if v := w.Violations(); v != 0 {
		t.Fatalf("检测器误报：不持锁的 DB 访问被记为违规 %d 次（%s）", v, w.Site())
	}

	// ② 校准-正：持锁期间发起 DB 访问必须被捕获。
	mu.Lock()
	if err := db.Model(&model.ChangeOrder{}).Count(&n).Error; err != nil {
		mu.Unlock()
		t.Fatalf("持锁查询失败: %v", err)
	}
	mu.Unlock()
	if w.Violations() == 0 {
		t.Fatal("检测器没有牙：持锁期间发起 DB 访问未被捕获，「零违规」结论不具证明力")
	}
	if site := w.Site(); !strings.Contains(site, "lockguard_test.go") {
		t.Fatalf("违规调用点应指向本测试的发起处，实际 %q", site)
	}

	// 锁已释放，违规计数不再增长（证明判据跟随持有状态而非粘滞）。
	before := w.Violations()
	_ = db.Model(&model.ChangeOrder{}).Count(&n).Error
	if w.Violations() != before {
		t.Fatalf("解锁后不应再有违规，实际从 %d 增至 %d", before, w.Violations())
	}

	// ③ 开关关闭：不计数、不判定（生产默认关，避免热路径开销与日志噪声）。
	SetEnabled(false)
	mu.Lock()
	_ = db.Model(&model.ChangeOrder{}).Count(&n).Error
	mu.Unlock()
	if w.Violations() != before {
		t.Fatalf("观测关闭时不应判定违规，实际 %d → %d", before, w.Violations())
	}
}

// TestGuardAttributesViolationToOwningGoroutineOnly 是本包的核心设计决定，也是与「裸 TryLock」探测法的分野。
//
// 场景：A goroutine 持锁做**非 DB** 工作（例如拼接命令、算哈希——完全合法的持锁行为），
// B goroutine 同时做 DB 访问。裸 TryLock 探测法会因「TryLock 失败」把 B 误报为违规；
// 本包按持有者 goroutine id 精确比对，应当零违规。
//
// 为什么这条必须测：本仓并发是常态（审批 worker / 推进器 / HTTP 请求并存），
// 一个会把合法并发判成违规的守卫，上线后只会被当作噪声关掉，防护随即失效。
func TestGuardAttributesViolationToOwningGoroutineOnly(t *testing.T) {
	db := openGuardTestDB(t)
	var mu Mutex
	w, err := Attach(db, &mu)
	if err != nil {
		t.Fatalf("挂探针失败: %v", err)
	}
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	// A：持锁做非 DB 工作，并在持锁期间把控制权交给 B。
	mu.Lock()
	holder := goid()
	if holder == 0 {
		mu.Unlock()
		t.Fatal("未能取到持锁 goroutine 的 id（探针归属判定不可信）")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// B：自身未持锁（锁在 A 手上），做 DB 访问必须**不被判违规**，且不被阻塞太久。
		var n int64
		if err := db.Model(&model.ChangeOrder{}).Count(&n).Error; err != nil {
			t.Errorf("B 查询失败: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		mu.Unlock()
		t.Fatal("B 的查询被阻塞：守卫不应改变任何锁/DB 语义（只观测、不介入）")
	}
	mu.Unlock()

	if v := w.Violations(); v != 0 {
		t.Fatalf("误报：锁由他人持有时，本 goroutine 的 DB 访问被记为违规 %d 次（%s）——"+
			"这正是裸 TryLock 探测法的缺陷，精确归属应把它排除", v, w.Site())
	}

	// 反证：A 自己持锁做 DB 访问时仍必须被抓到（排除「归属判定恒假」导致上面零违规的原因）。
	mu.Lock()
	var n int64
	_ = db.Model(&model.ChangeOrder{}).Count(&n).Error
	mu.Unlock()
	if w.Violations() == 0 {
		t.Fatal("持锁者本人的 DB 访问必须被判违规（否则归属判定恒假、上面的零违规无意义）")
	}
}

// TestGuardWrapsAllSixProcessors 验证六个 GORM 处理器都被覆盖：漏掉任何一个处理器，
// 那条路径上的持锁 DB 访问就会静默逃过检测（例如只挂 query 时，写操作的违规完全看不见）。
func TestGuardWrapsAllSixProcessors(t *testing.T) {
	db := openGuardTestDB(t)
	var mu Mutex
	w, err := Attach(db, &mu)
	if err != nil {
		t.Fatalf("挂探针失败: %v", err)
	}
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	// 每个代表一次不同处理器的 DB 访问；逐条在**持锁状态下**发起，要求每条都被捕获。
	steps := []struct {
		name string
		run  func() error
	}{
		{"Create", func() error {
			return db.Create(&model.ChangeOrder{Title: "探针", Status: model.ChangeOrderStatusDraft}).Error
		}},
		{"Query", func() error {
			var one model.ChangeOrder
			return db.First(&one).Error
		}},
		{"Update", func() error {
			return db.Model(&model.ChangeOrder{}).Where("1 = 1").Update("title", "改").Error
		}},
		{"Delete", func() error {
			return db.Where("title = ?", "改").Delete(&model.ChangeOrder{}).Error
		}},
		{"Raw", func() error {
			var n int64
			return db.Raw("SELECT count(*) FROM change_order").Scan(&n).Error
		}},
		{"Row", func() error {
			row := db.Model(&model.ChangeOrder{}).Limit(1).Row()
			if row == nil {
				return nil
			}
			var id uint
			// 上一步 Delete 后可能已无数据；「查不到」是合法结果，
			// 本步骤只关心探针是否在该处理器上触发（探针先于取连接与执行）。
			if err := row.Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return nil
		}},
	}
	for _, s := range steps {
		before := w.Violations()
		mu.Lock()
		err := s.run()
		mu.Unlock()
		if err != nil {
			t.Fatalf("%s 步骤执行失败: %v", s.name, err)
		}
		if w.Violations() == before {
			t.Fatalf("%s 处理器未被探针覆盖：持锁期间的该访问未被捕获", s.name)
		}
	}
}

// TestMutexSemanticsMatchSyncMutex 验证包装锁不改变互斥语义：
// 它替换的是生产代码里的 `sync.Mutex`，任何语义偏差（如允许重入、Unlock 不释放）
// 都会把「修死锁」变成「引入数据竞争」。
func TestMutexSemanticsMatchSyncMutex(t *testing.T) {
	var mu Mutex

	mu.Lock()
	if mu.TryLock() {
		t.Fatal("锁被持有时 TryLock 必须失败（不可重入）")
	}
	mu.Unlock()

	if !mu.TryLock() {
		t.Fatal("空闲锁的 TryLock 必须成功")
	}
	mu.Unlock()

	// 互斥性：N 个 goroutine 并发自增，若锁失效则结果小于 N。
	const goroutines, perG = 8, 200
	var counter int
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				mu.Lock()
				counter++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if counter != goroutines*perG {
		t.Fatalf("互斥失效：期望 %d，实际 %d", goroutines*perG, counter)
	}

	// 加解锁成对后，全局持有计数必须归零（否则守卫的快速路径会永远走慢路径，
	// 且「有人持锁」的判据会变成恒真——性能与正确性双失守）。
	if got := heldTotal.Load(); got != 0 {
		t.Fatalf("加解锁成对后持有计数应归零，实际 %d（登记泄漏）", got)
	}
}

// TestHeldTotalTracksConcurrentHolders 验证全局持有计数在并发下不漂移：
// 该计数是快速路径的判据，若漂移成负数或恒正，守卫要么漏报要么永久变慢。
func TestHeldTotalTracksConcurrentHolders(t *testing.T) {
	var mu Mutex
	const n = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	peak := make(chan int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mu.Lock()
			peak <- heldTotal.Load()
			time.Sleep(time.Millisecond) // 制造真实并发，确保计数被观察到
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	close(peak)

	// 互斥锁下任一时刻至多 1 个持有者：计数峰值不得大于 1。
	for v := range peak {
		if v > 1 {
			t.Fatalf("同一把锁的持有计数不应超过 1，实际观测到 %d", v)
		}
	}
	if got := heldTotal.Load(); got != 0 {
		t.Fatalf("并发加解锁后计数应归零，实际 %d", got)
	}
}

// TestCallSitePointsToBusinessCode 验证违规点定位：它要能指出业务代码的发起处，
// 而不是停在本包的探针闭包或 gorm 内部——否则排障时等于没有线索。
func TestCallSitePointsToBusinessCode(t *testing.T) {
	site := helperTriggeringDBCallSite()
	if !strings.Contains(site, "lockguard_test.go") {
		t.Fatalf("调用点应指向业务发起处，实际 %q", site)
	}
	if strings.Contains(site, "/lockguard/lockguard.go") {
		t.Fatalf("调用点不应停在探针自身，实际 %q", site)
	}
}

// helperTriggeringDBCallSite 让 callSite 有一个「测试文件里的业务帧」可指认。
func helperTriggeringDBCallSite() string {
	return callSite()
}

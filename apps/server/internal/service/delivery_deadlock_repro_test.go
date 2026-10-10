package service

// —— P0 死锁复现：单连接池下「批准执行」与「推进器 tick」互等（生产事件 2026-10-10 02:41:05）——
//
// 生产现场（v1.4.0，sqlite + `max-open-conns: 1`，当时 config.example.yml 的释放值）：
// 管理面走完「建单 → 提审 → 批准」后，审批行 75（operation_key=delivery.approve）停在 status=executing、
// lease_until 过期 20+ 分钟无人回收；所有需 DB 的端点永久挂起（healthz / metrics 不查库故仍正常）；
// 进程 6646 socket（485 CLOSE-WAIT）、37 线程全 futex_wait，CPU 与 WAL 零推进。
//
// 互等环（两个不同资源反序获取；行号为本仓当前版本）：
//
//	批准执行路径：持连接 → 等 mu
//	  approval_worker.go:151                w.svc.db.Transaction(...)          ← 取走连接池唯一连接，持有至提交
//	  approval_worker.go:157                  registry.ExecuteInTx(tx, ...)
//	  delivery_dangerous_approval.go:247      applyStartApprovedInTx(tx, ...)
//	  delivery_orchestrator_start.go:65         s.mu.Lock()                     ← 等 mu（此刻仍持有连接）
//
//	推进器 tick 路径：持 mu → 等连接
//	  delivery_orchestrator.go:189          s.mu.Lock()
//	  delivery_orchestrator.go:191            s.repo.ListActiveOrders(...)      ← 等连接（此刻仍持有 mu）
//
// `max-open-conns=1` 下没有第三条连接可打破该环，故双方永久互等。`busy_timeout(5000)`
// （store/db.go:331）是 **SQLite 文件锁**等待，对本环无效：等待发生在 database/sql 连接池排队
// （sql.DB.conn 等 connRequests channel，**不读 ctx 超时**），根本没进到 SQLite 层。
//
// 本文件两个用例都以**生产配置**（store.Open + MaxOpenConns=1，含生产 pragma）驱动**真实**审批 worker
// 与**真实**推进器：修复前必须失败（死锁检测），修复后转绿。

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// p0DeadlockWindow 是互等判定观察窗。生产现场该状态持续 20+ 分钟（lease 过期亦未被回收）；
// 测试取 5s —— 远大于任何正常 SQLite 查询的耗时，足以区分「永久互等」与「仅慢」。
const p0DeadlockWindow = 5 * time.Second

// p0RingProbeGrace 是「等待对方进入阻塞态」的宽限（起 goroutine 后等它真正卡住）。
const p0RingProbeGrace = 300 * time.Millisecond

// p0ReproEnv 是复现环境：生产同款单连接 sqlite + 真审批服务 + 真交付编排适配器。
type p0ReproEnv struct {
	db       *gorm.DB
	orders   *DeliveryOrderService
	orch     *DeliveryOrchestrator
	approval *ApprovalService
	nsID     uint
	clock    time.Time
}

// p0OpenSingleConnDB 打开 sqlite 且**刻意把池上限钉在 1**。
//
// 为什么不用 config.example.yml 的当前值（那是 4）：本用例复现的是**事故当时**的部署形态
// （v1.4.0 释放的样例是 `MaxOpenConns: 1`），环只在「池内无第三条连接」时闭合。
// 样例值提到 4 后（§2 的纵深防御），本例仍须按 1 跑——否则它会因为池够大而永远转绿，
// 失去「锁序一旦回归即变红」的作用；池上限是**复现前提**，不是被断言的对象。
// 测试仓里既有的 `MaxOpenConns: 2`（testsupport/db.go:59）与内存库装配都会掩盖该环。
// 走 store.Open 以带上生产 pragma（WAL + busy_timeout），用以证明 busy_timeout 无能为力。
func p0OpenSingleConnDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), name+".db"),
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("按生产配置打开 sqlite 失败: %v", err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent) // 判定靠超时与锁状态，不靠 SQL 日志
	t.Cleanup(func() { store.Close(db) })
	return db
}

// p0NewReproEnv 装配复现环境：真 Worker 执行链（注册交付审批适配器）+ 真推进器。
// settings 参数用于在「批准执行已持连接、尚未请求 mu」的精确时刻插入同步点。
func p0NewReproEnv(t *testing.T, settings deliverySettings) *p0ReproEnv {
	t.Helper()
	db := p0OpenSingleConnDB(t, "p0_deadlock")
	repo := repository.NewChangeOrderRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	health := healthview.NewStore()
	orders := NewDeliveryOrderService(db, repo, repository.NewConfigLayerVersionRepository(db),
		auditRepo, settings, health)
	blobSvc := NewDeliveryBlobService(db, repository.NewDeliveryBlobRepository(db), repo,
		repository.NewAgentCommandRepository(db), looseBlobSettings())
	blobSvc.SetRoot(t.TempDir())
	orch := NewDeliveryOrchestrator(db, repo, blobSvc, repository.NewAgentCommandRepository(db),
		auditRepo, health, metricwindow.New(0), nil)

	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, orders, orch)
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), auditRepo, registry)
	orders.SetApprovalService(approval)
	orch.SetApprovalService(approval)
	blobSvc.SetProgressWaker(orch)

	env := &p0ReproEnv{db: db, orders: orders, orch: orch, approval: approval,
		clock: p0FixtureClock()}
	orch.now = func() time.Time { return env.clock }
	env.seedCluster(t)
	return env
}

// p0FixtureClock 返回本组用例的固定时钟基准：**以真实当前时刻为起点、中途不再推进**。
//
// 为什么不再写死事故日期（2026-10-10 02:41:05）——那是一颗定时炸弹，已实测引爆：
// 审批 worker 用本时钟写租约（`claimNext` → `lease_until = clock + approvalWorkerLease(30s)`），
// 而许可签发判据在 `authz/authorization.go:527` 用的是**真实** `time.Now()`：
// `!time.Now().UTC().Before(req.LeaseUntil.UTC())` → `ErrForbidden`。
// 于是只要真实时间越过「写死时刻 + 30s」，批准执行就再也拿不到许可，
// 两条用例**必然**失败（表现为「单未进入 rolling」/「未在 5s 内到达闸门」）——
// 与锁序、连接池毫无关系。实测确认：仅把该写死值换成 `time.Now().UTC()`，两条用例立刻双双转绿。
//
// 为什么用「真实当前时刻」而不是修 authz 侧的时钟注入：本组用例要判的是**锁序**，
// 不关心"现在是几点"；改动生产代码（给 `executionAdapter` 加可注入时钟）只为让测试能写死日期，
// 是把测试的人为约束泄漏进生产接口。取当前时刻作基准既保留「时钟受控、测试期间恒定」的性质
// （`env.clock` 只读，无中途推进，断言仍确定），又天然避免与真实时钟比较时的过期问题。
//
// 注意：本函数每次调用取一次 now，故同一用例内恒定；跨用例各取各的，不存在共享状态。
func p0FixtureClock() time.Time { return time.Now().UTC().Truncate(time.Second) }

// seedCluster 铺集群事实：namespace + 一区两小区 + 模板源与两台目标（身份 active + 健康在线）。
func (e *p0ReproEnv) seedCluster(t *testing.T) {
	t.Helper()
	ns := model.Namespace{Code: "prod", Name: "prod"}
	mustCreate(t, e.db, &ns)
	e.nsID = ns.ID
	cluster := model.BCCluster{NamespaceID: ns.ID, Name: "bc-1"}
	mustCreate(t, e.db, &cluster)
	region := model.Region{BCClusterID: cluster.ID, Name: "region-1"}
	mustCreate(t, e.db, &region)
	zone1 := model.Zone{RegionID: region.ID, Name: "zone-1"}
	zone2 := model.Zone{RegionID: region.ID, Name: "zone-2"}
	mustCreate(t, e.db, &zone1)
	mustCreate(t, e.db, &zone2)
	seedDeliveryServer(t, e.db, ns.ID, "src-1", model.ServerKindBackend, &zone1.ID, model.AgentIdentityStatusActive)
	seedDeliveryServer(t, e.db, ns.ID, "t-1", model.ServerKindBackend, &zone1.ID, model.AgentIdentityStatusActive)
	seedDeliveryServer(t, e.db, ns.ID, "t-2", model.ServerKindBackend, &zone2.ID, model.AgentIdentityStatusActive)
	markDeliveryOnline(e.orch.health, ns.ID, "src-1", "t-1", "t-2")
}

// draftOrder 建一张满足全部提审前置的 draft 单（文件项 + blob 就绪 + 点名两台目标 + 模板源在线）。
func (e *p0ReproEnv) draftOrder(t *testing.T, shaSeed string) *model.ChangeOrder {
	t.Helper()
	detail, err := e.orders.Create(e.nsID, ChangeOrderInput{
		Title:          strPtr("发布大厅插件"),
		SourceServerID: strPtr("src-1"),
		ScanDir:        strPtr("plugins/"),
		Selector:       &ChangeSelector{Servers: []string{"t-1", "t-2"}},
	}, "ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("建 draft 单失败: %v", err)
	}
	seedFileItemWithBlob(t, e.db, detail.ID, "plugins/demo.jar", repeatHex(shaSeed, 64), 64)
	order, err := repository.NewChangeOrderRepository(e.db).FindByID(detail.ID)
	if err != nil || order == nil {
		t.Fatalf("回读 draft 单失败: %v", err)
	}
	return order
}

// worker 构造指向同一审批服务的 worker（生产同款构造，仅换成可控时钟）。
func (e *p0ReproEnv) worker() *ApprovalWorker {
	w := NewApprovalWorker(e.approval)
	w.now = func() time.Time { return e.clock }
	return w
}

// p0OccupiedConnection 借出连接池中唯一的连接，占住它直到 release（模拟「另一条路径正在用连接」）。
type p0OccupiedConnection struct{ release func() }

// p0HoldSoleConnection 用 sql.Conn 独占连接池仅有的 1 条连接（pool 上限为 1 时其他查询必然排队）。
func p0HoldSoleConnection(t *testing.T, db *gorm.DB) *p0OccupiedConnection {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("独占唯一连接失败: %v", err)
	}
	var once sync.Once
	return &p0OccupiedConnection{release: func() { once.Do(func() { _ = conn.Close() }) }}
}

// waitMutexHeld 在宽限内观测编排器 mu 是否被**他人**持有（TryLock 失败即被持有）。
//
// 仅用于把「对方已按预期进入持锁状态」从调度竞态变成确定事实，**不是**判定前提：
// 修复落地后该路径本就不该再持 mu，观测不到属预期结果，故此处返回布尔而非直接失败。
func waitMutexHeld(orch *DeliveryOrchestrator, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !orch.mu.TryLock() {
			return true
		}
		orch.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// waitUntilStuckOrDone 观测某 goroutine 是否在宽限内返回；返回 true 表示已返回（未卡住）。
func waitUntilStuckOrDone(done <-chan struct{}, grace time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

// p0PoolInUse 读连接池在用连接数（非阻塞；用于证明「唯一连接正被持有」）。
func p0PoolInUse(t *testing.T, db *gorm.DB) int {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	return sqlDB.Stats().InUse
}

// TestAdvanceTickMustNotHoldMutexWhileWaitingForConnection 锁定 方案 A 的不变量（也正是
// .claude/rules/testing-and-quality.md §3「DB IO 一律在锁外」所要求的口径）：
// **推进器 tick 在等待 DB 连接期间不得持有 s.mu**。
//
// 复现配方（完全确定，无调度竞态）：独占连接池唯一连接 → 起真 advanceActiveOrders →
// 它必然卡在 mu 之后的第一个查询（delivery_orchestrator.go:191 ListActiveOrders）。
// 此时若 mu 仍被它持有（TryLock 失败），说明它「持 mu 等连接」——即互等环的另一半，
// 生产中与「批准执行持连接等 mu」（delivery_orchestrator_start.go:65）合环。
//
// 修复前：mu 在整个等待期间被持有 → 本用例失败；修复后：等连接期间 mu 空闲 → 转绿。
func TestAdvanceTickMustNotHoldMutexWhileWaitingForConnection(t *testing.T) {
	env := p0NewReproEnv(t, p0PlainSettings{})

	// 独占唯一连接：任何后续 DB 查询必然排队等连接（等价于生产里「批准执行事务正持有连接」）。
	held := p0HoldSoleConnection(t, env.db)
	t.Cleanup(held.release)

	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		env.orch.advanceActiveOrders(context.Background())
	}()

	// 确证 tick 已卡在等连接（宽限内不返回）。
	if waitUntilStuckOrDone(tickDone, p0RingProbeGrace) {
		t.Fatal("推进 tick 不应在唯一连接被占用时返回（复现前提失效）")
	}
	// 关键断言：等待连接期间 mu 必须是空闲的。
	if !env.orch.mu.TryLock() {
		t.Fatalf("推进 tick 在等 DB 连接期间仍持有 s.mu（互等环的一半）："+
			"mu 由 tick 持有 → 任何持连接后请求 mu 的路径（批准执行 delivery_orchestrator_start.go:65）与之永久互等；"+
			"详见本文件头部的环图（db_in_use=%d）", p0PoolInUse(t, env.db))
	}
	env.orch.mu.Unlock()

	// 释放连接后 tick 必须能继续跑完（证明前面卡住的只是「等连接」，不是永久损坏）。
	held.release()
	if waitUntilStuckOrDone(tickDone, p0DeadlockWindow) {
		return
	}
	t.Fatalf("释放连接后推进 tick 仍未返回：tick 自身也卡死（db_in_use=%d）", p0PoolInUse(t, env.db))
}

// TestApprovalExecutionDeadlocksWithAdvanceTickOnSingleConnection 复现并锁定生产事故本体：
// 单连接池下「批准执行」（持连接 → 等 mu）与「推进器 tick」（持 mu → 等连接）永久互等。
//
// 走**真实**路径：申请提交 → 批准（CAS executing + 唤醒 worker）→ 真 ApprovalWorker.RunOnce
// （claimNext → db.Transaction → registry.ExecuteInTx → executeDeliveryApprovalInTx →
// applyStartApprovedInTx），不是测试快捷入口。
//
// 复现的确定性来自 p0GatedSettings：批准执行在**外层事务内**读
// delivery.approver-separation-enabled（delivery_order_service.go:652）。测试据此把 worker 精确停在
// 「已取走唯一连接、尚未继续」的位置，再从该点起真 tick 并放行 worker，让两条路径在唯一连接上真实交叠。
//
// 本用例判的是**环**（两半同时反序持锁），不是单侧：任一半修好，环都无法闭合——因为
//   - 只修 A（tick 不持 mu 等连接）：worker 请求 mu 时 mu 空闲，直接通过；
//   - 只修 C（批准执行不请求 mu）：tick 持 mu 等连接，worker 不等 mu，提交后连接释放，tick 继续；
//
// 故本用例在 A、C 任一落地后即转绿，而**各半的不变量由各自的专项用例锁定**：
// A 侧见 TestAdvanceTickMustNotHoldMutexWhileWaitingForConnection，
// C 侧见 TestApprovalExecutionMustNotWaitForMutexWhileHoldingConnection。
// 两侧同时回归才会重新合环——那正是本用例存在的意义（生产事故就是两半同时成立）。
//
// 修复前：两条路径在观察窗内零推进，且第三个 DB 调用同样挂起（生产「所有需 DB 端点挂起」同源）；
// 修复后：双方各自推进完成，本用例转绿。
func TestApprovalExecutionDeadlocksWithAdvanceTickOnSingleConnection(t *testing.T) {
	settings := (&p0GatedSettings{}).enable()
	env := p0NewReproEnv(t, settings)

	// 建单 → 提审 → 批准（生产现场的三步管理面动作）。
	order := env.draftOrder(t, "ab")
	ticket, err := env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "p0-submit", "ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := env.approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), "10.0.0.1"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}

	// 起真 worker：它会 claimNext（v3）→ 进入 execute 外层事务 → 在闸门处停住。
	settings.arm()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, _ = env.worker().RunOnce(context.Background())
	}()
	select {
	case <-settings.arrived:
	case <-time.After(p0DeadlockWindow):
		t.Fatalf("批准执行未在 %s 内到达闸门（复现前提失效）", p0DeadlockWindow)
	}
	// 闸门此刻：唯一连接已被批准执行事务占用，且它还没继续往下走。
	if inUse := p0PoolInUse(t, env.db); inUse < 1 {
		t.Fatalf("批准执行未持有连接（db_in_use=%d），复现前提失效", inUse)
	}

	// 起真 tick：与批准执行在唯一连接上真实交叠（连接已占满，tick 必然排到连接池上）。
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		env.orch.advanceActiveOrders(context.Background())
	}()
	// 记录等待期 mu 是否被他人持有——仅作诊断：任一半修复后，此处都不该出现「持 mu 等连接」。
	muHeldWhileWaiting := waitMutexHeld(env.orch, p0RingProbeGrace)

	// 放行批准执行：它继续走 applyStartApprovedInTx。
	settings.open()

	// 第三个 DB 调用：镜像生产「所有需 DB 的 HTTP 端点永久挂起」。
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		var n int64
		_ = env.db.Model(&model.ApprovalRequest{}).Count(&n).Error
	}()

	// 观察窗内任一方推进即视为未合环；双方零推进即复现成功。
	tickStuck := !waitUntilStuckOrDone(tickDone, p0DeadlockWindow)
	workerStuck := !waitUntilStuckOrDone(workerDone, p0DeadlockWindow)
	if !tickStuck && !workerStuck {
		return // 修复后：两条路径都正常收口
	}
	probeStuck := !waitUntilStuckOrDone(probeDone, p0RingProbeGrace)
	muHeldByOther := !env.orch.mu.TryLock()
	if !muHeldByOther {
		env.orch.mu.Unlock()
	}
	t.Fatalf("复现生产死锁环（2026-10-10 02:41:05）：\n"+
		"  tick 长停=%v（持 mu 等连接：delivery_orchestrator.go 的 advanceActiveOrders）\n"+
		"  worker 长停=%v（持连接等 mu：approval_worker.go 的外层事务 → applyStartApprovedInTx）\n"+
		"  第三个 DB 调用长停=%v（生产「所有需 DB 端点挂起」同源）\n"+
		"  mu 被他人持有=%v  等待期曾观测到 mu 被持有=%v  连接池在用=%d（应为 1，上限 1）\n"+
		"  结论：max-open-conns=1 下无第三条连接可破环；busy_timeout(5000) 对连接池排队无效",
		tickStuck, workerStuck, probeStuck, muHeldByOther, muHeldWhileWaiting, p0PoolInUse(t, env.db))
}

// p0PlainSettings 是复现所需的最小设置源（审批人分离关闭、能力下限为空 = 不校验）。
type p0PlainSettings struct{}

func (p0PlainSettings) GetBool(string) bool     { return false }
func (p0PlainSettings) GetString(string) string { return "" }

// p0GatedSettings 是可在精确时刻闸住批准执行的设置源。
//
// 依据：批准执行在**外层事务内**读 delivery.approver-separation-enabled
// （delivery_order_service.go:652，位于 executeDeliveryApprovalInTx → applyApprove），
// 该点已持有唯一连接，且早于 applyStartApprovedInTx 的 mu.Lock。测试在此插入同步点，
// 把「tick 先取 mu」从调度竞态变成确定事实，从而稳定复现而不偶发。
type p0GatedSettings struct {
	armed   atomic.Bool
	arrived chan struct{}
	release chan struct{}
	hit     sync.Once
	opened  sync.Once
}

func (g *p0GatedSettings) GetBool(key string) bool {
	if g.armed.Load() && key == SettingDeliveryApproverSeparationEnabled {
		g.hit.Do(func() {
			close(g.arrived)
			<-g.release
		})
	}
	return false
}

func (g *p0GatedSettings) GetString(string) string { return "" }

// enable 初始化闸门通道（必须在使用前调用一次）。
func (g *p0GatedSettings) enable() *p0GatedSettings {
	g.arrived = make(chan struct{})
	g.release = make(chan struct{})
	return g
}

// arm 开启闸门（此后的首次读取将阻塞，直到 open）。
func (g *p0GatedSettings) arm() { g.armed.Store(true) }

// open 放行闸门（幂等）。
func (g *p0GatedSettings) open() { g.opened.Do(func() { close(g.release) }) }

// Package lockguard 提供「持锁期间做 DB 访问」的运行时检测（防 P0 死锁回归）。
//
// 背景（2026-10-10 生产事故）：控制面在单连接池（`max-open-conns: 1`）下出现过
// 「持锁等连接 × 持连接等锁」的两资源反序获取，两条路径永久互等、所有需 DB 的端点挂起 25 分钟。
// 规范侧早有明文（`.claude/rules/testing-and-quality.md` §3「DB IO 一律在锁外」），
// 但此前只靠人肉遵守——本包把该规则从「人肉纪律」升级为「机器可强制的判据」。
//
// 机制：在 GORM 六个处理器各挂一个 `Before("*")` 锚点。该锚点**先于取连接触发**（已实测），
// 因此它捕获的是「DB 访问意图」——既覆盖拿到连接后的 SQL，也覆盖事故里最危险的那一态
// **「已持锁、正排在连接池队列上」**（此时还没拿到连接、SQL 还没执行）。
// 这是纯 SQL 层钩子做不到的。
//
// 与「裸 TryLock」探测法的关键差别（本包的核心设计决定）：
// 常见做法是对目标锁 `TryLock()`，失败即认定「当前 goroutine 持锁」。但 `sync.Mutex` 不可重入，
// **TryLock 失败只能说明锁被「某人」持有，不能说明被「自己」持有**——于是
// 「A 持锁做非 DB 工作、B（另一个 goroutine）做 DB 访问」会被误报为违规。而并发正是本仓常态。
// 本包改用**持有者 goroutine id 精确比对**：只有「当前 goroutine == 该锁的持有者」才算违规，
// 从根上消除这类误报，也不再需要在观测时短暂改动锁状态（TryLock 成功还需立刻 Unlock）。
//
// 成本：为不拖累热路径，判定入口先读一个全局计数（1 次原子读，实测约 6ns）——
// 无人持有任何受观测锁时直接返回，**根本不取 goroutine id**（取 id 实测约 5.8µs，不可放进热路径）。
// 只有当确实有锁被持有时才进慢路径做精确比对，而那已是需要被审视的场景。
package lockguard

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

// goid 返回当前 goroutine 的 id。
//
// 为什么用 `runtime.Stack` 解析而不是 `//go:linkname runtime.getg`：
// linkname 依赖运行时的内部符号与结构体布局，跨 Go 版本可能静默失效（甚至读到错值），
// 而本包是**安全防护件**，其自身正确性的优先级高于几个微秒。解析法只用公开 API。
// 代价（实测约 5.8µs）由上层快速路径挡住：无人持锁时根本不会调到本函数。
func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// 形如 "goroutine 123 [running]:\n..."
	s := buf[len("goroutine "):n]
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	var id int64
	for _, c := range s[:i] {
		id = id*10 + int64(c-'0')
	}
	return id
}

// heldTotal 是「当前被持有的受观测锁总数」。判定的快速路径依赖它：
// 为 0 时不可能存在「持锁做 DB」，无需取 goroutine id。
var heldTotal atomic.Int64

// Mutex 是可观测互斥锁：语义与 `sync.Mutex` 一致，额外记录当前持有者的 goroutine id。
//
// 为什么不直接用 `sync.Mutex` + 外部记录：持有者信息必须与加解锁**在同一处**更新，
// 否则一旦某条路径漏记，守卫会静默失效（且失效方向是「漏报」，最难发现）。
// 包成类型后，编译器保证「凡是加锁都经过这里」。
//
// 注意零值可用；不可拷贝（含 atomic 字段）。
type Mutex struct {
	mu    sync.Mutex
	owner atomic.Int64
}

// Lock 加锁并登记持有者。
func (m *Mutex) Lock() {
	m.mu.Lock()
	m.owner.Store(goid())
	heldTotal.Add(1)
}

// Unlock 释放并清空持有者登记。
//
// 语句顺序两组约束，两组都不能改：
//   - `owner.Store(0)` **必须在真正释放之前**：释放后再清零会覆盖「刚抢到锁的新持有者」写入的 id
//     （Unlock → 新持有者 Lock 是紧邻的两步，中间无同步点），那会让守卫**失去对新持有者的追踪**——
//     即漏报，而这正是本守卫最危险的失效方向。
//   - `heldTotal.Add(-1)` **必须在真正释放之后**：先递减会留下「锁仍被持有、全局计数已归零」的窗口，
//     而全局计数是判定入口的快速路径闸门（为 0 直接返回、连持有者都不比对）——窗口期内任何一次
//     DB 访问都会被静默放过。移到释放之后，计数只可能偏大（保守），不会偏小。
func (m *Mutex) Unlock() {
	m.owner.Store(0)
	m.mu.Unlock()
	heldTotal.Add(-1)
}

// TryLock 尝试加锁；成功时同样登记持有者（与 Lock 保持同一套不变量）。
func (m *Mutex) TryLock() bool {
	if !m.mu.TryLock() {
		return false
	}
	m.owner.Store(goid())
	heldTotal.Add(1)
	return true
}

// heldByCurrent 报告当前 goroutine 是否正是本锁的持有者。
// 这是违规判据的唯一来源：只有「自己持锁」才算违规，别人持锁不算（见包注释的误报说明）。
func (m *Mutex) heldByCurrent() bool {
	owner := m.owner.Load()
	return owner != 0 && owner == goid()
}

// enabled 是观测开关（进程级）：关闭时探针立即返回，不计数也不判定。
//
// 为什么做成进程级而不是逐 Watcher：这是**诊断开关**，语义是「现在开始盯住锁内 DB 访问」，
// 与「哪一把锁」无关；做成进程级才能被运维设置项直接热切（照 `log.level` 的做法）。
// 默认关——生产默认不开（见 LogViolation 的说明：生产落点是可观测性，不是 fail-fast）。
var enabled atomic.Bool

// SetEnabled 打开 / 关闭观测（运维设置项热改时调用，即时生效）。
func SetEnabled(on bool) { enabled.Store(on) }

// Enabled 报告当前是否在观测。
func Enabled() bool { return enabled.Load() }

// Watcher 把一组受观测锁挂到 GORM 上，捕获「持锁发起 DB 访问」。
type Watcher struct {
	// guards 是受观测的锁集合（注册后不再变更，故无需锁保护）。
	guards []*Mutex
	// accesses / violations 是观测计数：violations 用于断言，accesses 用于证明判据非空洞。
	accesses, violations atomic.Int64
	// onViolation 是违规回调；nil 时仅计数（生产模式的默认行为）。
	onViolation atomic.Pointer[func(Violation)]
	// site 记录首个违规调用点，便于指认回归位置。
	site atomic.Value
}

// Violation 描述一次「持锁做 DB 访问」。
type Violation struct {
	// Site 是发起该 DB 访问的调用点（首个属于被测代码的栈帧）。
	Site string
	// Processor 是触发它的 GORM 处理器名（query / create / update / delete / row / raw）。
	Processor string
}

// attached 记录每个 *gorm.DB 上已挂的 Watcher，使多次 Attach 复用同一组探针。
//
// 为什么必须复用而非各挂一套：gorm 的 sortCallbacks 对**同名**回调只保留第一个
// （callbacks.go 的 `getRIndex(sorted, c.name) == -1` 判据），第二位同名注册既会被
// 记 WARN 又被静默吞掉。而本包的回调名是固定的六个（lockguard:query 等），
// 生产装配恰好在同一个 *gorm.DB 上挂两份（交付编排器与归档服务，见 main.go），
// 若各自 Register 则**第二份永不执行**，其 Watcher 的计数恒为 0——
// 「两份守卫各自覆盖两个服务」这一说法会失真。
//
// 复用后语义更准确：探针是「观察这张连接上的任意 DB 访问是否发生在持锁期间」，
// 与具体是谁挂的无关；多个 Watcher 共享探针、各自持有自己的 guards 与计数。
// key 用 db 指针：同一实例共享、不同实例（夹具各建各的库）互不干扰。
var attached sync.Map // map[*gorm.DB]*sharedProbe

// sharedProbe 是一张 *gorm.DB 上共享的一组探针，持有全部待观测的 Watcher。
type sharedProbe struct {
	mu       sync.Mutex
	watchers []*Watcher
}

// Attach 在 db 的六个处理器上挂探针，观测给定的锁，返回 Watcher。
//
// 同一 *gorm.DB 上多次调用会**复用同一组探针**并把新锁并入观测集（见 attached 的说明）；
// 不同 *gorm.DB 各自独立。
//
// 返回值同时用作句柄：测试结束可不必显式关闭（进程内回调注册不影响后续用例的正确性，
// 因为 enabled 默认关闭、且判定是按「是否真持锁」而非时序）。需要严格隔离时用 Close。
func Attach(db *gorm.DB, guards ...*Mutex) (*Watcher, error) {
	w := &Watcher{guards: guards}

	// 已有探针：并入观测集，不再注册（否则被 gorm 吞掉）。
	if existing, ok := attached.Load(db); ok {
		sp := existing.(*sharedProbe)
		sp.mu.Lock()
		sp.watchers = append(sp.watchers, w)
		sp.mu.Unlock()
		return w, nil
	}

	sp := &sharedProbe{watchers: []*Watcher{w}}
	actual, loaded := attached.LoadOrStore(db, sp)
	if loaded {
		// 并发下另一个调用先登记成功：并入它，不重复注册。
		shared := actual.(*sharedProbe)
		shared.mu.Lock()
		shared.watchers = append(shared.watchers, w)
		shared.mu.Unlock()
		return w, nil
	}
	// 探针遍历共享集内的全部 Watcher：每个 Watcher 各自判自己的 guards 与计数，
	// 故「一张连接上的 DB 访问被哪个 Watcher 记下」由各 Watcher 的锁集合决定。
	probe := func(processor string) func(*gorm.DB) {
		return func(*gorm.DB) {
			sp.mu.Lock()
			watchers := sp.watchers
			sp.mu.Unlock()
			for _, watcher := range watchers {
				watcher.check(processor)
			}
		}
	}
	// 六个处理器各挂 Before("*")：排在该处理器回调序列最前，先于取连接触发（见包注释）。
	registrations := []struct {
		name    string
		reg     func(name string, fn func(*gorm.DB)) error
		display string
	}{
		{"lockguard:query", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Query().Before("*").Register(n, fn)
		}, "query"},
		{"lockguard:create", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Create().Before("*").Register(n, fn)
		}, "create"},
		{"lockguard:update", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Update().Before("*").Register(n, fn)
		}, "update"},
		{"lockguard:delete", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Delete().Before("*").Register(n, fn)
		}, "delete"},
		{"lockguard:row", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Row().Before("*").Register(n, fn)
		}, "row"},
		{"lockguard:raw", func(n string, fn func(*gorm.DB)) error {
			return db.Callback().Raw().Before("*").Register(n, fn)
		}, "raw"},
	}
	for _, r := range registrations {
		if err := r.reg(r.name, probe(r.display)); err != nil {
			return nil, fmt.Errorf("注册 %s 探针失败: %w", r.name, err)
		}
	}
	return w, nil
}

// OnViolation 设置违规回调（生产模式用于落日志 / 计数上报；测试模式用于 t.Fatalf）。
func (w *Watcher) OnViolation(fn func(Violation)) *Watcher {
	w.onViolation.Store(&fn)
	return w
}

// Accesses 返回观测到的 DB 访问次数（用于证明判据非空洞）。
func (w *Watcher) Accesses() int64 { return w.accesses.Load() }

// Violations 返回违规次数。
func (w *Watcher) Violations() int64 { return w.violations.Load() }

// Site 返回首个违规调用点（无违规时为空串）。
func (w *Watcher) Site() string {
	if v, ok := w.site.Load().(string); ok {
		return v
	}
	return ""
}

// Reset 清零计数与首个违规点（供分阶段断言：每段各自校验，避免上一段的计数污染下一段）。
func (w *Watcher) Reset() {
	w.accesses.Store(0)
	w.violations.Store(0)
	w.site.Store("")
}

// check 是每次 DB 访问的判定入口。
func (w *Watcher) check(processor string) {
	if !enabled.Load() {
		return
	}
	w.accesses.Add(1)
	// 快速路径：没有任何受观测锁被持有 ⇒ 不可能违规，连 goroutine id 都不必取。
	if heldTotal.Load() == 0 {
		return
	}
	if !w.holdingAny() {
		return
	}
	if w.violations.Add(1) == 1 {
		w.site.Store(callSite())
	}
	if fn := w.onViolation.Load(); fn != nil {
		(*fn)(Violation{Site: w.Site(), Processor: processor})
	}
}

// holdingAny 报告当前 goroutine 是否持有集合里的任一把锁。
func (w *Watcher) holdingAny() bool {
	for _, g := range w.guards {
		if g.heldByCurrent() {
			return true
		}
	}
	return false
}

// callSite 从调用栈里取首个「不属于本包、也不属于 gorm 内部」的帧，用于指认违规发生处。
//
// 判据排除三类：本包的探针闭包、gorm 的回调分发（它必然是中间帧，没有定位价值）、运行时内部。
// **不排除测试文件**：本包同时服务于生产诊断与测试断言，两者都可能是违规发起处；
// 若把测试帧也滤掉，测试里只能拿到无信息的 gorm 内部帧（无法指认到具体用例行号）。
func callSite() string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs) // 跳过 Callers 与探针闭包自身
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.File != "" && !isInternalFrame(f) {
			return fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Function)
		}
		if !more {
			return "未识别（栈内无可指认帧）"
		}
	}
}

// isInternalFrame 判该帧是否属于「无定位价值」的内部实现（本包探针 / gorm / 运行时）。
func isInternalFrame(f runtime.Frame) bool {
	if strings.Contains(f.File, "/lockguard/lockguard.go") {
		return true
	}
	if strings.Contains(f.File, "/gorm.io/gorm") {
		return true
	}
	return strings.HasPrefix(f.Function, "runtime.")
}

// LogViolation 是生产模式的默认处置：记 ERROR 日志、不 panic。
//
// 为什么生产不 panic：事故教训是「healthz / metrics 不查库 → 掩盖了故障范围」；
// 若守卫 panic，会把「部分端点慢」升级为「进程崩溃」，对控制面可用性是净损失
// （违反 `.claude/rules/architecture-invariants.md` §3「两者不得互为权威或互相阻塞」）。
// fail-fast 的正确落点是**测试与 CI**，生产落点是**可观测性**。
func LogViolation(v Violation) {
	slog.Error("检测到持锁期间发起 DB 访问（违反「DB IO 一律在锁外」）",
		"处理器", v.Processor, "调用点", v.Site)
}

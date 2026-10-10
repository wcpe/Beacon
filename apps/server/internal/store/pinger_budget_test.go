package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBoundedPingerFailsOnTimeWhenPoolExhausted 是 P1-1 的核心断言：**池被占满时，健康探测必须在
// 预算内准时失败，而不是无界挂起**。
//
// 为什么这条必须单独存在（评审 P1-1）：`(*sql.DB).Ping()` 内部走 `context.Background()`，
// 池耗尽时**永远不返回**。而包装层此前只有 `Ping()` 直通（无预算），运行期调用方拿到的又恰是
// 原生池（GetDBConn 的逃生口）——于是事故里「所有需 DB 的端点挂起」的症状会在
// `/api/system/status` 与归档可达性探测上原样复现，而这两条恰恰是排障时最需要的。
//
// 判别力（修复前必红）：把包装层的 PingContext 去掉、或让调用方退回 Ping()，本用例会在
// **耗时**上暴露——探测永不返回，只能靠 testing 超时收场（与 connpool_timeout_test.go 里
// 裸查询那条同款判据）。
func TestBoundedPingerFailsOnTimeWhenPoolExhausted(t *testing.T) {
	// 池上限 1；call 预算 300ms（足够长到不误判，又足够短到用例很快）。
	f := openConnPoolFixture(t, "bounded_ping", 1, 300, 30000)

	pinger := NewBoundedPinger(f.db)
	if pinger == nil {
		t.Fatal("NewBoundedPinger 返回 nil：包装层未启用或装配点失效，运行期探测将没有预算")
	}

	release := f.occupySoleConnection(t)
	defer release()

	start := time.Now()
	err := pinger.PingContext(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("池被占满时探测应失败（超时），实际成功——判据不成立")
	}
	// 上界给足余量（预算 300ms，宽限到 3s）：判的是「有界」，不是「精确准时」——
	// 挂起形态是**永不返回**，与 300ms 还是 800ms 无关。宽限避免调度抖动导致的假红。
	if elapsed > 3*time.Second {
		t.Fatalf("探测耗时 %v，远超预算 300ms——预算未生效（疑似退回了无界 Ping）", elapsed)
	}
	t.Logf("池耗尽时探测在 %v 内准时失败（预算 300ms）: %v", elapsed, err)
}

// TestBoundedPingerSucceedsWhenPoolFree 是上一条的反向对照：池空闲时探测必须成功。
//
// 为什么需要它：只有「池满必失败」一条时，一个「永远返回错误」的假实现也能通过；
// 加上本条才能把判据钉在「预算」而不是「一律失败」上。
func TestBoundedPingerSucceedsWhenPoolFree(t *testing.T) {
	f := openConnPoolFixture(t, "bounded_ping_ok", 2, 300, 30000)
	pinger := NewBoundedPinger(f.db)
	if pinger == nil {
		t.Fatal("NewBoundedPinger 返回 nil")
	}
	if err := pinger.PingContext(context.Background()); err != nil {
		t.Fatalf("池空闲时探测应成功，实际: %v", err)
	}
}

// TestBoundedPingerRespectsCallerDeadline 锁定「不覆盖调用方已有期限」这条硬约束在探测路径上同样成立。
//
// 调用方带来更紧的期限（请求级超时）时，失败必须归因于**调用方自己的期限**，
// 而不是被报成「连接池耗尽」——否则排障方向会被误导到池容量上。
func TestBoundedPingerRespectsCallerDeadline(t *testing.T) {
	// 本层预算给足 10s：若被误用，本用例会等到 10s（超时收场）；调用方期限只有 200ms。
	f := openConnPoolFixture(t, "bounded_ping_dl", 1, 10000, 30000)
	pinger := NewBoundedPinger(f.db)
	if pinger == nil {
		t.Fatal("NewBoundedPinger 返回 nil")
	}
	release := f.occupySoleConnection(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := pinger.PingContext(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("池被占满且有调用方期限时探测应失败")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("探测耗时 %v，未在调用方期限（200ms）附近返回——调用方期限被忽略或本层预算误用", elapsed)
	}
	// 归因断言：不得把「调用方自己的期限到期」翻译成「等 DB 连接超时（池耗尽）」。
	if errors.Is(err, ErrDBWaitTimeout) {
		t.Fatalf("调用方自己的期限到期被误报成「等 DB 连接超时（池耗尽）」，排障方向会被误导: %v", err)
	}
	t.Logf("调用方期限生效：%v 内失败: %v", elapsed, err)
}

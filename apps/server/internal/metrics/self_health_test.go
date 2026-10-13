package metrics

import (
	"strings"
	"sync"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// selfHealthFixture 构造一个注入了 live 来源与命令队列缓存的指标集。
func selfHealthFixture(t *testing.T, live SelfHealth) *Metrics {
	t.Helper()
	m := New(runtime.NewRegistry())
	m.SetSelfHealthSource(func() SelfHealth { return live })
	return m
}

// TestSelfHealthMetricsExposed 验证自身健康的各类指标都被输出，且值正确。
func TestSelfHealthMetricsExposed(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{
		DBPoolMaxOpen: 4, DBPoolOpen: 3, DBPoolInUse: 2, DBPoolIdle: 1,
		DBPoolWaitCount: 9, DBPoolWaitDuration: 1.5,
		LongpollConfig: 1, LongpollFile: 2, LongpollTopology: 3, LongpollCommand: 4,
		MetricWriteDiscarded: 17,
	})
	m.SetCommandStatusSnapshot(map[string]int{"pending": 5, "failed": 2}, 1700000000)

	body := scrape(t, m)
	for _, want := range []string{
		`beacon_self_db_pool_connections{state="max_open"} 4`,
		`beacon_self_db_pool_connections{state="open"} 3`,
		`beacon_self_db_pool_connections{state="in_use"} 2`,
		`beacon_self_db_pool_connections{state="idle"} 1`,
		`beacon_self_db_pool_wait_total 9`,
		`beacon_self_db_pool_wait_duration_seconds_total 1.5`,
		`beacon_self_longpoll_waiters{channel="config"} 1`,
		`beacon_self_longpoll_waiters{channel="file"} 2`,
		`beacon_self_longpoll_waiters{channel="topology"} 3`,
		`beacon_self_longpoll_waiters{channel="command"} 4`,
		`beacon_self_commands{status="pending"} 5`,
		`beacon_self_commands{status="failed"} 2`,
		`beacon_self_metric_write_discarded_total 17`,
		`beacon_self_commands_snapshot_timestamp_seconds 1.7e+09`,
	} {
		if !containsSample(body, want) {
			t.Fatalf("自身健康指标应含样本 %s，实际：\n%s", want, body)
		}
	}
}

// TestSelfHealthAllCommandStatusesEmitted 验证所有已知状态都输出，含值为 0 的。
//
// 为什么要求含零值：某状态归零时 series 若消失，看板上会被读成"没有数据"而非"队列为空"；
// 固定输出全部枚举让曲线连续，也让「某状态从未出现」与「该状态刚清空」可区分。
func TestSelfHealthAllCommandStatusesEmitted(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{})
	// 只给一个状态非零，其余应输出 0。
	m.SetCommandStatusSnapshot(map[string]int{"pending": 1}, 1700000000)

	body := scrape(t, m)
	for _, status := range commandStatuses {
		want := `beacon_self_commands{status="` + status + `"} 0`
		if status == "pending" {
			want = `beacon_self_commands{status="pending"} 1`
		}
		if !containsSample(body, want) {
			t.Fatalf("状态 %s 应输出（含零值），实际缺样本 %s：\n%s", status, want, body)
		}
	}
}

// TestSelfHealthUnknownCommandStatusSurfaced 验证未知状态**不被静默丢弃**。
//
// 未知状态意味着出现了代码未预期的取值（如新增枚举后漏改本包清单）。静默丢弃会让它
// 永远不被发现；输出后看板上会直接多出一条 series，运维一看即知。
func TestSelfHealthUnknownCommandStatusSurfaced(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{})
	m.SetCommandStatusSnapshot(map[string]int{"pending": 1, "brand_new_status": 42}, 1700000000)

	body := scrape(t, m)
	if !containsSample(body, `beacon_self_commands{status="brand_new_status"} 42`) {
		t.Fatalf("未知状态应照常输出（不静默丢弃），实际：\n%s", body)
	}
}

// TestSelfHealthWriteQueueExposed 验证写入队列深度与容量被输出（见 ADR-0091 补充）。
//
// 为什么锁这条：队列深度是 429 背压出现**之前**唯一的预警信号。此前该缺口已在
// OPERATIONS 记录（「429 出现前运维从指标上看不到积压」），本用例确保它真的被暴露。
func TestSelfHealthWriteQueueExposed(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{
		WriteQueueDepth:    map[string]int{"metric_sample": 1234, "health_snapshot": 0},
		WriteQueueCapacity: 4096,
	})

	body := scrape(t, m)
	for _, want := range []string{
		`beacon_self_write_queue_depth{route="metric_sample"} 1234`,
		// 深度为 0 的路由也要输出：否则「队列空」与「没采到」在看板上无法区分。
		`beacon_self_write_queue_depth{route="health_snapshot"} 0`,
		`beacon_self_write_queue_capacity 4096`,
	} {
		if !containsSample(body, want) {
			t.Fatalf("应含样本 %s，实际：\n%s", want, body)
		}
	}
}

// TestSelfHealthWriteQueueOmittedWhenNoRoutes 验证未注册路由时不输出队列指标。
//
// 容量为 0 意味着还没有任何路由注册（装配早期）。此时输出 depth=0 会误导成
// 「队列是空的、很健康」，而事实是「根本还没装配」——两者含义完全不同。
func TestSelfHealthWriteQueueOmittedWhenNoRoutes(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{WriteQueueCapacity: 0, WriteQueueDepth: map[string]int{}})

	body := scrape(t, m)
	if strings.Contains(body, "beacon_self_write_queue_capacity") {
		t.Fatalf("未注册路由时不应输出队列容量（会被误读成队列空而非未装配）：\n%s", body)
	}
}

// TestSelfHealthMetricNamesStable 锁定自身健康的指标名清单。
//
// 为什么单独锁名字：这些名字会出现在运维文档、告警规则与看板里，改名会让它们**静默失效**
// （PromQL 查不到序列只会返回空，不报错）。本用例把名字钉住，改名必须同步改这里，
// 从而强制改动者一并去想文档与告警规则。
func TestSelfHealthMetricNamesStable(t *testing.T) {
	// fixture 须把所有指标都激活（含写入队列），否则该指标名不会出现在输出里、断言会误判为改名。
	m := selfHealthFixture(t, SelfHealth{
		WriteQueueDepth:    map[string]int{"metric_sample": 1},
		WriteQueueCapacity: 4096,
	})
	m.SetCommandStatusSnapshot(map[string]int{"pending": 1}, 1700000000)

	body := scrape(t, m)
	for _, name := range []string{
		"beacon_self_db_pool_connections",
		"beacon_self_db_pool_wait_total",
		"beacon_self_db_pool_wait_duration_seconds_total",
		"beacon_self_longpoll_waiters",
		"beacon_self_commands",
		"beacon_self_metric_write_discarded_total",
		"beacon_self_commands_snapshot_timestamp_seconds",
		"beacon_self_write_queue_depth",
		"beacon_self_write_queue_capacity",
	} {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Fatalf("指标名 %s 应出现在抓取输出中（改名会静默打断既有看板与告警）", name)
		}
	}
}

// TestSelfHealthCommandStatusListMatchesModel 锁定 commandStatuses 与 model 的状态常量一致。
//
// 本包刻意不 import model（保持底层观测包不耦合数据模型），代价是清单需要手工同步；
// 这条用例就是那道防线——漏同步会让某状态静默缺失。
func TestSelfHealthCommandStatusListMatchesModel(t *testing.T) {
	// 与 internal/model/enums.go 的 CommandStatus* 常量逐字对应。
	want := []string{"pending", "fetched", "ready", "done", "failed", "expired"}
	if len(commandStatuses) != len(want) {
		t.Fatalf("commandStatuses 应含 %d 项，实际 %d 项：%v", len(want), len(commandStatuses), commandStatuses)
	}
	for i, s := range want {
		if commandStatuses[i] != s {
			t.Fatalf("第 %d 项应为 %q，实际 %q（与 model 的 CommandStatus* 漂移）", i, s, commandStatuses[i])
		}
	}
}

// TestSelfHealthCommandSnapshotRetainedOnNil 验证采集失败（传 nil）时保留上一轮值。
//
// 语义理由见 SetCommandByStatus 注释：清空会把"采集失败"伪装成"队列为空"。
func TestSelfHealthCommandSnapshotRetainedOnNil(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{})
	m.SetCommandStatusSnapshot(map[string]int{"pending": 8}, 1700000000)
	// 模拟本轮采集失败：不应覆盖已有缓存，也不应更新时间戳。
	m.SetCommandStatusSnapshot(nil, 1700009999)

	body := scrape(t, m)
	if !containsSample(body, `beacon_self_commands{status="pending"} 8`) {
		t.Fatalf("采集失败应保留上一轮计数，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_self_commands_snapshot_timestamp_seconds 1.7e+09`) {
		t.Fatalf("采集失败不应推进时间戳（否则陈旧度判据失真），实际：\n%s", body)
	}
}

// TestSelfHealthSnapshotIsolation 验证 Snapshot 返回的 map 是副本，外部改写不影响内部缓存。
func TestSelfHealthSnapshotIsolation(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{})
	m.SetCommandStatusSnapshot(map[string]int{"pending": 3}, 1700000000)

	snap := m.selfHealth.Snapshot()
	snap.CommandByStatus["pending"] = 999 // 篡改副本

	again := m.selfHealth.Snapshot()
	if again.CommandByStatus["pending"] != 3 {
		t.Fatalf("外部改写副本不应影响内部缓存，实际 %d", again.CommandByStatus["pending"])
	}
}

// TestSelfHealthConcurrentScrapeAndRefresh 验证并发抓取与后台刷新不产生数据竞争。
//
// 必须用 -race 跑才有判别力：抓取读缓存与刷新写缓存是天然并发路径。
// 判别力：去掉 SelfHealthCollector 里的锁，本用例在 -race 下立即变红。
func TestSelfHealthConcurrentScrapeAndRefresh(t *testing.T) {
	m := selfHealthFixture(t, SelfHealth{DBPoolOpen: 2})

	// 刷新协程持续写缓存，直到抓取协程全部结束。
	stop := make(chan struct{})
	var refresher sync.WaitGroup
	refresher.Add(1)
	go func() {
		defer refresher.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				m.SetCommandStatusSnapshot(map[string]int{"pending": i}, float64(1700000000+i))
			}
		}
	}()

	// 并发抓取：与刷新协程读写同一缓存。
	var scrapers sync.WaitGroup
	for i := 0; i < 4; i++ {
		scrapers.Add(1)
		go func() {
			defer scrapers.Done()
			for j := 0; j < 50; j++ {
				_ = scrape(t, m)
			}
		}()
	}
	scrapers.Wait() // 先等抓取结束，再停刷新协程，避免刷新提前退出导致竞争窗口消失
	close(stop)
	refresher.Wait()
}

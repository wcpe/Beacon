package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestOrchestratorStallMapConcurrentSafe 锁定 P0 回归：stallByOrder 的读写必须全程持锁。
//
// clearObserve（含其调用的 clearStall）有两条调用方：
//   - Cancel / applyConfirmBatch —— 已持 s.mu 同步调用；
//   - 审批执行适配器的 afterCommit 闭包 —— 由审批 worker 在事务提交后执行，**全程不持 s.mu**。
//
// 而推进器每 2s 在 mu 下经 detectStall 读写同一 map。若无独立锁保护，两侧即为并发读 + 写，
// Go 会抛 `fatal error: concurrent map read and map write`（不可 recover，控制面直接崩）。
// 触发点恰是本 FR 最核心的「确认门」场景（确认批 → clearObserve），故必须锁住。
//
// 本用例必须在 `-race` 下运行才有判别力（无竞态检测器时并发写 map 可能侥幸不 panic）。
func TestOrchestratorStallMapConcurrentSafe(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 推到确认门：此后 detectStall 每轮都会读 + 写该单的停滞观测。
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick()
	h.advance(6 * time.Second)
	h.tick()
	batches, err := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if err != nil {
		t.Fatalf("读批次失败: %v", err)
	}
	if batches[0].Status != model.ChangeBatchStatusAwaitingConfirm {
		t.Fatalf("应已到确认门，实际 %s", batches[0].Status)
	}
	// 先让停滞观测真的落到 map 里（过首报点后 detectStall 会写）。
	h.advance(2 * deliveryConfirmGateRemindAfter)
	h.tick()

	// 起跑门：两侧同时开工，避免「快的一侧在慢的一侧启动前就跑完」导致假绿。
	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	// 一侧：推进器在 mu 下推进（内部走 detectStall 读写 stallByOrder）；每轮带 DB 访问，较慢。
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 50; i++ {
			h.orch.advanceActiveOrders(context.Background())
		}
		close(done)
	}()
	// 另一侧：模拟审批 worker 的 afterCommit —— 不持 mu，直接清观察窗（内含 clearStall）。
	// 持续跑到推进侧结束为止，确保两侧在整个推进期间真实重叠（而不是快侧提前跑完）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for {
			select {
			case <-done:
				return
			default:
				h.orch.clearObserve(order.ID)
			}
		}
	}()
	close(start)
	wg.Wait()
}

package service

// —— 方案 C 不变量：审批批准执行**不请求 s.mu**（P0 死锁环的另一半）——
//
// 生产事故（2026-10-10 02:41:05）的环由两半反向持锁构成：批准执行「持连接等 mu」× 推进器 tick
// 「持 mu 等连接」。事故当时的部署是单连接池（`max-open-conns: 1`），池内没有第三条连接可破环，
// 故**两半必须各自被锁死**。环本身的复现见 delivery_deadlock_repro_test.go
// （任一半修好环即无法闭合，故那个用例判的是环，会随任一半转绿）；本文件锁的是 C 那一半自身的不变量：
//
//	1. TestApprovalExecutionMustNotWaitForMutexWhileHoldingConnection
//	   —— mu 被他人长期持有时，批准执行仍必须完成（修复前它持着唯一连接等 mu → 永久互等）。
//	2. TestApprovalStartRollsBackWithOuterTransaction
//	   —— 审批执行与启动仍在同一个外层事务里（FR-259）：事务回滚即无任何启动痕迹。

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestApprovalExecutionMustNotWaitForMutexWhileHoldingConnection 锁定方案 C 的不变量：
// **批准执行不得在持有事务连接的同时请求 `s.mu`**。
//
// 复现配方（确定性，不依赖调度序）：把 `s.mu` 交给测试自身长期持有（等价于「推进器 tick 或某个
// 控制操作正持 mu」，生产中此时它可能正在 mu 内等连接），再跑**真实**审批执行链
// （提交 → 批准 → 真 ApprovalWorker.RunOnce → 外层事务 → registry.ExecuteInTx →
// executeDeliveryApprovalInTx → applyStartApprovedInTx）。修复前 worker 会在
// `applyStartApprovedInTx` 的 `mu.Lock()` 上永久阻塞，且**仍持有连接池唯一那条连接**——
// 这正是生产现场「审批行停在 executing、所有需 DB 端点挂起」的形态。
//
// 为什么必须与连接占用一起判：仅断言「mu 被持有期间 worker 完成」还不够锋利——
// 若实现只是把等锁挪到事务外，本用例会误导性地通过而环仍在。故本用例同时要求
// 执行期间连接池是在用状态（证明它确实走完了「持连接」的那段），且完成时单已 rolling。
//
// 修复前：worker 在观察窗内零推进（mu 被持有 + 池在用=1，即互等环的 A 半）→ 本用例失败；
// 修复后：启动照常落库（approve→rolling + 批次 + 目标 + 审计），本用例转绿。
func TestApprovalExecutionMustNotWaitForMutexWhileHoldingConnection(t *testing.T) {
	env := p0NewReproEnv(t, p0PlainSettings{})

	// 建单 → 提审 → 批准（生产现场的三步管理面动作）。
	order := env.draftOrder(t, "cd")
	ticket, err := env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "c-submit", "ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := env.approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), "10.0.0.1"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}

	// 由测试自己长期持有 mu：模拟「另一条路径正持 mu」（生产中它此刻可能在 mu 内等连接）。
	env.orch.mu.Lock()
	muHeld := true
	defer func() {
		if muHeld {
			env.orch.mu.Unlock()
		}
	}()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, _ = env.worker().RunOnce(t.Context())
	}()

	// 观察窗内 mu 始终在测试手里：若 worker 请求 mu，它必然阻塞满整个窗口。
	// **本判定必须用非阻塞手段**——卡住时那条唯一的连接正被 worker 占着，
	// 任何回读（env.orders.Get / 计数查询）自身都要连接，会一起挂到测试超时而不是干净失败。
	if !waitUntilStuckOrDone(workerDone, p0DeadlockWindow) {
		inUse := p0PoolInUse(t, env.db) // sql.DB.Stats() 不申请连接
		env.orch.mu.Unlock()
		muHeld = false
		t.Fatalf("批准执行在持有事务连接的同时等待 s.mu（互等环的 A 半，生产 2026-10-10 02:41:05）：\n"+
			"  mu 由外部持有整整 %s 内 worker 零推进（池在用=%d，上限 1）\n"+
			"  该 worker 的 goroutine 此时仍攥着连接池唯一那条连接等 mu —— 生产现场即「审批行停在 executing +\n"+
			"  所有需 DB 端点挂起」，且 mu 的持有者若正在等这条连接则永久互等，busy_timeout 无效\n"+
			"  （它等的是 database/sql 连接池排队，不读 ctx 超时，根本没进 SQLite 层）\n"+
			"  修复方向：applyStartApprovedInTx 不得请求 s.mu——它运行在审批 worker 的外层事务里，\n"+
			"  该事务已取走连接池唯一那条连接", p0DeadlockWindow, inUse)
	}

	// 已完成：归还 mu 后校验启动真的落库（FR-259 的一致性由 TestApprovalStartRollsBackWithOuterTransaction 另行锁定）。
	env.orch.mu.Unlock()
	muHeld = false
	got, err := env.orders.Get(order.ID)
	if err != nil {
		t.Fatalf("回读变更单失败: %v", err)
	}
	if got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("批准执行完成但单未进入 rolling：实际 %s", got.Status)
	}
	repo := repository.NewChangeOrderRepository(env.db)
	batches, err := repo.ListBatches(order.ID)
	if err != nil || len(batches) == 0 {
		t.Fatalf("启动应固化批次：%v / %+v", err, batches)
	}
	targets, err := repo.ListTargetsByOrder(order.ID)
	if err != nil || len(targets) != 2 {
		t.Fatalf("启动应固化 2 台目标：%v / %+v", err, targets)
	}
}

// TestApprovalStartRollsBackWithOuterTransaction 锁定方案 C 改动不得破坏的既有保证（FR-259）：
// **审批执行（pending_approval→approved）与启动（approved→rolling + 批次 + 目标 + 审计）
// 仍在同一个外层事务里**——外层事务回滚时库内不得留下任何启动痕迹。
//
// 为什么必须有这条：本方案为消除死锁去掉了 `applyStartApprovedInTx` 的 `s.mu`，而「它必须保持事务性」
// 是这项改动的硬约束（启动是高风险动作，半成品单会让运维看到一张既非 approved 又未真正 rolling 的单）。
// 用例直接驱动**生产执行链**（registry.ExecuteInTx，与 delivery_dangerous_approval.go 的适配器同款入口），
// 在启动落库之后让外层事务失败，再断言库内为零痕迹。
//
// 用 e2e 事件面观测而非仅读状态：状态字段可能被后续流程改写，而「批次 / 目标 / 上传命令」是启动的
// 实体产物——它们只要有一行残留，就说明启动被拆成了两个事务。
func TestApprovalStartRollsBackWithOuterTransaction(t *testing.T) {
	env := p0NewReproEnv(t, p0PlainSettings{})
	order := env.draftOrder(t, "ef")
	if _, err := env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "c-tx", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("提审失败: %v", err)
	}

	req := p0ExecutingApprovalRequest(t, env, order.ID)
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, env.orders, env.orch)
	if err := env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	if err := env.db.Create(&model.ApprovalRequest{
		RequestID: req.RequestID, OperationKey: req.OperationKey, OperationKind: req.Operation.Kind,
		SchemaVersion: req.SchemaVersion, RequiredCapability: req.RequiredCapability,
		ResourceType: req.Operation.Resource, ResourceID: req.Operation.ResourceID, RequestReason: req.Operation.Reason,
		Payload: string(req.Payload), FrozenPayloadSHA256: req.PayloadHash, Status: req.Status,
		LeaseOwner: req.LeaseOwner, LeaseUntil: req.LeaseUntil, DeciderType: req.DeciderType, DeciderID: req.DeciderID,
		ApprovedAt: req.ApprovedAt, ApprovedBy: ptrStringValue("human:admin"), Version: req.Version,
	}).Error; err != nil {
		t.Fatalf("写入审批行失败: %v", err)
	}

	rollbackErr := errors.New("模拟外层事务在审批执行之后失败")
	afterCommitReturned := false
	err := env.db.Transaction(func(tx *gorm.DB) error {
		afterCommit, execErr := registry.ExecuteInTx(tx, req.RequestID, req.LeaseOwner)
		if execErr != nil {
			return execErr
		}
		afterCommitReturned = afterCommit != nil
		// 关掉事务：启动的一切产物都必须随之消失。
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("应以外层事务失败收场，实际 %v", err)
	}
	if !afterCommitReturned {
		t.Fatal("启动应给出提交后回执（唤醒推进器 / 通知模板源），实际为 nil")
	}

	// 单必须回到提审前的状态：既不是 approved 也不是 rolling。
	got, err := env.orders.Get(order.ID)
	if err != nil {
		t.Fatalf("回读变更单失败: %v", err)
	}
	if got.Status != model.ChangeOrderStatusPendingApproval {
		t.Fatalf("外层事务回滚后单应仍为待审批，实际 %s（启动被拆成了两个事务）", got.Status)
	}
	repo := repository.NewChangeOrderRepository(env.db)
	if batches, err := repo.ListBatches(order.ID); err != nil || len(batches) != 0 {
		t.Fatalf("回滚后不得残留批次：%v / %+v", err, batches)
	}
	if targets, err := repo.ListTargetsByOrder(order.ID); err != nil || len(targets) != 0 {
		t.Fatalf("回滚后不得残留目标：%v / %+v", err, targets)
	}
	var commands int64
	if err := env.db.Model(&model.AgentCommand{}).Where("type = ?", model.CommandTypeDeliveryUpload).
		Count(&commands).Error; err != nil || commands != 0 {
		t.Fatalf("回滚后不得残留上传命令：%v / %d", err, commands)
	}
	var receipts int64
	if err := env.db.Model(&model.ApprovalExecutionReceipt{}).Where("request_id = ?", req.RequestID).
		Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatalf("回滚后不得残留执行回执：%v / %d", err, receipts)
	}
}

// p0ExecutingApprovalRequest 按某单当前事实构造一条**可被注册表签发许可**的 executing 审批请求
// （冻结载荷 = 该单当前快照摘要），供直接驱动 registry.ExecuteInTx 的事务性用例使用。
func p0ExecutingApprovalRequest(t *testing.T, env *p0ReproEnv, orderID uint) authz.ApprovalRequest {
	t.Helper()
	stored, err := env.orders.requireOrder(orderID)
	if err != nil {
		t.Fatalf("读取变更单失败: %v", err)
	}
	snapshotHash, err := deliveryOrderSnapshotHash(env.orders.repo, stored)
	if err != nil {
		t.Fatalf("计算冻结摘要失败: %v", err)
	}
	payload, err := json.Marshal(deliveryApprovePayload{
		OrderID: orderID, ExpectedStatus: model.ChangeOrderStatusPendingApproval,
		SnapshotHash: snapshotHash, Operator: "admin", ClientIP: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("编码冻结载荷失败: %v", err)
	}
	sum := sha256.Sum256(payload)
	now := time.Now().UTC()
	return authz.ApprovalRequest{
		RequestID: "apr-c-tx-1", OperationKey: authz.OperationDeliveryApprove,
		Operation: authz.Operation{Kind: authz.OperationDeliveryApprove, Resource: model.TargetTypeChangeOrder,
			ResourceID: strconv.FormatUint(uint64(orderID), 10), Reason: "已确认影响面"},
		SchemaVersion: approvalSchemaVersion, RequiredCapability: auth.CapabilityApprovalRequest,
		Payload: payload, PayloadHash: fmt.Sprintf("%x", sum[:]), Status: model.ApprovalStatusExecuting,
		LeaseOwner: "lease-c-tx", LeaseUntil: ptrTime(now.Add(time.Minute)), DeciderType: auth.PrincipalKindHuman,
		DeciderID: "admin", ApprovedAt: &now, Actor: "admin", Version: 1,
	}
}

// ptrStringValue 取字符串指针（审批行的 ApprovedBy 列用）。
func ptrStringValue(v string) *string { return &v }

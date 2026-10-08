//go:build integration

package service

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestDeliveryTargetFactsMySQLConditionalUpdate 集成验证（真实 MySQL）失败回执落目标行的**方言相关**口径：
//
//	① 计数「只增不减」由 SQL 条件更新实现（`CASE WHEN ? > changed_file_count`）：更小的下界值不覆写已落定值，
//	   更大的值才写；0 计数 + 无备份的回执不碰既有事实；
//	② 推送阶段失败但盘上已改（计数 > 0 或备份已在）→ 一并补 `pushed_at`，使该目标进入整单回滚候选
//	   （`CountTargetsToRollback` 以 `pushed_at IS NOT NULL` 为准）；未动盘的失败不补。
//
// 单测（sqlite）已覆盖同一逻辑；本用例补 MySQL 方言 + 真实 schema 下的等价性（FR-266 终审 P2）。
func TestDeliveryTargetFactsMySQLConditionalUpdate(t *testing.T) {
	db := testsupport.OpenTestDB(t, "svc_target_facts")
	repo := repository.NewChangeOrderRepository(db)
	orch := NewDeliveryOrchestrator(db, repo, nil, nil, nil, nil, nil, nil)

	ns := model.Namespace{Code: "facts", Name: "失败事实口径"}
	mustCreate(t, db, &ns)
	order := model.ChangeOrder{
		NamespaceID: ns.ID, Title: "事实口径", Status: model.ChangeOrderStatusRolling,
		ActivationMethod: model.ActivationMethodHotReload,
	}
	mustCreate(t, db, &order)
	batch := model.ChangeBatch{OrderID: order.ID, BatchNo: 1, Status: model.ChangeBatchStatusRunning}
	mustCreate(t, db, &batch)
	rt := &orderRuntime{order: &order, batchNoByID: map[uint]int{batch.ID: 1}}

	// ① 生效阶段失败回执按配置工件数取下界（2），不得覆写推送阶段已落定的 3。
	smaller := seedFactsTarget(t, db, order.ID, batch.ID, "t-smaller", model.ChangeTargetStatusActivating, 3, true)
	orch.failTargetWithResult(rt, smaller, model.ChangeTargetStatusActivating, "生效失败", deliveryCmdResult{ChangedFileCount: 2, BackupPresent: true})
	assertFactsRow(t, db, smaller.ID, model.ChangeTargetStatusFailed, 3, true, false)

	// ② 更大的回执值才写（3 → 5）。
	bigger := seedFactsTarget(t, db, order.ID, batch.ID, "t-bigger", model.ChangeTargetStatusActivating, 3, true)
	orch.failTargetWithResult(rt, bigger, model.ChangeTargetStatusActivating, "生效失败", deliveryCmdResult{ChangedFileCount: 5, BackupPresent: true})
	assertFactsRow(t, db, bigger.ID, model.ChangeTargetStatusFailed, 5, true, false)

	// ③ 0 计数 + 无备份的回执不碰既有事实（保持 4 / true）。
	zero := seedFactsTarget(t, db, order.ID, batch.ID, "t-zero", model.ChangeTargetStatusActivating, 4, true)
	orch.failTargetWithResult(rt, zero, model.ChangeTargetStatusActivating, "关服失败", deliveryCmdResult{})
	assertFactsRow(t, db, zero.ID, model.ChangeTargetStatusFailed, 4, true, false)

	// ④ 推送中途失败（计数 1 + 备份在盘）→ failed 且补 pushed_at，计入整单回滚候选。
	partial := seedFactsTarget(t, db, order.ID, batch.ID, "t-partial", model.ChangeTargetStatusPushing, 0, false)
	orch.failPushingWithResult(rt, partial, "覆盖中途失败（已变更 1 项，备份已在盘）", deliveryCmdResult{ChangedFileCount: 1, BackupPresent: true})
	assertFactsRow(t, db, partial.ID, model.ChangeTargetStatusFailed, 1, true, true)

	// ⑤ 下载 / 备份阶段失败（未动盘）→ failed 但不补 pushed_at，不入回滚候选。
	untouched := seedFactsTarget(t, db, order.ID, batch.ID, "t-untouched", model.ChangeTargetStatusPushing, 0, false)
	orch.failPushingWithResult(rt, untouched, "流式下载 / 校验失败", deliveryCmdResult{})
	assertFactsRow(t, db, untouched.ID, model.ChangeTargetStatusFailed, 0, false, false)

	candidates, err := repo.CountTargetsToRollback(order.ID)
	if err != nil {
		t.Fatalf("统计回滚候选失败: %v", err)
	}
	if candidates != 1 {
		t.Fatalf("回滚候选应只有「曾覆盖磁盘」的那 1 个（t-partial），实际 %d", candidates)
	}
}

// seedFactsTarget 种一行目标（指定初态 / 初始化计数 / 备份标记；pushed_at 一律先空，由被测路径决定是否补）。
func seedFactsTarget(t *testing.T, db *gorm.DB, orderID, batchID uint, serverID, status string, changed int, backup bool) *model.ChangeTarget {
	t.Helper()
	row := model.ChangeTarget{
		OrderID: orderID, BatchID: batchID, ServerID: serverID, Status: status,
		ChangedFileCount: changed, BackupPresent: backup,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("种目标行失败: %v", err)
	}
	return &row
}

// assertFactsRow 断言目标行的事实口径（状态 / 计数 / 备份标记 / pushed_at 有无）。
func assertFactsRow(t *testing.T, db *gorm.DB, id uint, wantStatus string, wantChanged int, wantBackup, wantPushed bool) {
	t.Helper()
	var row model.ChangeTarget
	if err := db.First(&row, id).Error; err != nil {
		t.Fatalf("查目标行失败: %v", err)
	}
	if row.Status != wantStatus {
		t.Fatalf("状态期望 %s，实际 %s", wantStatus, row.Status)
	}
	if row.ChangedFileCount != wantChanged {
		t.Fatalf("计数期望 %d，实际 %d", wantChanged, row.ChangedFileCount)
	}
	if row.BackupPresent != wantBackup {
		t.Fatalf("备份标记期望 %v，实际 %v", wantBackup, row.BackupPresent)
	}
	if (row.PushedAt != nil) != wantPushed {
		t.Fatalf("pushed_at 命中期望 %v，实际 %v（%v）", wantPushed, row.PushedAt, time.Now())
	}
}

//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestRollbackRecordTargetResultWriteBackMySQL 真 MySQL：逐台结果写回必须在**目标表与子查询同表**时不报 1093。
//
// 背景（FR-271 返工的 P1）：归属判定天然要读 change_rollback_record_target 自身（「该台仍在途的最早动作」）。
// 若把它写成 `UPDATE change_rollback_record_target ... WHERE record_id = (SELECT MIN(record_id)
// FROM change_rollback_record_target ...)`，MySQL 直接报 ER_UPDATE_TABLE_USED（错误 1093）——
// 而写回失败只告警不阻断推进，症状就是「逐台结果在真库永停在途、控制面毫无异常」，
// 恰恰是本 FR 要修的东西在生产复活。SQLite 不报该错，故必须由本用例兜住。
// 现实现是两步走（先 SELECT 出归属 id，再按字面值 UPDATE），本用例同时锁住语义与不报错。
func TestRollbackRecordTargetResultWriteBackMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5rb_record")
	repo := NewChangeOrderRepository(db)

	order := &model.ChangeOrder{
		NamespaceID: 1, Title: "回滚记录写回", Status: model.ChangeOrderStatusCompleted,
		BatchMode: "percent", BatchSizes: "[100]", ActivationMethod: model.ActivationMethodPushOnly,
		ObserveWindowSec: 5, ActivateTimeoutSec: 60, PayloadState: model.PayloadStateReady,
		CreatedBy: "it-ops",
	}
	if err := repo.Create(order); err != nil {
		t.Fatalf("建单失败: %v", err)
	}

	// 两条动作记录（id 递增），其中 t-2 只属于较早那条（A）——交错动作下不能被后发起的 B 抢走归属。
	pushedAt := time.Now().UTC()
	targets := []model.ChangeTarget{
		{OrderID: order.ID, ServerID: "t-1", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt, BackupPresent: true},
		{OrderID: order.ID, ServerID: "t-2", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt, BackupPresent: true},
	}
	if err := repo.CreateTargets(targets); err != nil {
		t.Fatalf("写目标失败: %v", err)
	}
	recordA := &model.ChangeRollbackRecord{
		OrderID: order.ID, Kind: model.RollbackKindTargets, Reason: "动作A", Operator: "it-ops", TargetCount: 2,
	}
	if err := repo.CreateRollbackRecord(recordA); err != nil {
		t.Fatalf("写动作A失败: %v", err)
	}
	if err := repo.CreateRollbackRecordTargets([]model.ChangeRollbackRecordTarget{
		{RecordID: recordA.ID, ServerID: "t-1", Result: model.RollbackStatusPending},
		{RecordID: recordA.ID, ServerID: "t-2", Result: model.RollbackStatusPending},
	}); err != nil {
		t.Fatalf("写动作A逐台行失败: %v", err)
	}
	recordB := &model.ChangeRollbackRecord{
		OrderID: order.ID, Kind: model.RollbackKindTargets, Reason: "动作B", Operator: "it-ops", TargetCount: 1,
	}
	if err := repo.CreateRollbackRecord(recordB); err != nil {
		t.Fatalf("写动作B失败: %v", err)
	}
	if err := repo.CreateRollbackRecordTargets([]model.ChangeRollbackRecordTarget{
		{RecordID: recordB.ID, ServerID: "t-1", Result: model.RollbackStatusPending},
	}); err != nil {
		t.Fatalf("写动作B逐台行失败: %v", err)
	}

	// 写回 t-2 的终态：归属应是**较早**的动作 A（B 的范围内没有 t-2）。
	// 这一步在旧实现（同表子查询）下会返回 1093 错误。
	if err := repo.UpdateRollbackRecordTargetResult(order.ID, "t-2", model.RollbackStatusRolledBack, ""); err != nil {
		t.Fatalf("MySQL 写回逐台结果失败（同表子查询会报 1093）: %v", err)
	}
	rowsA, err := repo.ListRollbackRecordTargets([]uint{recordA.ID})
	if err != nil {
		t.Fatalf("读动作A逐台行失败: %v", err)
	}
	if byID := indexRecordTargets(rowsA); byID["t-2"].Result != model.RollbackStatusRolledBack {
		t.Fatalf("t-2 的结果应写回动作A: %+v", rowsA)
	}
	rowsB, err := repo.ListRollbackRecordTargets([]uint{recordB.ID})
	if err != nil {
		t.Fatalf("读动作B逐台行失败: %v", err)
	}
	if byID := indexRecordTargets(rowsB); byID["t-1"].Result != model.RollbackStatusPending {
		t.Fatalf("动作B 的 t-1 不应被动作A 的写回波及: %+v", rowsB)
	}

	// t-1 先由 A 写终态、再由 B（更晚发起的重试）写终态：两次各归其主。
	if err := repo.UpdateRollbackRecordTargetResult(order.ID, "t-1", model.RollbackStatusFailed, "备份不存在"); err != nil {
		t.Fatalf("A 写回 t-1 失败: %v", err)
	}
	if err := repo.UpdateRollbackRecordTargetResult(order.ID, "t-1", model.RollbackStatusRolledBack, ""); err != nil {
		t.Fatalf("B 写回 t-1 失败: %v", err)
	}
	rowsA, _ = repo.ListRollbackRecordTargets([]uint{recordA.ID})
	rowsB, _ = repo.ListRollbackRecordTargets([]uint{recordB.ID})
	if byID := indexRecordTargets(rowsA); byID["t-1"].Result != model.RollbackStatusFailed {
		t.Fatalf("动作A 的 t-1 应保留 A 那次的结果: %+v", rowsA)
	}
	if byID := indexRecordTargets(rowsB); byID["t-1"].Result != model.RollbackStatusRolledBack {
		t.Fatalf("动作B 的 t-1 应是 B 那次的结果: %+v", rowsB)
	}

	// 无未终态行可归属时不得报错（幂等：记录已被清理 / 已全部终态）。
	if err := repo.UpdateRollbackRecordTargetResult(order.ID, "t-9", model.RollbackStatusRolledBack, ""); err != nil {
		t.Fatalf("无归属行应静默返回: %v", err)
	}
}

// TestInitTargetRollbackCASMySQL 真 MySQL：置回滚初态的 CAS 条件含 `IS NULL` 与 `NOT IN`，
// 空态（NULL）必须被正确判为「可置态」，而在途态必须不被迁移。
func TestInitTargetRollbackCASMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5rb_cas")
	repo := NewChangeOrderRepository(db)

	order := &model.ChangeOrder{
		NamespaceID: 1, Title: "回滚置态 CAS", Status: model.ChangeOrderStatusCompleted,
		BatchMode: "percent", BatchSizes: "[100]", ActivationMethod: model.ActivationMethodPushOnly,
		ObserveWindowSec: 5, ActivateTimeoutSec: 60, PayloadState: model.PayloadStateReady,
		CreatedBy: "it-ops",
	}
	if err := repo.Create(order); err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	pushedAt := time.Now().UTC()
	if err := repo.CreateTargets([]model.ChangeTarget{
		{OrderID: order.ID, ServerID: "t-1", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt, BackupPresent: true},
		{OrderID: order.ID, ServerID: "t-2", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt, BackupPresent: false},
		{OrderID: order.ID, ServerID: "t-3", Status: model.ChangeTargetStatusPending},
		// 已终态的两种前态：failed（重试）/ rolled_back（再次回滚）——都在允许集内，必须命中
		{OrderID: order.ID, ServerID: "t-4", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt,
			BackupPresent: false, RollbackStatus: model.RollbackStatusFailed, RollbackError: "备份不存在"},
		{OrderID: order.ID, ServerID: "t-5", Status: model.ChangeTargetStatusActivated, PushedAt: &pushedAt,
			BackupPresent: true, RollbackStatus: model.RollbackStatusRolledBack},
		// 未推送但已带终态回滚态（历史数据 / 人工改库）：它不在回滚目标集内，
		// 但「值已等于目标值」会让「0 行回读只看是否在途」的写法误判成命中——回读必须同时判 pushed_at。
		{OrderID: order.ID, ServerID: "t-6", Status: model.ChangeTargetStatusSkipped,
			BackupPresent: false, RollbackStatus: model.RollbackStatusFailed, RollbackError: "备份不存在"},
	}); err != nil {
		t.Fatalf("写目标失败: %v", err)
	}
	byID := map[string]model.ChangeTarget{}
	rows, err := repo.ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("读目标失败: %v", err)
	}
	for _, row := range rows {
		byID[row.ServerID] = row
	}

	// 空态（NULL）＋有备份 → 命中并置 pending
	if ok, err := repo.InitTargetRollbackCAS(byID["t-1"].ID, true, "备份不存在"); err != nil || !ok {
		t.Fatalf("空态目标应命中 CAS: %v / %v", ok, err)
	}
	// 空态（NULL）＋无备份 → 命中并置 failed ＋原因
	if ok, err := repo.InitTargetRollbackCAS(byID["t-2"].ID, false, "备份不存在"); err != nil || !ok {
		t.Fatalf("空态无备份目标应命中 CAS: %v / %v", ok, err)
	}
	// 已在 pending（在途）→ 不命中、行不变
	if ok, err := repo.InitTargetRollbackCAS(byID["t-1"].ID, true, "备份不存在"); err != nil || ok {
		t.Fatalf("在途目标不得命中 CAS: %v / %v", ok, err)
	}
	// 从未推送（pushed_at 为空）→ 不命中
	if ok, err := repo.InitTargetRollbackCAS(byID["t-3"].ID, true, "备份不存在"); err != nil || ok {
		t.Fatalf("未推送目标不得命中 CAS: %v / %v", ok, err)
	}
	// 已 failed（终态、非空态）→ 命中（重试语义），置回 pending
	if ok, err := repo.InitTargetRollbackCAS(byID["t-4"].ID, true, "备份不存在"); err != nil || !ok {
		t.Fatalf("已失败目标应命中 CAS（重试）: %v / %v", ok, err)
	}
	// 已 failed 且**以相同原因再次置态**：MySQL affected rows 是 changed rows，此时可能返回 0 行，
	// 但行其实命中了 CAS 条件——0 行必须回读区分，不得把合法重试误拒成 409。
	if err := db.Model(&model.ChangeTarget{}).Where("id = ?", byID["t-4"].ID).
		Updates(map[string]any{"rollback_status": model.RollbackStatusFailed, "rollback_error": "备份不存在"}).Error; err != nil {
		t.Fatalf("回置失败态失败: %v", err)
	}
	if ok, err := repo.InitTargetRollbackCAS(byID["t-4"].ID, false, "备份不存在"); err != nil || !ok {
		t.Fatalf("值与现值相同的置态必须仍判命中（changed-rows 语义不得误拒）: %v / %v", ok, err)
	}
	// 已 rolled_back（终态、非空态）→ 命中（再次回滚）
	if ok, err := repo.InitTargetRollbackCAS(byID["t-5"].ID, true, "备份不存在"); err != nil || !ok {
		t.Fatalf("已回滚目标应命中 CAS: %v / %v", ok, err)
	}
	// 未推送但回滚态已等于目标值（0 行 + 值相同）：回读必须判 pushed_at，不得当成「无变化写入」而误命中
	if ok, err := repo.InitTargetRollbackCAS(byID["t-6"].ID, false, "备份不存在"); err != nil || ok {
		t.Fatalf("未推送且值相同的目标不得命中 CAS: %v / %v", ok, err)
	}
	// 不存在的行 → 不命中且不报错
	if ok, err := repo.InitTargetRollbackCAS(byID["t-5"].ID+9999, true, "备份不存在"); err != nil || ok {
		t.Fatalf("不存在的目标不得命中: %v / %v", ok, err)
	}

	rows, _ = repo.ListTargetsByOrder(order.ID)
	got := map[string]model.ChangeTarget{}
	for _, row := range rows {
		got[row.ServerID] = row
	}
	if got["t-1"].RollbackStatus != model.RollbackStatusPending || got["t-1"].RollbackError != "" {
		t.Fatalf("t-1 应为 pending 且无原因: %+v", got["t-1"])
	}
	if got["t-2"].RollbackStatus != model.RollbackStatusFailed || got["t-2"].RollbackError != "备份不存在" {
		t.Fatalf("t-2 应为 failed 且带原因: %+v", got["t-2"])
	}
	if got["t-3"].RollbackStatus != "" {
		t.Fatalf("t-3 不应被置态: %q", got["t-3"].RollbackStatus)
	}
	if got["t-4"].RollbackStatus != model.RollbackStatusFailed || got["t-4"].RollbackError != "备份不存在" {
		t.Fatalf("t-4 应停在 failed 且原因不变: %+v", got["t-4"])
	}
	if got["t-5"].RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("t-5 应被重新置为 pending: %+v", got["t-5"])
	}
	if got["t-6"].RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("t-6 应保持原样（未被置态）: %+v", got["t-6"])
	}
}

// indexRecordTargets 把逐台结果行按 serverId 建索引（断言用）。
func indexRecordTargets(rows []model.ChangeRollbackRecordTarget) map[string]model.ChangeRollbackRecordTarget {
	index := make(map[string]model.ChangeRollbackRecordTarget, len(rows))
	for _, row := range rows {
		index[row.ServerID] = row
	}
	return index
}

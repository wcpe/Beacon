package service

import (
	"path/filepath"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestReconcileSystemExecutionsWithProductionVersionShape 以生产口径复现版本形态失配：
// 落库的 target_version 来自 FrozenTarget.Version（= GitHub tag，形如 "v1.5.0"，带 v 前缀），
// 而启动对账传入的 currentVersion 来自 version.Version（= VERSION 文件，形如 "1.5.0"，无 v 前缀）。
//
// 本用例刻意用生产口径（带 v 的 target + 无 v 的 current），与既有四条用例（两侧都带 v）不同源——
// 正因既有用例两侧同源，该失配才长期未被发现。
func TestReconcileSystemExecutionsWithProductionVersionShape(t *testing.T) {
	db := newSystemExecutionTestDB(t, "system-execution-prod-shape")
	execution := model.SystemExecution{
		RequestID: "apr-prod-shape", Operation: "system.update.apply",
		Nonce: "nonce-prod-shape", Status: model.SystemExecutionStatusPending,
		TargetVersion: "v1.5.0", // FrozenTarget.Version 形态：来自 GitHub tag
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败：%v", err)
	}

	// version.Version 形态：来自 VERSION 文件，不带 v 前缀。
	if err := ReconcileSystemExecutions(db, filepath.Join(t.TempDir(), "beacon"), "1.5.0"); err != nil {
		t.Fatalf("对账失败：%v", err)
	}

	if err := db.First(&execution, execution.ID).Error; err != nil {
		t.Fatalf("读取执行记录失败：%v", err)
	}
	if execution.Status != model.SystemExecutionStatusSucceeded {
		t.Fatalf("v1.5.0 与 1.5.0 是同一版本，换版应判成功，实际 %s（原因 %q）", execution.Status, execution.FailureReason)
	}
}

// TestReconcileSystemExecutionsWithCandidateVersionShape 锁定候选构建下的形态差异（ADR-0092）：
// 候选实例的 currentVersion 带候选后缀，冻结目标是 GA tag——二者仍属同一正式版本，应判成功。
func TestReconcileSystemExecutionsWithCandidateVersionShape(t *testing.T) {
	db := newSystemExecutionTestDB(t, "system-execution-candidate-shape")
	execution := model.SystemExecution{
		RequestID: "apr-candidate-shape", Operation: "system.update.apply",
		Nonce: "nonce-candidate-shape", Status: model.SystemExecutionStatusPending,
		TargetVersion: "v1.5.0",
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败：%v", err)
	}

	if err := ReconcileSystemExecutions(db, filepath.Join(t.TempDir(), "beacon"), "1.5.0-rc.2"); err != nil {
		t.Fatalf("对账失败：%v", err)
	}

	if err := db.First(&execution, execution.ID).Error; err != nil {
		t.Fatalf("读取执行记录失败：%v", err)
	}
	if execution.Status != model.SystemExecutionStatusSucceeded {
		t.Fatalf("v1.5.0 与 1.5.0-rc.2 同属 1.5.0，换版应判成功，实际 %s（原因 %q）", execution.Status, execution.FailureReason)
	}
}

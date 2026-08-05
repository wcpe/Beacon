package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func TestReconcileSystemExecutionsMarksUnverifiedUpdateFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-execution-reconcile?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemExecution{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	execution := model.SystemExecution{RequestID: "apr-test", Operation: "system.update.apply", Nonce: "nonce-test", Status: model.SystemExecutionStatusPending, TargetVersion: "v9.9.9"}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	if err := ReconcileSystemExecutions(db, t.TempDir()+"/beacon", "v1.0.0"); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if err := db.First(&execution, execution.ID).Error; err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if execution.Status != model.SystemExecutionStatusFailed {
		t.Fatalf("未达到冻结版本应失败，实际 %s", execution.Status)
	}
}

func TestReconcileSystemExecutionsMarksStableUpdateSucceeded(t *testing.T) {
	db := newSystemExecutionTestDB(t, "system-execution-stable")
	execution := model.SystemExecution{RequestID: "apr-stable", Operation: "system.update.apply", Nonce: "nonce-stable", Status: model.SystemExecutionStatusRunning, TargetVersion: "v2.0.0"}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	if err := ReconcileSystemExecutions(db, filepath.Join(t.TempDir(), "beacon"), "v2.0.0"); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	assertSystemExecutionStatus(t, db, execution.ID, model.SystemExecutionStatusSucceeded)
}

func TestReconcileSystemExecutionsKeepsPendingVerificationRunning(t *testing.T) {
	db := newSystemExecutionTestDB(t, "system-execution-running")
	dir := t.TempDir()
	runPath := filepath.Join(dir, "beacon")
	if err := os.WriteFile(filepath.Join(dir, "beacon.update-pending"), []byte(`{"attempt":1,"version":"v2.0.0"}`), 0o600); err != nil {
		t.Fatalf("写验证标记失败: %v", err)
	}
	execution := model.SystemExecution{RequestID: "apr-running", Operation: "system.update.apply", Nonce: "nonce-running", Status: model.SystemExecutionStatusPending, TargetVersion: "v2.0.0"}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	if err := ReconcileSystemExecutions(db, runPath, "v2.0.0"); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	assertSystemExecutionStatus(t, db, execution.ID, model.SystemExecutionStatusRunning)
}

func TestReconcileSystemExecutionsMarksRollbackResult(t *testing.T) {
	db := newSystemExecutionTestDB(t, "system-execution-rollback")
	execution := model.SystemExecution{RequestID: "apr-rollback", Operation: "system.update.rollback", Nonce: "nonce-rollback", Status: model.SystemExecutionStatusPending, TargetVersion: "v1.0.0"}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	if err := ReconcileSystemExecutions(db, filepath.Join(t.TempDir(), "beacon"), "v1.0.0"); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	assertSystemExecutionStatus(t, db, execution.ID, model.SystemExecutionStatusSucceeded)
}

func newSystemExecutionTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemExecution{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func assertSystemExecutionStatus(t *testing.T, db *gorm.DB, id uint, want string) {
	t.Helper()
	var execution model.SystemExecution
	if err := db.First(&execution, id).Error; err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if execution.Status != want {
		t.Fatalf("执行状态不正确：want=%s got=%s", want, execution.Status)
	}
}

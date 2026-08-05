package service

import (
	"log/slog"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/update"
	"gorm.io/gorm"
)

// ReconcileSystemExecutions 在数据库装配后补记自替换进程留下的领域执行事实。
func ReconcileSystemExecutions(db *gorm.DB, runPath, currentVersion string) error {
	if db == nil {
		return nil
	}
	var executions []model.SystemExecution
	if err := db.Where("operation IN ? AND status IN ?", []string{"system.update.apply", "system.update.rollback"}, []string{model.SystemExecutionStatusPending, model.SystemExecutionStatusRunning}).Find(&executions).Error; err != nil {
		return err
	}
	for _, execution := range executions {
		status, failure := reconciledSystemExecutionStatus(execution, runPath, currentVersion)
		if err := db.Model(&model.SystemExecution{}).Where("id = ? AND status IN ?", execution.ID, []string{model.SystemExecutionStatusPending, model.SystemExecutionStatusRunning}).Updates(map[string]any{"status": status, "failure_reason": failure}).Error; err != nil {
			return err
		}
	}
	return nil
}

func reconciledSystemExecutionStatus(execution model.SystemExecution, runPath, currentVersion string) (string, string) {
	if execution.TargetVersion != currentVersion {
		return model.SystemExecutionStatusFailed, "换版未达到冻结目标版本"
	}
	if execution.Operation == "system.update.apply" && update.PendingUpdateVersion(runPath) != "" {
		return model.SystemExecutionStatusRunning, ""
	}
	return model.SystemExecutionStatusSucceeded, ""
}

// MarkSystemExecutionFailed 将异步触发前后出现的领域失败持久化为可查询事实。
func MarkSystemExecutionFailed(db *gorm.DB, requestID string, err error) {
	if db == nil || requestID == "" || err == nil {
		return
	}
	if updateErr := db.Model(&model.SystemExecution{}).Where("request_id = ? AND status IN ?", requestID, []string{model.SystemExecutionStatusPending, model.SystemExecutionStatusRunning}).Updates(map[string]any{"status": model.SystemExecutionStatusFailed, "failure_reason": redact.DesensitizeErr(err)}).Error; updateErr != nil {
		slog.Error("写入系统执行失败对账失败", "申请", requestID, "错误", updateErr)
		return
	}
}

// MarkSystemExecutionSucceeded 在稳定验证回调中补记换版成功。
func MarkSystemExecutionSucceeded(db *gorm.DB, targetVersion string) error {
	if db == nil || targetVersion == "" {
		return nil
	}
	return db.Model(&model.SystemExecution{}).Where("operation = ? AND target_version = ? AND status = ?", "system.update.apply", targetVersion, model.SystemExecutionStatusRunning).
		Updates(map[string]any{"status": model.SystemExecutionStatusSucceeded, "failure_reason": ""}).Error
}

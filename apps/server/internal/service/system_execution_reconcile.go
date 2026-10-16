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

// reconciledSystemExecutionStatus 判定一次换版执行的事实状态。
//
// 版本比对必须按语义而非字符串相等：两个版本串的形态天然不同——
//   - execution.TargetVersion 来自 FrozenTarget.Version，即 GitHub tag，形如 "v1.5.0"（parseGATag 强制要求前导 v）；
//   - currentVersion 来自 version.Version，即根 VERSION 注入的运行时版本，形如 "1.5.0" 或候选构建的 "1.5.0-rc.2"（ADR-0092）。
//
// 字符串相等会让 apply 路径恒判失败（"v1.5.0" != "1.5.0"），故改比「正式版本三段是否相同」：
// 前导 v 与候选后缀都不影响同一版本的判定，v1.5.0 / 1.5.0 / 1.5.0-rc.2 视为同一版本。
// 任一侧无法解析时退回字符串相等（保守：不确定就不判成功）。
func reconciledSystemExecutionStatus(execution model.SystemExecution, runPath, currentVersion string) (string, string) {
	if !update.SameReleaseVersion(execution.TargetVersion, currentVersion) {
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

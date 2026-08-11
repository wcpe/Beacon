//go:build integration

package service

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// UndrainForIntegrationTest 仅供带 integration 标签的黑盒测试调用 V1 事务内状态迁移。
// 正式管理面仍必须经审批适配器，不能使用此测试构造。
func UndrainForIntegrationTest(s *SchedulingService, ns, serverID, operator, clientIP string) error {
	if s == nil || ns == "" || serverID == "" || operator == "" {
		return apperr.ErrInvalidParam
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.applyUndrainInTx(tx, ns, serverID, operator, clientIP)
	})
}

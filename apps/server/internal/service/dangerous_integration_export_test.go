//go:build integration

package service

import "github.com/wcpe/Beacon/apps/server/internal/model"

// AssignZoneForIntegrationTest 仅供带 integration 标签的黑盒测试构造既有 V1 指派状态。
// 正式管理面必须经审批适配器，不能调用此测试桥。
func AssignZoneForIntegrationTest(s *ZoneService, ns, serverID, group, zone, operator, note, clientIP string) (*model.ZoneAssignment, error) {
	return s.applyAssignForTest(ns, serverID, group, zone, operator, note, clientIP)
}

// SubmitDeliveryOrderForIntegrationTest 仅供带 integration 标签的黑盒测试构造待审批变更单状态。
// 正式管理面必须通过 RequestSubmit 创建统一审批申请。
func SubmitDeliveryOrderForIntegrationTest(s *DeliveryOrderService, id uint, operator, clientIP string) (*ChangeOrderDetailView, error) {
	return s.applySubmit(id, operator, clientIP)
}

// ApproveDeliveryOrderForIntegrationTest 仅供带 integration 标签的黑盒测试校验领域审批状态机。
// 正式管理面必须由统一审批 worker 在事务内执行。
func ApproveDeliveryOrderForIntegrationTest(s *DeliveryOrderService, id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	return s.applyApprove(id, reason, operator, clientIP)
}

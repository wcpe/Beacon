package service

import (
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

// RequestSetServerDefaultEntryByServerID 按稳定业务 serverId 创建默认入口变更审批。
// 行主键只在服务端解析，MCP 等外部调用方不得接触数据库行 ID。
func (s *V2ControlPlaneService) RequestSetServerDefaultEntryByServerID(serverID string, value bool, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	if serverID == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	server, err := findServerByServerID(s.db, serverID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return s.RequestSetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: server.ID, Value: value, Reason: reason, Operator: operator, ClientIP: clientIP}, principal, idempotencyKey)
}

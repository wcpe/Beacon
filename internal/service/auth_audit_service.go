package service

import (
	"encoding/json"

	"beacon/internal/model"
	"beacon/internal/repository"
)

// AuthAuditService 记录管理面登录 / 登出审计（FR-7/FR-30）。
// 认证为无状态令牌、无跨表写，故单条 append，不开事务；
// detail 仅记操作者，严禁含口令 / 令牌等敏感数据。
type AuthAuditService struct {
	auditRepo *repository.AuditLogRepository
}

// NewAuthAuditService 构造服务。
func NewAuthAuditService(auditRepo *repository.AuditLogRepository) *AuthAuditService {
	return &AuthAuditService{auditRepo: auditRepo}
}

// RecordLogin 记一条登录成功审计。
func (s *AuthAuditService) RecordLogin(operator, clientIP string) error {
	return s.record(operator, model.ActionAuthLogin, clientIP)
}

// RecordLogout 记一条登出审计。
func (s *AuthAuditService) RecordLogout(operator, clientIP string) error {
	return s.record(operator, model.ActionAuthLogout, clientIP)
}

// record 追加一条认证会话审计（namespace 留空——认证为全局操作）。
func (s *AuthAuditService) record(operator, action, clientIP string) error {
	detail, _ := json.Marshal(map[string]string{"operator": operator})
	return s.auditRepo.Create(&model.AuditLog{
		Operator:   operator,
		Action:     action,
		TargetType: model.TargetTypeAuth,
		TargetRef:  operator,
		Detail:     string(detail),
		Result:     model.ResultOK,
		ClientIP:   clientIP,
	})
}

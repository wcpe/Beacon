package service

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

var (
	errBCDirectoryResyncActive = apperr.New(409, "BC_DIRECTORY_RESYNC_ACTIVE", "该 BC 已有进行中的目录重同步命令")
	errBCOffline               = apperr.New(409, "BC_OFFLINE", "BC 当前不在线")
	errTargetNotBC             = apperr.New(409, "TARGET_NOT_BC", "目标不是已确认的 BC")
	errNoBCTargets             = apperr.New(409, "NO_BC_TARGETS", "当前 namespace 没有 BC 目标")
	errNoEligibleBC            = apperr.New(409, "NO_ELIGIBLE_BC", "当前没有可立即重同步的在线 BC")
	errServerNotFound          = apperr.New(404, "SERVER_NOT_FOUND", "服务器不存在")
)

// DirectoryResyncResult 是 namespace 级触发的一台 BC 的受理结果。
type DirectoryResyncResult struct {
	ServerID  string `json:"serverId"`
	Accepted  bool   `json:"accepted"`
	CommandID uint   `json:"commandId,omitempty"`
	Status    string `json:"status,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

// NamespaceDirectoryResyncResult 是 namespace 级立即重同步响应。
type NamespaceDirectoryResyncResult struct {
	NamespaceID uint                    `json:"namespaceId"`
	Requested   int                     `json:"requested"`
	Accepted    int                     `json:"accepted"`
	Rejected    int                     `json:"rejected"`
	Results     []DirectoryResyncResult `json:"results"`
}

// SetDirectoryResyncCommandPort 注入目录重同步所需的命令仓库与提交后唤醒器。
func (s *V2ControlPlaneService) SetDirectoryResyncCommandPort(repo *repository.AgentCommandRepository, notifier CommandNotifier) {
	s.directoryResyncRepo = repo
	s.directoryResyncNotifier = notifier
}

// RequestNamespaceDirectoryResync 对触发时刻快照内的全部 BC 逐台求值；一台失败不回滚其它目标。
func (s *V2ControlPlaneService) RequestNamespaceDirectoryResync(namespaceID uint, operator, clientIP string) (*NamespaceDirectoryResyncResult, error) {
	if namespaceID == 0 || strings.TrimSpace(operator) == "" || s.directoryResyncRepo == nil {
		return nil, apperr.ErrInvalidParam
	}
	ns, err := findNamespaceByID(s.db, namespaceID)
	if err != nil {
		return nil, err
	}
	var servers []model.Server
	if err := s.db.Where("namespace_id = ? AND kind = ? AND bc_cluster_id IS NOT NULL AND lifecycle = ?", ns.ID, model.ServerKindProxy, model.ServerLifecycleActive).Order("server_id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errNoBCTargets
	}
	result := &NamespaceDirectoryResyncResult{NamespaceID: ns.ID, Requested: len(servers), Results: make([]DirectoryResyncResult, 0, len(servers))}
	for i := range servers {
		item, requestErr := s.requestDirectoryResync(ns, &servers[i], operator, clientIP, "namespace")
		if requestErr == nil {
			result.Accepted++
		} else {
			result.Rejected++
			item = rejectionResult(servers[i].ServerID, requestErr)
		}
		result.Results = append(result.Results, item)
	}
	if result.Accepted == 0 {
		return result, errNoEligibleBC
	}
	if err := s.writeNamespaceDirectoryResyncAudit(ns, result, operator, clientIP); err != nil {
		return nil, err
	}
	return result, nil
}

// RequestServerDirectoryResync 对一台 BC 下发立即目录重同步。
func (s *V2ControlPlaneService) RequestServerDirectoryResync(serverRowID uint, operator, clientIP string) (DirectoryResyncResult, error) {
	if serverRowID == 0 || strings.TrimSpace(operator) == "" || s.directoryResyncRepo == nil {
		return DirectoryResyncResult{}, apperr.ErrInvalidParam
	}
	var server model.Server
	err := s.db.First(&server, serverRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DirectoryResyncResult{}, errServerNotFound
	}
	if err != nil {
		return DirectoryResyncResult{}, err
	}
	ns, err := findNamespaceByID(s.db, server.NamespaceID)
	if err != nil {
		return DirectoryResyncResult{}, err
	}
	return s.requestDirectoryResync(ns, &server, operator, clientIP, "server")
}

func (s *V2ControlPlaneService) requestDirectoryResync(ns *model.Namespace, server *model.Server, operator, clientIP, scope string) (DirectoryResyncResult, error) {
	if !isServerActive(server) {
		return DirectoryResyncResult{}, apperr.ErrServerArchived
	}
	if !s.isConfirmedOnlineBC(ns, server) {
		if server.Kind != model.ServerKindProxy || server.BCClusterID == nil || !s.hasActiveProxyIdentity(server) {
			return DirectoryResyncResult{}, errTargetNotBC
		}
		return DirectoryResyncResult{}, errBCOffline
	}
	s.directoryResyncMu.Lock()
	defer s.directoryResyncMu.Unlock()
	var command *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&model.AgentCommand{}).Where("namespace = ? AND server_id = ? AND type = ? AND status IN ?", ns.Code, server.ServerID,
			model.CommandTypeBCDirectoryResync, []string{model.CommandStatusPending, model.CommandStatusFetched}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errBCDirectoryResyncActive
		}
		command = &model.AgentCommand{NamespaceCode: ns.Code, ServerID: server.ServerID, Type: model.CommandTypeBCDirectoryResync, Payload: "{}", Status: model.CommandStatusPending, Operator: operator}
		if err := s.directoryResyncRepo.WithTx(tx).Create(command); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"commandId": command.ID, "namespace": ns.Code, "scope": scope})
		return createAudit(tx, model.AuditLog{NamespaceCode: ns.Code, Operator: operator, Action: model.ActionInstanceBCDirectoryResync,
			TargetType: model.TargetTypeInstance, TargetRef: server.ServerID, Detail: string(detail), Result: model.ResultOK, ClientIP: clientIP})
	})
	if err != nil {
		return DirectoryResyncResult{}, err
	}
	if s.directoryResyncNotifier != nil {
		s.directoryResyncNotifier.NotifyCommand(ns.Code, server.ServerID)
	}
	return DirectoryResyncResult{ServerID: server.ServerID, Accepted: true, CommandID: command.ID, Status: command.Status}, nil
}

func (s *V2ControlPlaneService) isConfirmedOnlineBC(ns *model.Namespace, server *model.Server) bool {
	if server.Kind != model.ServerKindProxy || server.BCClusterID == nil || !s.hasActiveProxyIdentity(server) || s.runtime == nil {
		return false
	}
	instance := s.runtime.Get(ns.Code, server.ServerID)
	return instance != nil && instance.Status == runtime.StatusOnline
}

func (s *V2ControlPlaneService) hasActiveProxyIdentity(server *model.Server) bool {
	var count int64
	err := s.db.Model(&model.AgentIdentity{}).Where("namespace_id = ? AND server_id = ? AND kind = ? AND status = ?", server.NamespaceID, server.ServerID,
		model.ServerKindProxy, model.AgentIdentityStatusActive).Count(&count).Error
	return err == nil && count > 0
}

func rejectionResult(serverID string, err error) DirectoryResyncResult {
	app, ok := err.(*apperr.Error)
	if !ok {
		return DirectoryResyncResult{ServerID: serverID, Code: "INTERNAL_ERROR", Message: "目录重同步创建失败"}
	}
	return DirectoryResyncResult{ServerID: serverID, Code: app.Code, Message: app.Message}
}

func (s *V2ControlPlaneService) writeNamespaceDirectoryResyncAudit(ns *model.Namespace, result *NamespaceDirectoryResyncResult, operator, clientIP string) error {
	accepted := make([]uint, 0, result.Accepted)
	for _, item := range result.Results {
		if item.Accepted {
			accepted = append(accepted, item.CommandID)
		}
	}
	detail, _ := json.Marshal(map[string]any{"namespace": ns.Code, "requested": result.Requested, "accepted": result.Accepted, "rejected": result.Rejected, "commandIds": accepted})
	return createAudit(s.db, model.AuditLog{NamespaceCode: ns.Code, Operator: operator, Action: model.ActionNamespaceBCDirectoryResync,
		TargetType: model.TargetTypeNamespace, TargetRef: ns.Code, Detail: string(detail), Result: model.ResultOK, ClientIP: clientIP})
}

// ReceiveDirectoryResyncResult 只接收当前已认证 BC 对其 fetched 命令的回执。
func (s *V2ControlPlaneService) ReceiveDirectoryResyncResult(identity agentauth.Identity, commandID uint, ok bool, reason string) error {
	if commandID == 0 || identity.Kind != model.ServerKindProxy || identity.Namespace == "" || identity.ServerID == "" {
		return apperr.ErrCommandNotFound
	}
	cmd, err := s.directoryResyncRepo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeBCDirectoryResync || cmd.Status != model.CommandStatusFetched || cmd.NamespaceCode != identity.Namespace || cmd.ServerID != identity.ServerID {
		return apperr.ErrCommandNotFound
	}
	status := model.CommandStatusDone
	detail := ""
	if !ok {
		status, detail = model.CommandStatusFailed, cleanDirectoryResyncReason(reason)
	}
	hit, err := s.directoryResyncRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, status, detail)
	if err != nil {
		return err
	}
	if !hit {
		return apperr.ErrCommandNotFound
	}
	return nil
}

func cleanDirectoryResyncReason(reason string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(reason))
	if len([]rune(clean)) > 512 {
		clean = string([]rune(clean)[:512])
	}
	if clean == "" {
		return "BC 目录重同步失败"
	}
	return clean
}

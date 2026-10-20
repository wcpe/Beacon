package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
)

// 浏览正文的数据库保留上限，是否存在授权均不影响此期限。
const browseResultTTL = 5 * time.Minute

// BrowseResultHub 是资产预览结果等待的窄接口（由 longpoll.Hub 实现）。
type BrowseResultHub interface {
	Register(ns, serverID string) *longpoll.Waiter
	Deregister(w *longpoll.Waiter)
	Notify(ns string, serverIDs []string)
}

// browsePayload 是 fs-browse 命令的载荷（落 agent_command.payload JSON，FR-110，见 ADR-0049）。
// op 区分三种只读操作；path 为相对 plugins 根的相对路径（list/tree 可空=列根）；
// offset/limit 仅 list 用（分页）、maxDepth 仅 tree 用（展开深度），agent 收口到各自硬上限。
type browsePayload struct {
	Op       string `json:"op"`
	Path     string `json:"path,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	MaxDepth int    `json:"maxDepth,omitempty"`
}

// BrowseParams 是 admin 触发文件浏览的入参（FR-110）。
type BrowseParams struct {
	Namespace string
	ServerID  string
	Op        string
	Path      string
	Offset    int
	Limit     int
	MaxDepth  int
	Operator  string
	ClientIP  string
}

type browseApprovalPayload struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Op        string `json:"op"`
	Path      string `json:"path"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	MaxDepth  int    `json:"maxDepth"`
	Operator  string `json:"operator"`
	ClientIP  string `json:"clientIP"`
}

// RequestBrowse 直接创建受限文件浏览命令并写专项审计。
func (s *AgentCommandService) RequestBrowse(_ context.Context, p BrowseParams) (string, error) {
	if s == nil || s.db == nil {
		return "", apperr.ErrInternal
	}
	if !validBrowseParams(p) {
		return "", apperr.ErrInvalidParam
	}
	var cmd *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cmd, err = s.applyRequestBrowseInTx(tx, p)
		return err
	})
	if err != nil {
		return "", err
	}
	if s.notifier != nil {
		s.notifier.NotifyCommand(p.Namespace, p.ServerID)
	}
	return strconv.FormatUint(uint64(cmd.ID), 10), nil
}

// RequestBrowseApproval 冻结受限浏览参数并创建审批申请；批准 worker 才能下发命令与待激活授权。
//
// Deprecated: 受限浏览改为直读（RequestBrowse）后本入口已无 handler 调用，
// 保留仅为兼容旧审批单的读取 / 回放；适配器注册保留使历史待执行审批仍可按原语义落地。
func (s *AgentCommandService) RequestBrowseApproval(p BrowseParams, reason, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	if s == nil || s.approval == nil {
		// 装配缺失是服务端问题，不把故障甩给调用方（此前与参数错误混用同一 400）。
		return ApprovalTicketView{}, apperr.ErrInternal
	}
	if reason == "" || idempotencyKey == "" || !validBrowseParams(p) {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := map[string]any{"namespace": p.Namespace, "serverId": p.ServerID, "op": p.Op, "path": p.Path, "offset": p.Offset, "limit": p.Limit, "maxDepth": p.MaxDepth, "operator": p.Operator, "clientIP": p.ClientIP}
	req, err := s.approval.Request(authz.Operation{Kind: authz.OperationAgentCommandFSBrowse, Resource: "agent-command", ResourceID: p.Namespace + "/" + p.ServerID, IdempotencyKey: idempotencyKey, Reason: reason}, payload, principal, p.ClientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey}, nil
}

func validBrowseParams(p BrowseParams) bool {
	return p.Namespace != "" && p.ServerID != "" && p.Operator != "" && model.IsValidBrowseOp(p.Op)
}

// applyRequestBrowseInTx 在同一领域事务中创建浏览命令和审计。
//
// Deprecated: 仅由旧审批适配器在领域事务内调用；直读改造后不再有 handler 走本入口，
// 保留仅为兼容旧审批单的执行 / 回放。
func (s *AgentCommandService) applyRequestBrowseInTx(tx *gorm.DB, p BrowseParams) (*model.AgentCommand, error) {
	if tx == nil || !validBrowseParams(p) {
		return nil, apperr.ErrInvalidParam
	}
	payload, _ := json.Marshal(browsePayload{Op: p.Op, Path: p.Path, Offset: p.Offset, Limit: p.Limit, MaxDepth: p.MaxDepth})
	cmd := &model.AgentCommand{NamespaceCode: p.Namespace, ServerID: p.ServerID, Type: model.CommandTypeFsBrowse, Payload: string(payload), Status: model.CommandStatusPending, Operator: p.Operator}
	if err := s.repo.WithTx(tx).Create(cmd); err != nil {
		return nil, err
	}
	// 用 json.Marshal 而非 `%q` 拼接：path 是外部可控文本，Go 的 %q 对控制字符输出 `\x01`
	// 而 JSON 不允许 `\x` 转义，会静默写出非法 detail（issue #144）。
	if err := s.auditRepo.WithTx(tx).Create(&model.AuditLog{NamespaceCode: p.Namespace, Operator: p.Operator, Action: model.ActionFileBrowse, TargetType: model.TargetTypeCommand, TargetRef: p.ServerID, Detail: auditJSON(map[string]any{"commandId": cmd.ID, "op": p.Op, "path": p.Path}), Result: model.ResultOK, ClientIP: p.ClientIP}); err != nil {
		return nil, err
	}
	return cmd, nil
}

// ReceiveBrowseResult 接收 agent 回传的文件浏览结果（FR-110）：命令须存在、type=fs-browse 且处 fetched。
//
// ok=true 则把结果 JSON 转存命令瞬态（browse_result）并 CAS fetched→done；ok=false 则 CAS fetched→failed
// 并只记服务端安全枚举摘要。result 是受控瞬态——绝不入审计、不落持久真源。
func (s *AgentCommandService) ReceiveBrowseResult(identity agentauth.Identity, commandID uint, ok bool, result, _ string) error {
	if s == nil {
		return apperr.ErrInternal
	}
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeFsBrowse {
		return apperr.ErrCommandNotFound
	}
	if identity.Namespace == "" || identity.ServerID == "" || cmd.NamespaceCode != identity.Namespace || cmd.ServerID != identity.ServerID {
		return apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	if !ok {
		return s.db.Transaction(func(tx *gorm.DB) error {
			hit, err := s.repo.WithTx(tx).UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusFailed, `{"error":"agent_browse_failed"}`)
			if err != nil {
				return err
			}
			if !hit {
				return apperr.ErrCommandNotFound
			}
			if s.grants == nil {
				return nil
			}
			return s.grants.WithTx(tx).RevokePendingCommandIfPresent(cmd.ID)
		})
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(result)))
	return s.db.Transaction(func(tx *gorm.DB) error {
		hit, err := s.repo.WithTx(tx).UpdateStatusWithBrowseResult(cmd.ID, result, time.Now().UTC().Add(browseResultTTL))
		if err != nil {
			return err
		}
		if !hit {
			return apperr.ErrCommandNotFound
		}
		if s.grants == nil {
			return nil
		}
		return s.grants.WithTx(tx).BindAndActivatePendingCommandFromAgentIfPresent(cmd.ID, authz.OperationAgentCommandFSBrowse, hash, time.Now().UTC())
	})
}

// ConsumeApprovedBrowse 仅允许原申请主体一次性取得已由 Agent 回传的浏览结果。
//
// Deprecated: 正文改为直读（ReadBrowseResult）后本入口已无 handler 调用，保留仅为兼容旧授权单的读取 / 回放。
func (s *AgentCommandService) ConsumeApprovedBrowse(grantID string, commandID uint, principal auth.Principal) (string, error) {
	if s == nil || s.grants == nil || grantID == "" || commandID == 0 {
		return "", apperr.ErrForbidden
	}
	var result string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		cmd, err := s.repo.WithTx(tx).FindByID(commandID)
		if err != nil || cmd == nil || cmd.Type != model.CommandTypeFsBrowse || cmd.Status != model.CommandStatusDone {
			return apperr.ErrForbidden
		}
		if cmd.BrowseResult == "" {
			return apperr.ErrSensitiveAccessConsumed
		}
		result = cmd.BrowseResult
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(result)))
		if err := s.grants.WithTx(tx).Consume(grantID, principal, authz.OperationAgentCommandFSBrowse, fmt.Sprintf("agent-command/%d", commandID), hash, time.Now().UTC()); err != nil {
			return err
		}
		cleared, err := s.repo.WithTx(tx).ClearBrowseResult(commandID, result)
		if err != nil {
			return err
		}
		if !cleared {
			return apperr.ErrSensitiveAccessConsumed
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// resolveCommandNamespaceScope 把「可能缺省」的 namespace 作用域解析为唯一的 namespace code。
//
// 命令 ID 是自增可枚举的，而 Server 的唯一键是 (namespace_id, server_id)，同名 serverId 可跨
// namespace 共存，因此读路径必须把请求限定到唯一 namespace，不能只比对裸 serverId。
// 显式给出 namespaceCode 时按 (server_id, namespace) 联合解析；缺省时按 server_id 反查——
// v1 路由 /admin/v1/instances/{serverId}/... 不带 namespace 参数，只能这样反查。
// 目标行不存在或同名 serverId 跨 namespace 共存导致歧义时统一返回 ErrCommandNotFound，
// 不区分「不存在」与「无权读」，避免向猜测方泄露命令是否存在。
func resolveCommandNamespaceScope(db *gorm.DB, serverID, namespaceCode string) (string, error) {
	if db == nil || serverID == "" {
		return "", apperr.ErrCommandNotFound
	}
	query := db.Model(&model.Server{}).Where("server_id = ?", serverID)
	if namespaceCode != "" {
		var explicit model.Namespace
		if err := db.Where("code = ?", namespaceCode).First(&explicit).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", apperr.ErrCommandNotFound
			}
			return "", err
		}
		query = query.Where("namespace_id = ?", explicit.ID)
	}
	// 多取一条即可判定歧义，避免把跨环境的同名实例全量拉出来。
	var servers []model.Server
	if err := query.Order("namespace_id asc").Limit(2).Find(&servers).Error; err != nil {
		return "", err
	}
	if len(servers) != 1 {
		return "", apperr.ErrCommandNotFound
	}
	var namespace model.Namespace
	if err := db.Where("id = ?", servers[0].NamespaceID).First(&namespace).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", apperr.ErrCommandNotFound
		}
		return "", err
	}
	return namespace.Code, nil
}

// ReadBrowseResult 直接读取指定命令的结果，并在同一事务内清理正文和记录元数据审计。
// namespace 为读请求的命名空间作用域（可为空，空则按 serverID 反查）；与命令归属不一致时按不存在拒绝。
func (s *AgentCommandService) ReadBrowseResult(commandID uint, namespace, serverID, operator, clientIP string) (string, error) {
	if s == nil || s.db == nil || s.auditRepo == nil {
		return "", apperr.ErrInternal
	}
	if commandID == 0 || serverID == "" || operator == "" {
		return "", apperr.ErrInvalidParam
	}
	var result string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		cmd, err := s.repo.WithTx(tx).FindByID(commandID)
		if err != nil {
			return err
		}
		if cmd == nil || cmd.ServerID != serverID || cmd.Type != model.CommandTypeFsBrowse || cmd.Status != model.CommandStatusDone {
			return apperr.ErrCommandNotFound
		}
		// namespace 作用域校验：没有这一步，凭枚举到的 commandID 即可读到别的环境同名实例的浏览正文。
		scopeNamespace, err := resolveCommandNamespaceScope(tx, serverID, namespace)
		if err != nil {
			return err
		}
		if cmd.NamespaceCode != scopeNamespace {
			return apperr.ErrCommandNotFound
		}
		if cmd.BrowseResult == "" {
			return apperr.ErrSensitiveAccessConsumed
		}
		// 正文独立 TTL 必须在读路径自己兜住：清理器周期等于 TTL，过期正文最长还能多读一整个周期，
		// 清理器失败时更是永久可读。nil 视为无过期限制（兼容历史数据），但不跳过其它校验。
		if cmd.BrowseResultExpiresAt != nil && !cmd.BrowseResultExpiresAt.After(time.Now().UTC()) {
			return apperr.ErrSensitiveAccessConsumed
		}
		var payload browsePayload
		if json.Unmarshal([]byte(cmd.Payload), &payload) != nil {
			return apperr.ErrForbidden
		}
		result = cmd.BrowseResult
		cleared, err := s.repo.WithTx(tx).ClearBrowseResult(commandID, result)
		if err != nil {
			return err
		}
		if !cleared {
			return apperr.ErrSensitiveAccessConsumed
		}
		// 读路径用独立动作名 ActionFileBrowseRead：与触发命令的 ActionFileBrowse 分开，
		// 否则审计页无法按 action 过滤「谁读过正文」。detail 只记元数据，绝不记正文。
		// 用 json.Marshal 而非字符串拼接：serverId / op / path 均为外部可控文本，拼接易产出非法 JSON。
		detail, err := json.Marshal(map[string]any{
			"commandId": commandID, "serverId": serverID, "op": payload.Op, "path": payload.Path, "bytes": len(result),
		})
		if err != nil {
			return err
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: cmd.NamespaceCode, Operator: operator, Action: model.ActionFileBrowseRead,
			TargetType: model.TargetTypeCommand, TargetRef: serverID,
			Detail: string(detail),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// ClearExpiredBrowseResults 清理所有已到独立过期时间且未消费的浏览正文，命令元数据仍保留。
func (s *AgentCommandService) ClearExpiredBrowseResults(now time.Time) (int64, error) {
	if s == nil || s.db == nil || s.repo == nil {
		return 0, apperr.ErrForbidden
	}
	return s.repo.ClearExpiredBrowseResults(now.UTC())
}

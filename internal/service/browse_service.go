package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
)

// browseWaitTimeout 是文件浏览请求等待 agent 回传的上限（FR-110）：
// 浏览是交互式请求 / 响应，agent 在线时应秒级回传；超时即判 agent 未及时回传（504）。
// 固定常量（不走运维设置）：交互浏览的等待窗口与长轮询挂起语义不同，独立取值、不耦合。
const browseWaitTimeout = 15 * time.Second

// BrowseResultHub 是文件浏览结果等待的窄接口（由 longpoll.Hub 实现，FR-110）：
// admin 请求先注册 waiter，agent 回传结果后 NotifyBrowse 唤醒等待者重查命令状态。
// 与命令待办唤醒（CommandNotifier.NotifyCommand）分立——那是唤醒 agent 拉命令，这是唤醒 admin 取结果。
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

// SetBrowseResultHub 注入文件浏览结果等待 Hub（启动时装配；未注入则 RequestBrowse 不可用）。
func (s *AgentCommandService) SetBrowseResultHub(h BrowseResultHub) { s.browseHub = h }

// RequestBrowse 由 admin 触发对某在线实例的只读文件浏览（FR-110，见 ADR-0049 决策 9）：
//
// 复用 agent_command 生命周期做请求 / 响应——先注册结果 waiter（杜绝注册前回传丢唤醒），再事务内建
// pending fs-browse 命令 + file.browse 审计（detail 仅 commandId/op/path，绝不含文件内容），提交后唤醒该
// agent SSE 发 command-pending；随后阻塞等待 agent 回传（ReceiveBrowseResult 转存结果 + CAS done + 唤醒结果
// waiter）：被唤醒即重查命令——done 则读出瞬态结果返回，failed 则按越权 / 非目录 / 非文本回 404，
// 超时（agent 离线 / 未及时回传）回 504。结果 JSON 原文返回（由 handler 透传给前端）。
func (s *AgentCommandService) RequestBrowse(ctx context.Context, p BrowseParams) (string, error) {
	if p.Namespace == "" || p.ServerID == "" || p.Operator == "" {
		return "", apperr.ErrInvalidParam
	}
	if !model.IsValidBrowseOp(p.Op) {
		return "", apperr.ErrInvalidParam
	}
	if s.browseHub == nil {
		return "", apperr.ErrInternal // 未装配（编程 / 装配错误）
	}
	payload, _ := json.Marshal(browsePayload{
		Op: p.Op, Path: p.Path, Offset: p.Offset, Limit: p.Limit, MaxDepth: p.MaxDepth,
	})
	cmd := &model.AgentCommand{
		NamespaceCode: p.Namespace, ServerID: p.ServerID,
		Type: model.CommandTypeFsBrowse, Payload: string(payload),
		Status: model.CommandStatusPending, Operator: p.Operator,
	}

	// 先注册结果 waiter，再建命令——消除「命令刚建、agent 极速回传、admin 尚未注册」的丢唤醒窗口
	// （与长轮询 / 变更点「先注册后算」同序）。
	waiter := s.browseHub.Register(p.Namespace, p.ServerID)
	defer s.browseHub.Deregister(waiter)

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
		// Create 后 cmd.ID 已回填，可入审计 detail（无敏感内容：仅 commandId/op/path）。
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: p.Namespace,
			Operator:      p.Operator, Action: model.ActionFileBrowse,
			TargetType: model.TargetTypeCommand, TargetRef: p.ServerID,
			Detail: fmt.Sprintf(`{"commandId":%d,"op":%q,"path":%q}`, cmd.ID, p.Op, p.Path),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		return "", err
	}
	// 提交成功后唤醒该 agent 的 SSE 流发 command-pending（agent 离线则无 waiter、等到超时 504）。
	if s.notifier != nil {
		s.notifier.NotifyCommand(p.Namespace, p.ServerID)
	}
	slog.Info("触发在线实例文件浏览", "namespace", p.Namespace, "serverId", p.ServerID,
		"op", p.Op, "path", p.Path, "commandId", cmd.ID, "operator", p.Operator)

	return s.awaitBrowseResult(ctx, waiter, cmd.ID)
}

// awaitBrowseResult 阻塞等待某 fs-browse 命令回传结果：被唤醒即重查命令状态，done 取结果、failed 报错、
// 超时 504。结果 waiter 唤醒是 best-effort 提示——每次唤醒 / 超时都据库内命令真态裁决（不轻信信号）。
func (s *AgentCommandService) awaitBrowseResult(ctx context.Context, waiter *longpoll.Waiter, commandID uint) (string, error) {
	deadline := time.Now().Add(browseWaitTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", apperr.ErrBrowseTimeout
		}
		// 每次最多等到 deadline；被唤醒（agent 回传）或超时都重查命令真态。
		waiter.Wait(ctx, remaining)
		if ctx.Err() != nil {
			return "", apperr.ErrBrowseTimeout // 客户端断连按超时处理（结果已无人取）
		}
		cmd, err := s.repo.FindByID(commandID)
		if err != nil {
			return "", err
		}
		if cmd == nil {
			return "", apperr.ErrCommandNotFound
		}
		switch cmd.Status {
		case model.CommandStatusDone:
			return cmd.BrowseResult, nil
		case model.CommandStatusFailed:
			// agent 回传越权 / 非目录 / 非文本（原语 null）→ 命令 failed，对外按目标不存在裁决。
			return "", apperr.ErrBrowseTargetNotFound
		case model.CommandStatusExpired:
			return "", apperr.ErrBrowseTimeout
		default:
			// 仍 pending / fetched：未回传，继续等到 deadline（被无关唤醒会立即重查、命中本分支再等）。
			continue
		}
	}
}

// ReceiveBrowseResult 接收 agent 回传的文件浏览结果（FR-110）：命令须存在、type=fs-browse 且处 fetched。
//
// ok=true 则把结果 JSON 转存命令瞬态（browse_result）并 CAS fetched→done；ok=false（agent 原语返回 null：
// 越权 / 非目录 / 非文本）则 CAS fetched→failed 记原因摘要（无敏感内容）。无论成败，提交后唤醒等待中的
// admin 结果 waiter 让其立即取结果（不必等到超时）。result 是受控瞬态——绝不入审计、不落持久真源。
func (s *AgentCommandService) ReceiveBrowseResult(ns, serverID string, commandID uint, ok bool, result, reason string) error {
	cmd, err := s.repo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeFsBrowse {
		return apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	// 唤醒等待者用命令自身的 ns/serverId（与 admin 注册 waiter 的键一致），不信请求体 ns/serverId 越键唤醒。
	defer func() {
		if s.browseHub != nil {
			s.browseHub.Notify(cmd.NamespaceCode, []string{cmd.ServerID})
		}
	}()
	if !ok {
		s.markFailed(cmd.ID, reason)
		return nil
	}
	hit, e := s.repo.UpdateStatusWithBrowseResult(cmd.ID, result)
	if e != nil {
		return e
	}
	if !hit {
		return apperr.ErrCommandNotFound // 被并发终结（前态不符）
	}
	slog.Info("收到文件浏览结果回传", "commandId", cmd.ID, "bytes", len(result))
	return nil
}

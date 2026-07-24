package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
)

const (
	// assetPreviewMaxBytes 文本预览 / diff 单文件上限（512 KiB，spec §4.5）：超此上限 agent 只回前 512 KiB 并标 truncated。
	assetPreviewMaxBytes = 512 * 1024
	// assetPreviewTimeout 预览 / diff 同步等待 agent 回传内容的上限（10 秒，spec §4.5）。
	assetPreviewTimeout = 10 * time.Second
	// SettingAssetsSensitivePathPatterns 敏感路径规则设置键（字符串数组 glob 的 JSON 文本，spec §3.3）。
	// **非热改白名单项**——只经专用端点 GET/PUT /admin/v2/assets/sensitive-rules 读写底层设置项，/settings 不重复暴露。
	SettingAssetsSensitivePathPatterns = "assets.sensitive-path-patterns"
)

// assetInstanceGetter 是命令下发前在线校验的窄接口（由 InstanceService 实现）：
// 目标 agent 不在内存注册表（离线）即 asset_agent_offline，不建命令空等超时。
type assetInstanceGetter interface {
	Get(ns, serverID string) (*runtime.Instance, error)
}

// AssetRef 定位一个文件资产（业务 serverId + 相对路径）。
type AssetRef struct {
	ServerID string
	Path     string
}

// PreviewParams 是一次文件内容预览请求（operator/clientIp 由鉴权链与请求上下文注入）。
type PreviewParams struct {
	ServerID string
	Path     string
	Reason   string
	Operator string
	ClientIP string
}

// DiffParams 是一次两侧文件内容 diff 请求。
type DiffParams struct {
	Left     AssetRef
	Right    AssetRef
	Reason   string
	Operator string
	ClientIP string
}

// AssetContentPayload 是 agent 回传的单文件内容（agent 面 /assets/content 请求体，瞬态）。
// 不含 sha256/size —— 预览 / diff 响应的 sha256/size 取控制面清单（file_asset）权威值（二进制全文哈希，agent 侧只读前缀无法算全文哈希）。
type AssetContentPayload struct {
	Binary    bool
	Truncated bool
	Content   string
	Error     string
}

// AssetPreviewResult 是预览响应（对齐 contracts AssetPreviewResponse；二进制时 Content 为 nil）。
type AssetPreviewResult struct {
	Content   *string
	Truncated bool
	Binary    bool
	SHA256    string
	Size      int64
	Sensitive bool
}

// AssetDiffSide 是 diff 一侧的内容与元数据。
type AssetDiffSide struct {
	ServerID string
	Path     string
	Content  string
	SHA256   string
}

// AssetDiffResult 是 diff 响应（对齐 contracts AssetDiffResponse；Identical 时两侧为空）。
type AssetDiffResult struct {
	Identical bool
	Left      *AssetDiffSide
	Right     *AssetDiffSide
}

// assetReadPayload 是 asset-read 命令的载荷（落 agent_command.payload JSON，FR-164 §4.5）。
type assetReadPayload struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"maxBytes"`
}

// assetContent 是 agent 回传的单文件内容（内存中继，**绝不落库**）。
type assetContent struct {
	binary    bool
	truncated bool
	content   string
	errMsg    string // agent 读失败原因（已脱敏），非空即读取失败
}

// assetReadHandle 是一次单文件读取的等待句柄（命令 id + 结果 waiter）。
type assetReadHandle struct {
	commandID uint
	waiter    *longpoll.Waiter
}

// assetContentRelay 在内存中转 agent 回传的文件内容（FR-164 §4.5）：
// 内容为受审计的瞬态数据，**绝不落库 / 不进审计 / 不缓存**——仅在等待器存活期间在内存中转一次。
type assetContentRelay struct {
	mu    sync.Mutex
	slots map[uint]*assetContent // key 存在=有 admin 正在等待；value nil=内容尚未到达
}

func newAssetContentRelay() *assetContentRelay {
	return &assetContentRelay{slots: make(map[uint]*assetContent)}
}

// expect 登记一个等待槽（admin 下发命令后、唤醒 agent 前调用）。
func (r *assetContentRelay) expect(id uint) {
	r.mu.Lock()
	r.slots[id] = nil
	r.mu.Unlock()
}

// forget 移除等待槽并丢弃任何未取内容（admin 退出时 defer 调用，防泄漏）。
func (r *assetContentRelay) forget(id uint) {
	r.mu.Lock()
	delete(r.slots, id)
	r.mu.Unlock()
}

// deposit 由 agent 侧投递内容；槽不存在（admin 已超时离开或尚未 expect）返回 false、内容丢弃不泄漏。
func (r *assetContentRelay) deposit(id uint, c *assetContent) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.slots[id]; !ok {
		return false
	}
	r.slots[id] = c
	return true
}

// take 由 admin 侧取内容；尚未到达返回 (nil,false)。
func (r *assetContentRelay) take(id uint) (*assetContent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.slots[id]
	if c == nil {
		return nil, false
	}
	return c, true
}

// AssetPreviewService 编排文件内容预览 / diff / 敏感规则（FR-164，spec v2-file-assets.md §4.5/§4.6/§4.7）。
// 控制面**不存文件内容**：preview 走 asset-read 命令下发 + agent 读盘回传 + 内存中继同步透传，内容瞬态不落库、不进审计。
// 敏感路径规则匹配在控制面执行（命令下发前拦截），agent 不感知敏感语义。
type AssetPreviewService struct {
	db          *gorm.DB
	cmdRepo     *repository.AgentCommandRepository
	assetRepo   *repository.FileAssetRepository
	settingRepo *repository.SettingRepository
	auditRepo   *repository.AuditLogRepository
	hub         BrowseResultHub
	notifier    CommandNotifier
	instances   assetInstanceGetter
	relay       *assetContentRelay
}

// NewAssetPreviewService 构造服务（hub 供 admin 注册结果 waiter、agent 回传后唤醒；notifier 唤醒目标 agent 拉命令）。
func NewAssetPreviewService(db *gorm.DB, cmdRepo *repository.AgentCommandRepository, assetRepo *repository.FileAssetRepository,
	settingRepo *repository.SettingRepository, auditRepo *repository.AuditLogRepository,
	hub BrowseResultHub, notifier CommandNotifier, instances assetInstanceGetter,
) *AssetPreviewService {
	return &AssetPreviewService{
		db: db, cmdRepo: cmdRepo, assetRepo: assetRepo, settingRepo: settingRepo, auditRepo: auditRepo,
		hub: hub, notifier: notifier, instances: instances, relay: newAssetContentRelay(),
	}
}

// Preview 预览单个文件内容（FR-164 §4.5）：
// 校验 → 存在性（404）→ 敏感规则（无 reason 命中即 403）→ 在线（离线 504）→ 下发 asset-read + 同步等回传 →
// 先写审计后返回（二进制只回元数据、超限标 truncated）。内容瞬态不落库、不进审计 detail、不缓存。
func (s *AssetPreviewService) Preview(ctx context.Context, p PreviewParams) (*AssetPreviewResult, error) {
	if p.ServerID == "" || p.Path == "" {
		return nil, apperr.ErrInvalidParam
	}
	srv, nsCode, err := s.resolveServer(p.ServerID)
	if err != nil {
		return nil, err
	}
	asset, err := s.assetRepo.FindByServerPath(srv.ID, p.Path)
	if err != nil {
		return nil, err
	}
	if asset == nil {
		return nil, apperr.ErrAssetNotFound
	}
	matcher, err := s.sensitiveMatcher()
	if err != nil {
		return nil, err
	}
	sensitive := matcher.matches(p.Path)
	if sensitive && strings.TrimSpace(p.Reason) == "" {
		return nil, apperr.ErrAssetSensitivePath
	}
	if _, e := s.instances.Get(nsCode, p.ServerID); e != nil {
		return nil, apperr.ErrAssetAgentOffline
	}
	content, err := s.readOne(ctx, nsCode, p.ServerID, p.Path, p.Operator)
	if err != nil {
		return nil, err
	}
	// 成功读取（含二进制元数据）：先写审计后返回（sensitive 放行标 sensitiveOverride + 原因原文，**绝不含内容**）。
	if err := s.recordPreviewAudit(p, srv.ServerID, nsCode, content, sensitive); err != nil {
		return nil, err
	}
	// sha256/size 取清单权威值（file_asset），非 agent 侧前缀读的哈希（二进制无法算全文哈希）。
	return &AssetPreviewResult{
		Content:   contentOrNil(content),
		Truncated: content.truncated,
		Binary:    content.binary,
		SHA256:    asset.SHA256,
		Size:      asset.Size,
		Sensitive: sensitive,
	}, nil
}

// Diff 比较两侧文件内容（FR-164 §4.5/§4.6）：
// 存在性 → 二进制 / 超限早拒（asset_diff_unsupported）→ 敏感规则 → 两侧清单哈希相同则短路 identical（不取内容）→
// 否则在线校验后并行取两侧内容 → 先写审计后返回。任一侧回传二进制 / 截断亦拒。
func (s *AssetPreviewService) Diff(ctx context.Context, p DiffParams) (*AssetDiffResult, error) {
	if p.Left.ServerID == "" || p.Left.Path == "" || p.Right.ServerID == "" || p.Right.Path == "" {
		return nil, apperr.ErrInvalidParam
	}
	leftSrv, leftNS, err := s.resolveServer(p.Left.ServerID)
	if err != nil {
		return nil, err
	}
	rightSrv, rightNS, err := s.resolveServer(p.Right.ServerID)
	if err != nil {
		return nil, err
	}
	leftAsset, rightAsset, err := s.diffAssets(leftSrv.ID, p.Left.Path, rightSrv.ID, p.Right.Path)
	if err != nil {
		return nil, err
	}
	// 二进制 / 超限早拒（清单元数据即判：is_text 提示 + 大小），避免无谓向 agent 取内容（对齐 devmock 顺序）。
	if !leftAsset.IsText || !rightAsset.IsText || leftAsset.Size > assetPreviewMaxBytes || rightAsset.Size > assetPreviewMaxBytes {
		return nil, apperr.ErrAssetDiffUnsupported
	}
	matcher, err := s.sensitiveMatcher()
	if err != nil {
		return nil, err
	}
	sensitive := matcher.matches(p.Left.Path) || matcher.matches(p.Right.Path)
	if sensitive && strings.TrimSpace(p.Reason) == "" {
		return nil, apperr.ErrAssetSensitivePath
	}
	// 两侧清单哈希相同：短路返回一致，不取内容（spec §4.5）。
	if leftAsset.SHA256 == rightAsset.SHA256 {
		if err := s.recordDiffAudit(p, leftSrv.ServerID, rightSrv.ServerID, leftNS, true, sensitive); err != nil {
			return nil, err
		}
		return &AssetDiffResult{Identical: true}, nil
	}
	if _, e := s.instances.Get(leftNS, p.Left.ServerID); e != nil {
		return nil, apperr.ErrAssetAgentOffline
	}
	if _, e := s.instances.Get(rightNS, p.Right.ServerID); e != nil {
		return nil, apperr.ErrAssetAgentOffline
	}
	leftContent, rightContent, err := s.readPair(ctx, leftNS, p.Left, rightNS, p.Right, p.Operator)
	if err != nil {
		return nil, err
	}
	if leftContent.binary || rightContent.binary || leftContent.truncated || rightContent.truncated {
		return nil, apperr.ErrAssetDiffUnsupported
	}
	if err := s.recordDiffAudit(p, leftSrv.ServerID, rightSrv.ServerID, leftNS, false, sensitive); err != nil {
		return nil, err
	}
	// sha256 取清单权威值（file_asset），与 identical 短路的比对口径一致。
	return &AssetDiffResult{
		Identical: false,
		Left:      &AssetDiffSide{ServerID: p.Left.ServerID, Path: p.Left.Path, Content: leftContent.content, SHA256: leftAsset.SHA256},
		Right:     &AssetDiffSide{ServerID: p.Right.ServerID, Path: p.Right.Path, Content: rightContent.content, SHA256: rightAsset.SHA256},
	}, nil
}

// diffAssets 查两侧清单行，任一缺失即 asset_not_found。
func (s *AssetPreviewService) diffAssets(leftServerRow uint, leftPath string, rightServerRow uint, rightPath string) (*model.FileAsset, *model.FileAsset, error) {
	left, err := s.assetRepo.FindByServerPath(leftServerRow, leftPath)
	if err != nil {
		return nil, nil, err
	}
	right, err := s.assetRepo.FindByServerPath(rightServerRow, rightPath)
	if err != nil {
		return nil, nil, err
	}
	if left == nil || right == nil {
		return nil, nil, apperr.ErrAssetNotFound
	}
	return left, right, nil
}

// ReceiveContent 接收 agent 回传的单文件内容（FR-164 §4.5）：命令须存在、type=asset-read、归属回传 agent 且处 fetched。
// CAS 推进命令 done（读失败则 failed），**内容只进内存中继绝不落库**，唤醒等待中的 admin。
func (s *AssetPreviewService) ReceiveContent(ns, serverID string, commandID uint, p AssetContentPayload) error {
	cmd, err := s.cmdRepo.FindByID(commandID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Type != model.CommandTypeAssetRead {
		return apperr.ErrCommandNotFound
	}
	// 校验回传 agent 拥有该命令（权威身份来自鉴权中间件，非请求体自报）：防跨 agent 越权投递内容。
	if cmd.NamespaceCode != ns || cmd.ServerID != serverID {
		return apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	next := model.CommandStatusDone
	if p.Error != "" {
		next = model.CommandStatusFailed
	}
	hit, e := s.cmdRepo.UpdateStatus(commandID, model.CommandStatusFetched, next, "")
	if e != nil {
		return e
	}
	if !hit {
		return apperr.ErrCommandNotFound // 被并发终结（前态不符）
	}
	delivered := s.relay.deposit(commandID, &assetContent{
		binary: p.Binary, truncated: p.Truncated, content: p.Content, errMsg: p.Error,
	})
	if s.hub != nil {
		s.hub.Notify(cmd.NamespaceCode, []string{cmd.ServerID})
	}
	slog.Info("收到文件资产内容回传", "commandId", commandID, "serverId", serverID,
		"binary", p.Binary, "truncated", p.Truncated, "delivered", delivered, "读失败", p.Error != "")
	return nil
}

// GetSensitiveRules 读当前敏感路径规则清单（无存储则返回内置默认种子，spec §3.3/§4.6）。
func (s *AssetPreviewService) GetSensitiveRules() ([]string, error) {
	return s.loadSensitivePatterns()
}

// PutSensitiveRules 整体替换敏感路径规则（spec §4.6）：归一 → 逐条 glob 可编译校验 → 事务内 Upsert + 写审计（前后差异）。
// 清空清单等价关闭敏感保护，允许但同样入审计。
func (s *AssetPreviewService) PutSensitiveRules(patterns []string, operator, clientIP string) ([]string, error) {
	cleaned := normalizeSensitivePatterns(patterns)
	if _, err := newSensitiveMatcher(cleaned); err != nil {
		return nil, apperr.ErrInvalidParam // 坏 glob 拒绝落库，防敏感保护静默失效
	}
	before, err := s.loadSensitivePatterns()
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(cleaned)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if _, e := s.settingRepo.WithTx(tx).Upsert(SettingAssetsSensitivePathPatterns, string(raw), model.SettingValueTypeString); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionAssetSensitiveRuleUpdate,
			TargetType: model.TargetTypeSettings, TargetRef: SettingAssetsSensitivePathPatterns,
			Detail: sensitiveRuleAuditDetail(before, cleaned), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	slog.Info("敏感路径规则已更新", "operator", operator, "改前条数", len(before), "改后条数", len(cleaned))
	return cleaned, nil
}

// readOne 下发单文件 asset-read 命令并同步等回传（超时 asset_preview_timeout、读失败 asset_read_failed）。
func (s *AssetPreviewService) readOne(ctx context.Context, ns, serverID, path, operator string) (*assetContent, error) {
	handle, err := s.dispatchRead(ns, serverID, path, operator)
	if err != nil {
		return nil, err
	}
	defer s.cleanup(handle)
	content, err := s.awaitContent(ctx, handle, time.Now().Add(assetPreviewTimeout))
	if err != nil {
		return nil, err
	}
	if content.errMsg != "" {
		slog.Warn("agent 回传文件资产读取失败", "serverId", serverID, "path", path, "原因", content.errMsg)
		return nil, apperr.ErrAssetReadFailed
	}
	return content, nil
}

// readPair 并行下发两侧 asset-read 命令并按共享 deadline 收结果（两侧 agent 并行读盘，总等待上限 assetPreviewTimeout）。
func (s *AssetPreviewService) readPair(ctx context.Context, leftNS string, left AssetRef, rightNS string, right AssetRef, operator string) (*assetContent, *assetContent, error) {
	leftH, err := s.dispatchRead(leftNS, left.ServerID, left.Path, operator)
	if err != nil {
		return nil, nil, err
	}
	defer s.cleanup(leftH)
	rightH, err := s.dispatchRead(rightNS, right.ServerID, right.Path, operator)
	if err != nil {
		return nil, nil, err
	}
	defer s.cleanup(rightH)
	deadline := time.Now().Add(assetPreviewTimeout)
	leftContent, err := s.awaitContent(ctx, leftH, deadline)
	if err != nil {
		return nil, nil, err
	}
	if leftContent.errMsg != "" {
		return nil, nil, apperr.ErrAssetReadFailed
	}
	rightContent, err := s.awaitContent(ctx, rightH, deadline)
	if err != nil {
		return nil, nil, err
	}
	if rightContent.errMsg != "" {
		return nil, nil, apperr.ErrAssetReadFailed
	}
	return leftContent, rightContent, nil
}

// dispatchRead 注册结果 waiter + 中继槽 → 建 asset-read 命令 → 唤醒目标 agent 拉命令。
// 先注册 waiter，再建命令并 expect 槽、随后才 notify——消除「命令刚建、agent 极速回传、admin 尚未登记」的丢唤醒窗口。
func (s *AssetPreviewService) dispatchRead(ns, serverID, path, operator string) (*assetReadHandle, error) {
	waiter := s.hub.Register(ns, serverID)
	payload, _ := json.Marshal(assetReadPayload{Path: path, MaxBytes: assetPreviewMaxBytes})
	cmd := &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeAssetRead, Payload: string(payload),
		Status: model.CommandStatusPending, Operator: operator,
	}
	if err := s.cmdRepo.Create(cmd); err != nil {
		s.hub.Deregister(waiter)
		return nil, err
	}
	s.relay.expect(cmd.ID)
	if s.notifier != nil {
		s.notifier.NotifyCommand(ns, serverID)
	}
	slog.Info("下发文件资产读取命令", "namespace", ns, "serverId", serverID, "path", path,
		"commandId", cmd.ID, "operator", operator)
	return &assetReadHandle{commandID: cmd.ID, waiter: waiter}, nil
}

// cleanup 摘除 waiter 并丢弃中继槽（每次读取结束 defer 调用，防泄漏）。
func (s *AssetPreviewService) cleanup(handle *assetReadHandle) {
	s.hub.Deregister(handle.waiter)
	s.relay.forget(handle.commandID)
}

// awaitContent 阻塞等待某 asset-read 命令回传内容：被唤醒即查中继，超时 / 断连按 asset_preview_timeout 处理。
func (s *AssetPreviewService) awaitContent(ctx context.Context, handle *assetReadHandle, deadline time.Time) (*assetContent, error) {
	for {
		if c, ok := s.relay.take(handle.commandID); ok {
			return c, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, apperr.ErrAssetPreviewTimeout
		}
		handle.waiter.Wait(ctx, remaining)
		if ctx.Err() != nil {
			return nil, apperr.ErrAssetPreviewTimeout // 客户端断连按超时处理（结果已无人取）
		}
	}
}

// resolveServer 把业务 serverId 解析为 server 行 + 所属 namespace code（契约无 namespaceId，按 serverId 全局解析首条命中）。
func (s *AssetPreviewService) resolveServer(serverID string) (*model.Server, string, error) {
	var srv model.Server
	err := s.db.Where("server_id = ?", serverID).First(&srv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", apperr.ErrAssetNotFound
	}
	if err != nil {
		return nil, "", err
	}
	var ns model.Namespace
	if e := s.db.Where("id = ?", srv.NamespaceID).First(&ns).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil, "", apperr.ErrAssetNotFound
		}
		return nil, "", e
	}
	return &srv, ns.Code, nil
}

// sensitiveMatcher 读当前敏感规则并编译为匹配器；已落库规则坏（理论上不会，PUT 已校验）时回退内置默认，保证保护不失效。
func (s *AssetPreviewService) sensitiveMatcher() (*sensitiveMatcher, error) {
	patterns, err := s.loadSensitivePatterns()
	if err != nil {
		return nil, err
	}
	m, err := newSensitiveMatcher(patterns)
	if err != nil {
		slog.Warn("敏感路径规则编译失败，回退内置默认", "原因", err)
		return newSensitiveMatcher(defaultSensitivePatterns)
	}
	return m, nil
}

// loadSensitivePatterns 从设置 store 读敏感规则；无存储 → 内置默认种子，损坏 → 回退默认（保护不失效）。
func (s *AssetPreviewService) loadSensitivePatterns() ([]string, error) {
	st, err := s.settingRepo.Get(SettingAssetsSensitivePathPatterns)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return append([]string(nil), defaultSensitivePatterns...), nil
	}
	var patterns []string
	if e := json.Unmarshal([]byte(st.Value), &patterns); e != nil {
		slog.Warn("敏感路径规则存储损坏，回退内置默认", "原因", e)
		return append([]string(nil), defaultSensitivePatterns...), nil
	}
	if patterns == nil {
		patterns = []string{}
	}
	return patterns, nil
}

// recordPreviewAudit 落一条 asset.preview 审计（detail 记 serverId/path/truncated/binary + 敏感放行标记，**绝不含内容**）。
// 审计写失败拒绝返回内容（对齐 payload 查看「先审计后返回」纪律，杜绝「看了没记录」）。
func (s *AssetPreviewService) recordPreviewAudit(p PreviewParams, serverID, nsCode string, c *assetContent, sensitive bool) error {
	detail := map[string]any{"serverId": serverID, "path": p.Path, "truncated": c.truncated, "binary": c.binary}
	if sensitive {
		detail["sensitiveOverride"] = true
		detail["reason"] = p.Reason
	}
	raw, _ := json.Marshal(detail)
	entry := &model.AuditLog{
		NamespaceCode: nsCode, Operator: p.Operator, Action: model.ActionAssetPreview,
		TargetType: model.TargetTypeAsset, TargetRef: serverID,
		Detail: string(raw), Result: model.ResultOK, ClientIP: p.ClientIP,
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Error("文件资产预览审计落库失败，拒绝返回内容", "serverId", serverID, "path", p.Path,
			"operator", p.Operator, "原因", err)
		return err
	}
	slog.Info("文件资产内容预览", "serverId", serverID, "path", p.Path, "operator", p.Operator,
		"binary", c.binary, "sensitiveOverride", sensitive)
	return nil
}

// recordDiffAudit 落一条 asset.diff 审计（detail 记两侧 serverId/path + identical + 敏感放行标记，**绝不含内容**）。
func (s *AssetPreviewService) recordDiffAudit(p DiffParams, leftServerID, rightServerID, nsCode string, identical, sensitive bool) error {
	detail := map[string]any{
		"left":      map[string]string{"serverId": leftServerID, "path": p.Left.Path},
		"right":     map[string]string{"serverId": rightServerID, "path": p.Right.Path},
		"identical": identical,
	}
	if sensitive {
		detail["sensitiveOverride"] = true
		detail["reason"] = p.Reason
	}
	raw, _ := json.Marshal(detail)
	entry := &model.AuditLog{
		NamespaceCode: nsCode, Operator: p.Operator, Action: model.ActionAssetDiff,
		TargetType: model.TargetTypeAsset, TargetRef: leftServerID,
		Detail: string(raw), Result: model.ResultOK, ClientIP: p.ClientIP,
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Error("文件资产 diff 审计落库失败，拒绝返回内容", "left", leftServerID, "right", rightServerID,
			"operator", p.Operator, "原因", err)
		return err
	}
	slog.Info("文件资产内容 diff", "left", leftServerID, "leftPath", p.Left.Path, "right", rightServerID,
		"rightPath", p.Right.Path, "identical", identical, "sensitiveOverride", sensitive, "operator", p.Operator)
	return nil
}

// contentOrNil 二进制回 nil（前端只展示元数据），文本回内容副本。
func contentOrNil(c *assetContent) *string {
	if c.binary {
		return nil
	}
	v := c.content
	return &v
}

// normalizeSensitivePatterns 归一规则：去首尾空白、丢空串（允许空数组=关闭保护）。
func normalizeSensitivePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sensitiveRuleAuditDetail 组装规则修改审计 detail（前后 glob 清单，非凭据，可安全入 detail）。
func sensitiveRuleAuditDetail(before, after []string) string {
	raw, _ := json.Marshal(map[string]any{"before": before, "after": after})
	return string(raw)
}

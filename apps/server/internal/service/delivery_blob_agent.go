package service

import (
	"encoding/json"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// deliveryBlobOrderStatuses 是数据面归属校验认可的变更单状态集（spec §5.3，见 ADR-0069）：
// 活动单（rolling/paused/rolling_back）之外也放行 approved——组单审批通过后、M3 启动前的
// 准备期上传（payload 预热）属合法动作；M2 期尚无活动单，approved 即数据面联调入口。
var deliveryBlobOrderStatuses = []string{
	model.ChangeOrderStatusApproved, model.ChangeOrderStatusRolling,
	model.ChangeOrderStatusPaused, model.ChangeOrderStatusRollingBack,
}

// changeOrderTerminalStatuses 是 blob 清理视角的「已了结」单状态集（spec §4.5.4）：
// completed / cancelled 非严格终态（仍可回滚）但已不再消费 blob；回滚期间（rolling_back）仍在引用。
var changeOrderTerminalStatuses = []string{
	model.ChangeOrderStatusCompleted, model.ChangeOrderStatusCancelled, model.ChangeOrderStatusRolledBack,
}

// AuthorizeBlobUpload 校验流式上传归属（PUT，spec §5.3）：请求身份须是某「引用该 sha 的
// approved / 活动单」的模板源，否则 403。namespace 隔离由查询天然限定（identity 的 namespace）。
func (s *DeliveryBlobService) AuthorizeBlobUpload(id agentauth.Identity, sha string) error {
	sha, err := normalizeBlobSHA(sha)
	if err != nil {
		return err
	}
	orders, err := s.orders.ListOrdersReferencingSHA(id.NamespaceID, sha, deliveryBlobOrderStatuses)
	if err != nil {
		return err
	}
	for i := range orders {
		if orders[i].SourceServerID == id.ServerID {
			return nil
		}
	}
	return apperr.ErrDeliveryBlobForbidden
}

// AuthorizeBlobDownload 校验流式下载归属（GET，spec §5.3）：sha 属于某 approved / 活动单的文件项，
// 且请求身份是该单模板源（M2 简化放行，便于源侧自校验）或在该单目标集内，否则 403。
func (s *DeliveryBlobService) AuthorizeBlobDownload(id agentauth.Identity, sha string) error {
	sha, err := normalizeBlobSHA(sha)
	if err != nil {
		return err
	}
	orders, err := s.orders.ListOrdersReferencingSHA(id.NamespaceID, sha, deliveryBlobOrderStatuses)
	if err != nil {
		return err
	}
	for i := range orders {
		if orders[i].SourceServerID == id.ServerID {
			return nil
		}
		member, e := s.isOrderTarget(&orders[i], id.ServerID)
		if e != nil {
			return e
		}
		if member {
			return nil
		}
	}
	// 配置灰度冻结渲染工件（ADR-0071）：config blob 由控制面渲染、sha 不落 change_order_item，
	// 经工件表 (单, 目标, sha) 反查——命中「活动 / 已审批单内本目标」的工件即放行，严格到该单该目标该 blob。
	authorized, err := s.artifacts.ExistsAuthorizedSHA(id.NamespaceID, id.ServerID, sha, deliveryBlobOrderStatuses)
	if err != nil {
		return err
	}
	if authorized {
		return nil
	}
	return apperr.ErrDeliveryBlobForbidden
}

// AuthorizeBlobHead 校验存在性查询归属（HEAD，spec §5.3）：上传方（去重判断）与下载方（断点判断）
// 都要 HEAD，故放行「可上传或可下载」任一侧。config blob 的 HEAD 走下载侧（经 AuthorizeBlobDownload
// 的工件分支覆盖），无需在此另加分支。
func (s *DeliveryBlobService) AuthorizeBlobHead(id agentauth.Identity, sha string) error {
	if err := s.AuthorizeBlobUpload(id, sha); err == nil {
		return nil
	}
	return s.AuthorizeBlobDownload(id, sha)
}

// isOrderTarget 判定某 serverId 是否在变更单目标集内：change_target 固化快照优先（M3 启动后权威）；
// 尚未固化（approved 未启动）按 selector 即时解析——M2 简化实现，M3 固化落库后启动过的单恒走前者。
func (s *DeliveryBlobService) isOrderTarget(order *model.ChangeOrder, serverID string) (bool, error) {
	ids, err := s.orders.ListTargetServerIDs(order.ID)
	if err != nil {
		return false, err
	}
	if len(ids) > 0 {
		for _, id := range ids {
			if id == serverID {
				return true, nil
			}
		}
		return false, nil
	}
	targets, err := resolveChangeTargets(s.db, order.NamespaceID, decodeSelector(order.Selector), order.SourceServerID)
	if err != nil {
		return false, err
	}
	for i := range targets {
		if targets[i].ServerID == serverID {
			return true, nil
		}
	}
	return false, nil
}

// requireOwnOrder 取变更单并校验与请求身份同 namespace；跨 namespace 一律按不存在处理（不泄露他环境单存在性）。
func (s *DeliveryBlobService) requireOwnOrder(id agentauth.Identity, orderID uint) (*model.ChangeOrder, error) {
	order, err := requireChangeOrder(s.orders, orderID)
	if err != nil {
		return nil, err
	}
	if order.NamespaceID != id.NamespaceID {
		return nil, apperr.ErrChangeOrderNotFound
	}
	return order, nil
}

// DeliveryUploadManifestView 是模板源待上传清单响应（GET .../upload-manifest，spec §5.2；camelCase）。
type DeliveryUploadManifestView struct {
	// 变更单 id
	OrderID uint `json:"orderId"`
	// 待上传项（path/sha256/size；已就绪 blob 不在列）
	Items []DeliveryBlobRequirement `json:"items"`
}

// UploadManifest 模板源拉取待上传 blob 清单（spec §4.5.2 第 1 步的服务器侧）：
// 仅本单模板源可拉（403）；返回 MissingBlobs 对应项，agent 逐项 HEAD 去重后流式 PUT。
func (s *DeliveryBlobService) UploadManifest(id agentauth.Identity, orderID uint) (*DeliveryUploadManifestView, error) {
	order, err := s.requireOwnOrder(id, orderID)
	if err != nil {
		return nil, err
	}
	if order.SourceServerID == "" || order.SourceServerID != id.ServerID {
		return nil, apperr.ErrDeliveryNotSource
	}
	missing, err := s.MissingBlobs(order.ID)
	if err != nil {
		return nil, err
	}
	return &DeliveryUploadManifestView{OrderID: order.ID, Items: missing}, nil
}

// DeliveryManifestFileView 是目标差异清单的文件项（path/action/sha256/size；delete 项无 sha/size）。
type DeliveryManifestFileView struct {
	// 服务器根内相对路径
	Path string `json:"path"`
	// add / update / delete（相对目标语义由 agent 按本地清单重判，spec §4.2.3）
	Action string `json:"action"`
	// 模板源侧内容哈希（delete 项为空）
	SHA256 string `json:"sha256,omitempty"`
	// 字节数（delete 项为 0）
	Size int64 `json:"size,omitempty"`
}

// DeliveryManifestConfigView 目标差异清单的配置项摘要（历史契约字段）。配置灰度落地后 config_change 项
// 已归一为渲染后的文件项进 Files（ADR-0071），本字段恒为空数组、仅为响应契约向后兼容保留（agent 不消费）。
type DeliveryManifestConfigView struct {
	// 作用域层级（五层之一）
	ScopeKind string `json:"scopeKind"`
	// 作用域实体 id
	ScopeID uint `json:"scopeId"`
	// 回滚锚点版本（可空）
	FromVersionID *uint `json:"fromVersionId"`
	// 要发布的目标版本（可空）
	ToVersionID *uint `json:"toVersionId"`
}

// DeliveryTargetManifestView 是目标差异清单响应（GET .../manifest，spec §5.2；camelCase）。
type DeliveryTargetManifestView struct {
	// 变更单 id
	OrderID uint `json:"orderId"`
	// 生效方式（单级配置全批继承，agent 推送阶段无需但可预知）
	ActivationMethod string `json:"activationMethod"`
	// 文件差异项全集（agent 按本地清单重判相对目标语义并对同 hash 文件跳过）
	Files []DeliveryManifestFileView `json:"files"`
	// 配置变更项摘要
	Configs []DeliveryManifestConfigView `json:"configs"`
}

// TargetManifest 目标拉取本服差异清单 + 配置项摘要（spec §4.5.3 的清单侧）：仅本单目标集内身份可拉（403）。
func (s *DeliveryBlobService) TargetManifest(id agentauth.Identity, orderID uint) (*DeliveryTargetManifestView, error) {
	order, err := s.requireOwnOrder(id, orderID)
	if err != nil {
		return nil, err
	}
	member, err := s.isOrderTarget(order, id.ServerID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, apperr.ErrDeliveryNotTarget
	}
	items, err := s.orders.ListItems(order.ID)
	if err != nil {
		return nil, err
	}
	view := &DeliveryTargetManifestView{
		OrderID: order.ID, ActivationMethod: order.ActivationMethod,
		Files: make([]DeliveryManifestFileView, 0, len(items)), Configs: make([]DeliveryManifestConfigView, 0),
	}
	hasConfig := false
	for i := range items {
		switch items[i].Kind {
		case model.ChangeItemKindFileDiff:
			view.Files = append(view.Files, fileManifestView(&items[i]))
		case model.ChangeItemKindConfigChange:
			hasConfig = true
		}
	}
	// config_change 项归一为冻结渲染工件文件项（ADR-0071）：读工件表、不重渲染，消除 head 漂移竞态。
	if err := s.appendConfigArtifacts(view, order.ID, id.ServerID, hasConfig); err != nil {
		return nil, err
	}
	return view, nil
}

// fileManifestView 把一条 file_diff 变更项映射为清单文件项（delete 项 sha/size 为空）。
func fileManifestView(item *model.ChangeOrderItem) DeliveryManifestFileView {
	file := DeliveryManifestFileView{Path: derefString(item.Path), Action: derefString(item.Action)}
	if item.SHA256 != nil {
		file.SHA256 = *item.SHA256
	}
	if item.SizeBytes != nil {
		file.Size = *item.SizeBytes
	}
	return file
}

// appendConfigArtifacts 把本单为该目标冻结的配置渲染工件（ADR-0071）作为文件项补进清单：读工件表、不重渲染
// （渲染已在 payload 准备期由 PrepareConfigBlobs 落工件 + 写 blob）。未装配渲染器（M2 数据面路径）跳过；
// 有 config_change 项却查不到工件（payload 未准备）→ 明确报错，不静默漏发配置。
func (s *DeliveryBlobService) appendConfigArtifacts(view *DeliveryTargetManifestView, orderID uint, serverID string, hasConfig bool) error {
	if s.configRenderer == nil {
		return nil
	}
	arts, err := s.artifacts.ListByOrderServer(orderID, serverID)
	if err != nil {
		return err
	}
	if hasConfig && len(arts) == 0 {
		return apperr.ErrDeliveryConfigArtifactMissing
	}
	for i := range arts {
		view.Files = append(view.Files, DeliveryManifestFileView{
			Path: arts[i].Path, Action: model.ChangeItemActionUpdate, SHA256: arts[i].SHA256, Size: arts[i].SizeBytes,
		})
	}
	return nil
}

// 交付阶段回执 phase 取值（spec §5.2）。
const (
	DeliveryPhaseUpload   = "upload"
	DeliveryPhasePush     = "push"
	DeliveryPhaseActivate = "activate"
	DeliveryPhaseRollback = "rollback"
)

// 交付阶段回执 status 取值（spec §5.2）。
const (
	DeliveryResultSuccess = "success"
	DeliveryResultFailed  = "failed"
)

// deliveryPhaseCommandTypes 是回执 phase → 命令类型映射（命令经既有长轮询通道下发，spec §4.5.1）。
var deliveryPhaseCommandTypes = map[string]string{
	DeliveryPhaseUpload:   model.CommandTypeDeliveryUpload,
	DeliveryPhasePush:     model.CommandTypeDeliveryPush,
	DeliveryPhaseActivate: model.CommandTypeDeliveryActivate,
	DeliveryPhaseRollback: model.CommandTypeDeliveryRollback,
}

// DeliveryResultInput 是阶段回执入参（POST .../result，spec §5.2）。
type DeliveryResultInput struct {
	// 阶段：upload / push / activate / rollback
	Phase string
	// 结果：success / failed
	Status string
	// 实际变更文件数（push 回执）
	ChangedFileCount int
	// 本地同 hash 跳过文件数（push 回执）
	SkippedFileCount int
	// 是否已生成本地备份（push 回执，回滚预检依据）
	BackupPresent bool
	// 失败原因（脱敏后落库展示，ADR-0057）
	Error string
}

// deliveryCommandPayload 是交付命令 payload 的最小解析形状（M2 只消费 orderId 做归属匹配；
// 完整 payload（清单摘要 / activation 参数）由 M3 编排器定义与写入）。
type deliveryCommandPayload struct {
	OrderID uint `json:"orderId"`
}

// ReceiveResult 接收 agent 阶段回执（spec §5.2）：按 phase 校验角色归属 → 定位该单在途命令 →
// CAS fetched→done/failed 落回执摘要。upload 成功另行刷新本单 blob 引用（清理保护）；
// push / activate / rollback 仅落命令账——payload_state 与批次 / 目标状态机推进是 M3/M4 编排器的接缝，
// 编排器将按命令终态与回执字段（changed/skipped/backupPresent/error）推进 change_target。
func (s *DeliveryBlobService) ReceiveResult(id agentauth.Identity, orderID uint, input DeliveryResultInput) error {
	cmdType, ok := deliveryPhaseCommandTypes[input.Phase]
	if !ok || (input.Status != DeliveryResultSuccess && input.Status != DeliveryResultFailed) {
		return apperr.ErrInvalidParam
	}
	order, err := s.requireOwnOrder(id, orderID)
	if err != nil {
		return err
	}
	if err := s.authorizeResultRole(order, id, input.Phase); err != nil {
		return err
	}
	cmd, err := s.findDeliveryCommand(id, order.ID, cmdType)
	if err != nil {
		return err
	}
	next := model.CommandStatusDone
	if input.Status == DeliveryResultFailed {
		next = model.CommandStatusFailed
	}
	if ok, err := s.cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, next,
		deliveryResultDetail(order.ID, input)); err != nil || !ok {
		return errOrCommand(err)
	}
	if input.Phase == DeliveryPhaseUpload && input.Status == DeliveryResultSuccess {
		if e := s.TouchReferences(order.ID); e != nil { // 上传落定：刷新引用保护新 blob 不被保留期清理误删
			return e
		}
	}
	// 命令落定后即时唤醒推进器：由其单一驱动源读命令终态推进 payload 准备 / change_target 状态机（M3）。
	if s.waker != nil {
		s.waker.WakeOrder(order.ID)
	}
	return nil
}

// authorizeResultRole 校验回执角色：upload 只认模板源，push / activate / rollback 只认目标集内身份。
func (s *DeliveryBlobService) authorizeResultRole(order *model.ChangeOrder, id agentauth.Identity, phase string) error {
	if phase == DeliveryPhaseUpload {
		if order.SourceServerID != id.ServerID {
			return apperr.ErrDeliveryNotSource
		}
		return nil
	}
	member, err := s.isOrderTarget(order, id.ServerID)
	if err != nil {
		return err
	}
	if !member {
		return apperr.ErrDeliveryNotTarget
	}
	return nil
}

// findDeliveryCommand 定位该身份 + 该单 + 该类型的在途（fetched）命令：payload 内 orderId 应用层匹配。
// 无在途命令（未下发 / 已回执 / 已过期）返回 COMMAND_NOT_FOUND——回执必须挂在真实命令生命周期上。
func (s *DeliveryBlobService) findDeliveryCommand(id agentauth.Identity, orderID uint, cmdType string) (*model.AgentCommand, error) {
	cmds, err := s.cmdRepo.ListFetchedByType(id.Namespace, id.ServerID, cmdType)
	if err != nil {
		return nil, err
	}
	for i := range cmds {
		var payload deliveryCommandPayload
		if json.Unmarshal([]byte(cmds[i].Payload), &payload) == nil && payload.OrderID == orderID {
			return &cmds[i], nil
		}
	}
	return nil, apperr.ErrCommandNotFound
}

// deliveryResultDetail 组装命令 result_detail 摘要 JSON（仅计数与脱敏原因，绝不含文件内容，spec §4.5.1）。
func deliveryResultDetail(orderID uint, input DeliveryResultInput) string {
	detail := map[string]any{
		"orderId": orderID, "phase": input.Phase, "status": input.Status,
		"changedFileCount": input.ChangedFileCount, "skippedFileCount": input.SkippedFileCount,
		"backupPresent": input.BackupPresent,
	}
	if input.Status == DeliveryResultFailed {
		detail["error"] = sanitizeFileSyncError(input.Error)
	}
	raw, _ := json.Marshal(detail)
	return string(raw)
}

// deliveryBlobSweepRef 是清理审计的 TargetRef 固定值（清理是全局 sweep，无单一对象定位）。
const deliveryBlobSweepRef = "sweep"

package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// RegisterAssetPreviewApprovalAdapter 注册文件内容读取的审批适配器。
func RegisterAssetPreviewApprovalAdapter(registry *authz.ApprovalRegistry, previews *AssetPreviewService, grants *SensitiveAccessGrantService) {
	if registry == nil || previews == nil || grants == nil {
		return
	}
	registry.Register(authz.OperationAgentCommandFSBrowse, authz.RequireExecutionReceipt(assetPreviewApprovalAdapter{previews: previews, grants: grants}))
}

type assetPreviewApprovalAdapter struct {
	previews *AssetPreviewService
	grants   *SensitiveAccessGrantService
}

type assetPreviewApprovalPayload struct {
	Namespace string                    `json:"namespace"`
	ServerID  string                    `json:"serverId"`
	Path      string                    `json:"path"`
	SHA256    string                    `json:"sha256"`
	PairID    string                    `json:"pairId,omitempty"`
	PairSide  string                    `json:"pairSide,omitempty"`
	Peer      *assetPreviewApprovalPeer `json:"peer,omitempty"`
}

type assetPreviewApprovalPeer struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
}

func (assetPreviewApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

// RequestAccess 冻结单个文件的权威目标与清单哈希，正文绝不进入审批载荷。
func (s *AssetPreviewService) RequestAccess(serverID, path, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	if s == nil || s.approval == nil {
		// 装配缺失是服务端问题，不把故障甩给调用方（此前与参数错误混用同一 400）。
		return model.ApprovalRequest{}, apperr.ErrInternal
	}
	if strings.TrimSpace(serverID) == "" || strings.TrimSpace(path) == "" ||
		strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	srv, namespace, err := s.resolveServer(serverID)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	asset, err := s.assetRepo.FindByServerPath(srv.ID, path)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if asset == nil {
		return model.ApprovalRequest{}, apperr.ErrAssetNotFound
	}
	return s.approval.Request(authz.Operation{
		Kind: authz.OperationAgentCommandFSBrowse, NamespaceID: &srv.NamespaceID,
		Resource: "file_asset", ResourceID: stableAssetApprovalRef(namespace, serverID, path),
		IdempotencyKey: idempotencyKey, Reason: reason,
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{
			{Label: "服务器", Value: serverID}, {Label: "内容版本", Value: asset.SHA256},
		},
	}, map[string]any{"namespace": namespace, "serverId": serverID, "path": path, "sha256": asset.SHA256}, principal, clientIP)
}

// RequestPairAccess 为跨服务器双侧文件读取分别创建审批申请；正文不会写入任一审批载荷。
func (s *AssetPreviewService) RequestPairAccess(left, right AssetRef, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, model.ApprovalRequest, error) {
	if s == nil || s.approval == nil {
		// 装配缺失是服务端问题，不把故障甩给调用方（此前与参数错误混用同一 400）。
		return model.ApprovalRequest{}, model.ApprovalRequest{}, apperr.ErrInternal
	}
	if left.ServerID == "" || left.Path == "" || right.ServerID == "" || right.Path == "" ||
		left.ServerID == right.ServerID || strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	leftServer, leftNamespace, err := s.resolveServer(left.ServerID)
	if err != nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, err
	}
	rightServer, rightNamespace, err := s.resolveServer(right.ServerID)
	if err != nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, err
	}
	leftAsset, err := s.assetRepo.FindByServerPath(leftServer.ID, left.Path)
	if err != nil || leftAsset == nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, assetApprovalAssetError(err, leftAsset)
	}
	rightAsset, err := s.assetRepo.FindByServerPath(rightServer.ID, right.Path)
	if err != nil || rightAsset == nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, assetApprovalAssetError(err, rightAsset)
	}
	pairID := stableAssetPairID(idempotencyKey, leftNamespace, left, leftAsset.SHA256, rightNamespace, right, rightAsset.SHA256)
	leftPayload := assetPreviewApprovalPayload{Namespace: leftNamespace, ServerID: left.ServerID, Path: left.Path, SHA256: leftAsset.SHA256, PairID: pairID, PairSide: "left",
		Peer: &assetPreviewApprovalPeer{Namespace: rightNamespace, ServerID: right.ServerID, Path: right.Path, SHA256: rightAsset.SHA256}}
	rightPayload := assetPreviewApprovalPayload{Namespace: rightNamespace, ServerID: right.ServerID, Path: right.Path, SHA256: rightAsset.SHA256, PairID: pairID, PairSide: "right",
		Peer: &assetPreviewApprovalPeer{Namespace: leftNamespace, ServerID: left.ServerID, Path: left.Path, SHA256: leftAsset.SHA256}}
	leftRequest, err := s.requestPairSide(leftServer, leftPayload, reason, idempotencyKey+":left", principal, clientIP)
	if err != nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, err
	}
	rightRequest, err := s.requestPairSide(rightServer, rightPayload, reason, idempotencyKey+":right", principal, clientIP)
	if err != nil {
		return model.ApprovalRequest{}, model.ApprovalRequest{}, err
	}
	return leftRequest, rightRequest, nil
}

func (s *AssetPreviewService) requestPairSide(server *model.Server, payload assetPreviewApprovalPayload, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	return s.approval.Request(authz.Operation{
		Kind: authz.OperationAgentCommandFSBrowse, NamespaceID: &server.NamespaceID,
		Resource: "file_asset_pair_read", ResourceID: stableAssetApprovalRef(payload.Namespace, payload.ServerID, payload.Path),
		IdempotencyKey: idempotencyKey, Reason: reason,
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{
			{Label: "本侧服务器", Value: payload.ServerID}, {Label: "本侧内容版本", Value: payload.SHA256},
			{Label: "对侧服务器", Value: payload.Peer.ServerID}, {Label: "对侧内容版本", Value: payload.Peer.SHA256},
		},
	}, map[string]any{
		"namespace": payload.Namespace, "serverId": payload.ServerID, "path": payload.Path, "sha256": payload.SHA256,
		"pairId": payload.PairID, "pairSide": payload.PairSide,
		"peer": map[string]string{"namespace": payload.Peer.Namespace, "serverId": payload.Peer.ServerID, "path": payload.Peer.Path, "sha256": payload.Peer.SHA256},
	}, principal, clientIP)
}

func (a assetPreviewApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if permit.Operation() != authz.OperationAgentCommandFSBrowse {
		return nil, apperr.ErrForbidden
	}
	payload, err := parseAssetPreviewApproval(req.Payload)
	if err != nil {
		return nil, err
	}
	srv, namespace, err := resolveAssetPreviewServer(tx, payload.ServerID)
	if err != nil || namespace != payload.Namespace || srv.NamespaceID != dereferenceNamespaceID(req.Operation.NamespaceID) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	asset, err := a.previews.assetRepo.WithTx(tx).FindByServerPath(srv.ID, payload.Path)
	if err != nil || asset == nil || asset.SHA256 != payload.SHA256 {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if _, err := a.previews.instances.Get(payload.Namespace, payload.ServerID); err != nil {
		return nil, apperr.ErrAssetAgentOffline
	}
	if err := a.validatePairPeer(tx, payload); err != nil {
		return nil, err
	}
	cmd, err := a.previews.createAssetReadCommandInTx(tx, payload.Namespace, payload.ServerID, payload.Path, req.RequesterID)
	if err != nil {
		return nil, err
	}
	target, err := authz.NewSensitiveAccessTarget("agent-command", fmt.Sprint(cmd.ID), payload.SHA256)
	if err != nil {
		return nil, err
	}
	grantService := a.grants.WithTx(tx)
	var grant *model.SensitiveAccessGrant
	if payload.PairID != "" {
		grant, err = grantService.CreatePendingPair(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, target.Ref, target.ContentHash, payload.PairID, payload.PairSide)
	} else {
		grant, err = grantService.CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, target.Ref, target.ContentHash)
	}
	if err != nil {
		return nil, err
	}
	resultRef := fmt.Sprintf("agent-sensitive-operation:command:%d:grant:%s", cmd.ID, grant.GrantID)
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: resultRef}).Error; err != nil {
		return nil, err
	}
	// 命令对长轮询 Agent 一提交即可见，故在提交前登记中继槽，避免极速回传丢失正文。
	a.previews.relay.expect(cmd.ID)
	return func() {
		if a.previews.notifier != nil {
			a.previews.notifier.NotifyCommand(payload.Namespace, payload.ServerID)
		}
	}, nil
}

func (a assetPreviewApprovalAdapter) validatePairPeer(tx *gorm.DB, payload assetPreviewApprovalPayload) error {
	if payload.PairID == "" {
		if payload.PairSide != "" || payload.Peer != nil {
			return apperr.ErrInvalidParam
		}
		return nil
	}
	if (payload.PairSide != "left" && payload.PairSide != "right") || payload.Peer == nil || payload.Peer.Namespace == "" || payload.Peer.ServerID == "" || payload.Peer.Path == "" || payload.Peer.SHA256 == "" || payload.Peer.ServerID == payload.ServerID {
		return apperr.ErrInvalidParam
	}
	peerServer, peerNamespace, err := resolveAssetPreviewServer(tx, payload.Peer.ServerID)
	if err != nil || peerNamespace != payload.Peer.Namespace {
		return apperr.ErrApprovalTargetChanged
	}
	peerAsset, err := a.previews.assetRepo.WithTx(tx).FindByServerPath(peerServer.ID, payload.Peer.Path)
	if err != nil || peerAsset == nil || peerAsset.SHA256 != payload.Peer.SHA256 {
		return apperr.ErrApprovalTargetChanged
	}
	return nil
}

// ConsumeApproved 在原申请主体的单次授权消费中返回已经由 Agent 回传的文件内容。
func (s *AssetPreviewService) ConsumeApproved(grantID string, commandID uint, principal auth.Principal) (*AssetPreviewResult, error) {
	if s == nil || s.grants == nil || commandID == 0 {
		return nil, apperr.ErrForbidden
	}
	grant, err := s.grants.repo.FindByID(grantID)
	if err != nil || grant == nil || grant.PairID != "" {
		return nil, apperr.ErrForbidden
	}
	content, ready := s.relay.take(commandID)
	if !ready {
		return nil, apperr.ErrForbidden
	}
	cmd, err := s.cmdRepo.FindByID(commandID)
	if err != nil || cmd == nil || cmd.Type != model.CommandTypeAssetRead || cmd.Status != model.CommandStatusDone {
		return nil, apperr.ErrForbidden
	}
	var read assetReadPayload
	if json.Unmarshal([]byte(cmd.Payload), &read) != nil || read.Path == "" {
		return nil, apperr.ErrForbidden
	}
	srv, _, err := s.resolveServer(cmd.ServerID)
	if err != nil {
		return nil, err
	}
	asset, err := s.assetRepo.FindByServerPath(srv.ID, read.Path)
	if err != nil || asset == nil {
		return nil, apperr.ErrAssetNotFound
	}
	target, err := authz.NewSensitiveAccessTarget("agent-command", fmt.Sprint(commandID), asset.SHA256)
	if err != nil {
		return nil, err
	}
	if err := s.grants.Consume(grantID, principal, authz.OperationAgentCommandFSBrowse, target.Ref, target.ContentHash, time.Now().UTC()); err != nil {
		return nil, err
	}
	s.relay.forget(commandID)
	return &AssetPreviewResult{Content: contentOrNil(content), Truncated: content.truncated, Binary: content.binary, SHA256: asset.SHA256, Size: asset.Size}, nil
}

// ConsumePairApproved 原子消费双侧授权，只返回不含正文的差异摘要。
func (s *AssetPreviewService) ConsumePairApproved(grantID string, commandID uint, principal auth.Principal) (*AssetPairDiffResult, error) {
	// 逐条区分：装配缺失是服务端问题、缺参数是调用方问题、授权查不到是凭据问题——
	// 此前统一 403 会把排查方向全部指向权限。
	if s == nil || s.grants == nil {
		return nil, apperr.ErrInternal
	}
	if commandID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	// 「授权不存在」与「已过期」同回 HTTP 410（语义都是「该凭据不可用」），响应体 code 仍区分
	// 处置方向（口径见 ErrSensitiveAccessNotFound 注释）。
	requested, err := s.grants.repo.FindByID(grantID)
	if err != nil {
		return nil, err
	}
	if requested == nil || requested.PairID == "" {
		return nil, apperr.ErrSensitiveAccessNotFound
	}
	// FindPairByGrantID 的 SQL 已按「pair_id = 本 grant 的 pair_id」过滤，返回的两条必然与
	// requested 同组，故无需再比对 PairID（比对条件恒为假）；组不完整 / 同侧重复由仓库层
	// 失败关闭（ErrForbidden）拦截。
	leftGrant, rightGrant, err := s.grants.repo.FindPairByGrantID(grantID)
	if err != nil {
		return nil, err
	}
	leftCommandID, rightCommandID, err := pairCommandIDs(leftGrant, rightGrant, grantID, commandID)
	if err != nil {
		return nil, err
	}
	left, err := s.pairReadSide(leftGrant, leftCommandID)
	if err != nil {
		s.invalidatePairOnDrift(leftGrant.PairID, err)
		return nil, err
	}
	right, err := s.pairReadSide(rightGrant, rightCommandID)
	if err != nil {
		s.invalidatePairOnDrift(rightGrant.PairID, err)
		return nil, err
	}
	leftContent, leftReady := s.relay.take(leftCommandID)
	rightContent, rightReady := s.relay.take(rightCommandID)
	if !leftReady || !rightReady {
		// 正文尚未由 Agent 回传：属时序问题，报「尚未就绪」而非越权。
		return nil, apperr.ErrSensitiveAccessNotConsumed
	}
	if _, _, err := s.grants.ConsumePair(grantID, principal, time.Now().UTC()); err != nil {
		return nil, err
	}
	defer s.relay.forget(leftCommandID)
	defer s.relay.forget(rightCommandID)
	unsupported := leftContent.binary || rightContent.binary || leftContent.truncated || rightContent.truncated
	identical := !unsupported && leftContent.content == rightContent.content
	params := DiffParams{Left: left.ref, Right: right.ref, Operator: auth.NormalizePrincipal(principal).StableID()}
	if err := s.recordDiffAudit(params, left.server.ServerID, right.server.ServerID, left.namespace, identical, false); err != nil {
		return nil, err
	}
	return &AssetPairDiffResult{Identical: identical, Changed: !identical && !unsupported, Unsupported: unsupported,
		Left:  AssetPairDiffSide{ServerID: left.ref.ServerID, Path: left.ref.Path, SHA256: left.asset.SHA256, Size: left.asset.Size},
		Right: AssetPairDiffSide{ServerID: right.ref.ServerID, Path: right.ref.Path, SHA256: right.asset.SHA256, Size: right.asset.Size}}, nil
}

type pairReadSide struct {
	server    *model.Server
	namespace string
	ref       AssetRef
	asset     *model.FileAsset
}

func (s *AssetPreviewService) pairReadSide(grant *model.SensitiveAccessGrant, commandID uint) (pairReadSide, error) {
	cmd, err := s.cmdRepo.FindByID(commandID)
	if err != nil || cmd == nil || cmd.Type != model.CommandTypeAssetRead || cmd.Status != model.CommandStatusDone || grant.TargetRef != fmt.Sprintf("agent-command/%d", commandID) || grant.Operation != authz.OperationAgentCommandFSBrowse {
		return pairReadSide{}, apperr.ErrForbidden
	}
	var read assetReadPayload
	if json.Unmarshal([]byte(cmd.Payload), &read) != nil || read.Path == "" {
		return pairReadSide{}, apperr.ErrForbidden
	}
	server, namespace, err := s.resolveServer(cmd.ServerID)
	if err != nil {
		return pairReadSide{}, err
	}
	asset, err := s.assetRepo.FindByServerPath(server.ID, read.Path)
	if err != nil || asset == nil || asset.SHA256 != grant.ContentVersionHash {
		return pairReadSide{}, apperr.ErrApprovalTargetChanged
	}
	return pairReadSide{server: server, namespace: namespace, ref: AssetRef{ServerID: cmd.ServerID, Path: read.Path}, asset: asset}, nil
}

func pairCommandIDs(left, right *model.SensitiveAccessGrant, requestedGrantID string, requestedCommandID uint) (uint, uint, error) {
	leftID, err := commandIDFromTargetRef(left.TargetRef)
	if err != nil {
		return 0, 0, err
	}
	rightID, err := commandIDFromTargetRef(right.TargetRef)
	if err != nil || leftID == rightID {
		return 0, 0, apperr.ErrForbidden
	}
	if (left.GrantID == requestedGrantID && leftID != requestedCommandID) || (right.GrantID == requestedGrantID && rightID != requestedCommandID) {
		return 0, 0, apperr.ErrForbidden
	}
	return leftID, rightID, nil
}

func commandIDFromTargetRef(ref string) (uint, error) {
	var commandID uint
	if _, err := fmt.Sscanf(ref, "agent-command/%d", &commandID); err != nil || commandID == 0 {
		return 0, apperr.ErrForbidden
	}
	return commandID, nil
}

func (s *AssetPreviewService) invalidatePairOnDrift(pairID string, err error) {
	if err == apperr.ErrApprovalTargetChanged {
		_ = s.grants.RevokePair(pairID)
	}
}

func parseAssetPreviewApproval(raw []byte) (assetPreviewApprovalPayload, error) {
	var payload assetPreviewApprovalPayload
	if json.Unmarshal(raw, &payload) != nil || payload.Namespace == "" || payload.ServerID == "" || payload.Path == "" || payload.SHA256 == "" {
		return assetPreviewApprovalPayload{}, apperr.ErrInvalidParam
	}
	return payload, nil
}

func stableAssetPairID(idempotencyKey, leftNamespace string, left AssetRef, leftHash, rightNamespace string, right AssetRef, rightHash string) string {
	value := strings.Join([]string{idempotencyKey, leftNamespace, left.ServerID, left.Path, leftHash, rightNamespace, right.ServerID, right.Path, rightHash}, "\x00")
	return "asset-pair-" + fmt.Sprintf("%x", sha256Bytes(value))
}

func assetApprovalAssetError(err error, asset *model.FileAsset) error {
	if err != nil {
		return err
	}
	if asset == nil {
		return apperr.ErrAssetNotFound
	}
	return nil
}

func stableAssetApprovalRef(namespace, serverID, path string) string {
	return namespace + "/" + serverID + "/" + fmt.Sprintf("%x", sha256Bytes(path))
}

func sha256Bytes(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func dereferenceNamespaceID(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}

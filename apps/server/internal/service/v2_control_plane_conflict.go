package service

import (
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/bootwatch"
)

// AlertSink 是并发身份冲突告警留痕的窄写依赖（由 AlertEventService 实现，FR-177）。
// 在 service 本地声明、依赖倒置，避免对告警持久化实现的强耦合，便于测试替身。
type AlertSink interface {
	// Record 落库一条告警事件；失败仅由调用方记 WARN、不阻断主流程。
	Record(*model.AlertEvent) error
}

// ConflictPeerView 是并发身份冲突双方 boot 明细（spec §5.2）：既作 conflict_peers TEXT 列的序列化结构，
// 也作详情端点 conflictPeers 的对外结构，字段与 devmock/契约 ConflictPeer 对齐。
type ConflictPeerView struct {
	BootID     string    `json:"bootId"`
	LastAddr   string    `json:"lastAddr"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// SetConflictWatch 装配并发身份冲突检测依赖（FR-177）：
// reg 为 bootId 活跃注册表（进程内真源）；window 返回当前冲突窗口（从设置 store 读、热生效）；alerts 为告警留痕出口。
// 任一为 nil 时对应能力降级（reg=nil 全禁检测；alerts=nil 只审计不告警），不 panic。
func (s *V2ControlPlaneService) SetConflictWatch(reg *bootwatch.Registry, window func() time.Duration, alerts AlertSink) {
	s.bootRegistry = reg
	s.conflictWindow = window
	s.alertSink = alerts
}

// conflictWindowDur 取当前冲突检测窗口；未装配取值函数或返回非正值时回退默认（10 分钟）。
func (s *V2ControlPlaneService) conflictWindowDur() time.Duration {
	if s.conflictWindow != nil {
		if d := s.conflictWindow(); d > 0 {
			return d
		}
	}
	return time.Duration(identityDefaultConflictWindowSec) * time.Second
}

// detectRegisterConflict 注册前做 bootId 往复观测（FR-177，spec §4.5）：
// handled=true 表示本次注册被冲突短路（落败方重抢或往复已转 conflict），调用方直接返回附带的 409；
// handled=false 表示无冲突短路（含身份仍在 pending 阶段的往复），调用方继续常规注册。
// 注册表观测在本方法内完成并释放锁；转冲突的落库（markIdentityConflict）在锁外，守「runtime 锁内不做 DB IO」。
func (s *V2ControlPlaneService) detectRegisterConflict(p AgentRegisterV2Params, now time.Time) (bool, error) {
	if s.bootRegistry == nil {
		return false, nil
	}
	obs := s.bootRegistry.OnRegister(p.IdentityID, p.BootID, p.Addr, now, s.conflictWindowDur())
	if obs.Evicted {
		// resolve 落败方以同一 boot 重新抢占 → 持续拒绝并给指引。
		return true, apperr.ErrIdentityConflictLoser
	}
	if !obs.ConflictDetected {
		return false, nil
	}
	marked, err := s.markIdentityConflict(p.IdentityID, obs.Peers, now)
	if err != nil {
		return true, err
	}
	if marked {
		return true, apperr.ErrIdentityConflict
	}
	// 未标记（身份非 active/disabled）→ 落常规注册路径。
	return false, nil
}

// markIdentityConflict 落实 T12（spec §4.3）：身份为 active/disabled 时转 conflict，写 conflict_reason/conflict_peers +
// 审计(system)；事务提交后记告警。返回是否已标记（false 表示身份不在可转冲突态，调用方继续常规注册）。
// 事务内不碰注册表锁（调用前已在锁外完成往复观测），守「runtime 锁内不做 DB IO」。
func (s *V2ControlPlaneService) markIdentityConflict(identityID string, peers []bootwatch.Peer, now time.Time) (bool, error) {
	peersJSON := marshalConflictPeers(peers)
	var (
		marked   bool
		nsCode   string
		serverID string
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ident, err := findIdentityByID(tx, identityID)
		if err != nil {
			return err
		}
		if ident == nil {
			return apperr.ErrInstanceNotFound
		}
		// 仅已绑定态（active/disabled）转冲突；pending 等阶段的重复注册按常规刷新处理（spec §4.4 Q4 针对已绑定并发双实例）。
		if ident.Status != model.AgentIdentityStatusActive && ident.Status != model.AgentIdentityStatusDisabled {
			return nil
		}
		ns, err := findNamespaceByID(tx, ident.NamespaceID)
		if err != nil {
			return err
		}
		ident.Status = model.AgentIdentityStatusConflict
		ident.ConflictReason = "duplicate-boot-id"
		ident.ConflictPeers = peersJSON
		ident.PendingExpiresAt = nil
		ident.StatusChangedAt = now
		if err := tx.Save(ident).Error; err != nil {
			return err
		}
		marked, nsCode, serverID = true, ns.Code, ident.ServerID
		return auditIdentity(tx, ns, ident, model.ActionIdentityConflict, "system", model.ResultOK, "")
	})
	if err != nil {
		return false, err
	}
	if marked {
		s.raiseConflictAlert(nsCode, serverID, identityID, peers)
	}
	return marked, nil
}

// raiseConflictAlert 记一条并发身份冲突告警（FR-177，spec §4.3 T12「触发告警」）：
// 复用告警事件留痕通道，落库后即在管理台「事件」页可见；detail 仅记 identityId + 双方 boot 明细，不含任何凭据。
func (s *V2ControlPlaneService) raiseConflictAlert(namespace, serverID, identityID string, peers []bootwatch.Peer) {
	if s.alertSink == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"identityId": identityID, "peers": toConflictPeerViews(peers)})
	if err := s.alertSink.Record(&model.AlertEvent{
		Type:      model.AlertEventTypeIdentityConflict,
		Level:     model.AlertLevelCritical,
		ServerID:  serverID,
		Namespace: namespace,
		Message:   "并发身份冲突：" + serverID + " 检出同 identityId 交替 bootId（" + identityID + "），双实例已冻结待处置",
		Detail:    string(detail),
	}); err != nil {
		slog.Warn("身份冲突告警留痕失败", "identityId", identityID, "serverId", serverID, "错误", err)
	}
}

// ResolveConflictParams 是冲突处置（保留指定实例）入参（FR-177，spec §5.2）。
type ResolveConflictParams struct {
	KeepBootID string
	Reason     string
	Operator   string
	ClientIP   string
}

// ResolveAgentIdentityConflict 落实 T13（spec §4.3）：以 keepBootId 为准恢复 active，清冲突态 + 审计；
// 处置后落败方后续请求持续 409（由注册表 evicted 识别）。非 conflict → 409；keepBootId 不在冲突双方 → 400。
func (s *V2ControlPlaneService) ResolveAgentIdentityConflict(identityID string, p ResolveConflictParams) (*model.AgentIdentity, error) {
	if p.Reason == "" || p.KeepBootID == "" {
		return nil, apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	var out model.AgentIdentity
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ident, err := findIdentityByID(tx, identityID)
		if err != nil {
			return err
		}
		if ident == nil {
			return apperr.ErrInstanceNotFound
		}
		if ident.Status != model.AgentIdentityStatusConflict {
			return apperr.ErrIllegalState
		}
		if !conflictPeersContain(ident.ConflictPeers, p.KeepBootID) {
			return apperr.ErrConflictKeepBootInvalid
		}
		ns, err := findNamespaceByID(tx, ident.NamespaceID)
		if err != nil {
			return err
		}
		ident.Status = model.AgentIdentityStatusActive
		ident.BootID = p.KeepBootID
		ident.ConflictReason = ""
		ident.ConflictPeers = ""
		ident.StatusChangedAt = now
		if err := tx.Save(ident).Error; err != nil {
			return err
		}
		out = *ident
		return createAudit(tx, model.AuditLog{
			NamespaceCode: ns.Code, Operator: operatorOrSystem(p.Operator),
			Action: model.ActionIdentityConflictResolve, TargetType: model.TargetTypeIdentity,
			TargetRef: ident.IdentityID, Detail: conflictResolveAuditDetail(p.KeepBootID, p.Reason),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	// 提交后更新注册表：以保留方为 current，其余窗口内活跃 boot 记为落败（后续持续 409）。
	if s.bootRegistry != nil {
		s.bootRegistry.Resolve(identityID, p.KeepBootID, now, s.conflictWindowDur())
	}
	return &out, nil
}

// toConflictPeerViews 把注册表 boot 快照映射为对外/持久化视图。
func toConflictPeerViews(peers []bootwatch.Peer) []ConflictPeerView {
	views := make([]ConflictPeerView, 0, len(peers))
	for _, p := range peers {
		views = append(views, ConflictPeerView{BootID: p.BootID, LastAddr: p.LastAddr, LastSeenAt: p.LastSeen})
	}
	return views
}

// marshalConflictPeers 序列化冲突双方明细为 JSON 文本（落 conflict_peers TEXT 列）。
func marshalConflictPeers(peers []bootwatch.Peer) string {
	raw, _ := json.Marshal(toConflictPeerViews(peers))
	return string(raw)
}

// ParseConflictPeers 解析 conflict_peers TEXT 列为视图切片（供详情端点回显 conflictPeers）；空 / 非法返回 nil。
func ParseConflictPeers(raw string) []ConflictPeerView {
	if raw == "" {
		return nil
	}
	var views []ConflictPeerView
	if err := json.Unmarshal([]byte(raw), &views); err != nil {
		return nil
	}
	return views
}

// conflictPeersContain 判断 keepBootId 是否在持久化的冲突双方内（spec §5.2 的 400 判据）。
func conflictPeersContain(raw, bootID string) bool {
	for _, v := range ParseConflictPeers(raw) {
		if v.BootID == bootID {
			return true
		}
	}
	return false
}

// conflictResolveAuditDetail 组装冲突处置审计 detail（json 文本）：保留的 bootId + 处置原因（无凭据）。
func conflictResolveAuditDetail(keepBootID, reason string) string {
	raw, _ := json.Marshal(map[string]string{"keepBootId": keepBootID, "reason": reason})
	return string(raw)
}

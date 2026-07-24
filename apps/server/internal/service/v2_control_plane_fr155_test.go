package service

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// fr155Fixture 承载 FR-155 用例共享脚手架：一个 namespace + 一条 BC 集群 / 大区 / 两个小区。
type fr155Fixture struct {
	db      *gorm.DB
	svc     *V2ControlPlaneService
	ns      *model.Namespace
	token   string
	cluster *model.BCCluster
	region  *model.Region
	zoneA   *model.Zone
	zoneB   *model.Zone
}

func setupFR155Cluster(t *testing.T) fr155Fixture {
	t.Helper()
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc-a", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r1", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zoneA, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-a", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区 A 失败: %v", err)
	}
	zoneB, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-b", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区 B 失败: %v", err)
	}
	return fr155Fixture{db: db, svc: svc, ns: ns, token: token, cluster: cluster, region: region, zoneA: zoneA, zoneB: zoneB}
}

// approveServer 注册并确认一台 server（默认未分配），返回其 server 行 id。
func (f fr155Fixture) approveServer(t *testing.T, identityID, serverID, kind string) uint {
	t.Helper()
	if _, err := f.svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: f.token, IdentityID: identityID, ServerID: serverID, Kind: kind, BootID: "boot-" + serverID,
	}); err != nil {
		t.Fatalf("注册 %s 失败: %v", serverID, err)
	}
	if _, err := f.svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认 %s 失败: %v", serverID, err)
	}
	var server model.Server
	if err := f.db.Where("namespace_id = ? AND server_id = ?", f.ns.ID, serverID).First(&server).Error; err != nil {
		t.Fatalf("取 %s server 行失败: %v", serverID, err)
	}
	return server.ID
}

func (f fr155Fixture) reloadServer(t *testing.T, id uint) model.Server {
	t.Helper()
	var server model.Server
	if err := f.db.First(&server, id).Error; err != nil {
		t.Fatalf("重载 server %d 失败: %v", id, err)
	}
	return server
}

func (f fr155Fixture) auditCount(t *testing.T, action string) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("统计审计 %s 失败: %v", action, err)
	}
	return count
}

const (
	fr155IdentityA = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	fr155IdentityB = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	fr155IdentityP = "cccccccc-3333-4333-8333-cccccccccccc"
	fr155IdentityC = "dddddddd-4444-4444-8444-dddddddddddd"
)

func (f fr155Fixture) assign(t *testing.T, rowID uint, kind string, targetID uint, defaultEntry bool) {
	t.Helper()
	if _, err := f.svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{rowID}, TargetKind: kind, TargetID: targetID,
		IsDefaultEntry: defaultEntry, Reason: "首次分配", Operator: "admin",
	}); err != nil {
		t.Fatalf("首次分配 server %d 失败: %v", rowID, err)
	}
}

// TestV2RezoneServersInitAndReapprove 换区工单发起 + 重确认落区闭环。
func TestV2RezoneServersInitAndReapprove(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, true)

	results, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{rowID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID,
		Reason: "扩容换区", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("发起换区应成功: %v", err)
	}
	if len(results) != 1 || !results[0].Ok {
		t.Fatalf("换区结果应逐台 ok，实际 %+v", results)
	}
	server := f.reloadServer(t, rowID)
	if server.ZoneID != nil || server.BCClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("换区发起后应解绑清归属 + 清默认入口，实际 %+v", server)
	}
	if server.PendingZoneID == nil || *server.PendingZoneID != f.zoneB.ID {
		t.Fatalf("换区发起后应写预填目标 zoneB，实际 %+v", server.PendingZoneID)
	}
	var ident model.AgentIdentity
	if err := f.db.Where("identity_id = ?", fr155IdentityA).First(&ident).Error; err != nil {
		t.Fatalf("取身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusPending {
		t.Fatalf("换区发起后身份应重入 pending，实际 %s", ident.Status)
	}
	if f.auditCount(t, model.ActionServerRezoneInit) != 1 {
		t.Fatalf("应记 1 条 zone.rezone.initiated 审计")
	}

	// 重确认：缺省取预填目标落区
	if _, err := f.svc.ApproveAgentIdentity(fr155IdentityA, ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("换区重确认应成功: %v", err)
	}
	server = f.reloadServer(t, rowID)
	if server.ZoneID == nil || *server.ZoneID != f.zoneB.ID || server.PendingZoneID != nil {
		t.Fatalf("重确认后应落区 zoneB 且清 pending，实际 %+v", server)
	}
	if f.auditCount(t, model.ActionServerRezoneDone) != 1 {
		t.Fatalf("应记 1 条 zone.rezone.completed 审计")
	}
}

// TestV2RezoneRejectsUnassignedServer 换区工单选中未分配 server → 400 not_assigned。
func TestV2RezoneRejectsUnassignedServer(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	_, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{rowID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID,
		Reason: "换区", Operator: "admin",
	})
	if !errors.Is(err, apperr.ErrRezoneNotAssigned) {
		t.Fatalf("未分配 server 换区应 not_assigned，实际 %v", err)
	}
}

// TestV2RezoneAtomicRollback 批内含未分配 server → 整批回滚，已分配 server 不被改动。
func TestV2RezoneAtomicRollback(t *testing.T) {
	f := setupFR155Cluster(t)
	assignedID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, assignedID, model.AssignmentTargetZone, f.zoneA.ID, false)
	unassignedID := f.approveServer(t, fr155IdentityB, "lobby-2", model.ServerKindBackend)

	_, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{assignedID, unassignedID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID,
		Reason: "换区", Operator: "admin",
	})
	if !errors.Is(err, apperr.ErrRezoneNotAssigned) {
		t.Fatalf("含未分配台的整批换区应 not_assigned，实际 %v", err)
	}
	server := f.reloadServer(t, assignedID)
	if server.ZoneID == nil || *server.ZoneID != f.zoneA.ID || server.PendingZoneID != nil {
		t.Fatalf("整批回滚后已分配 server 应保持原样 zoneA、无 pending，实际 %+v", server)
	}
	if f.auditCount(t, model.ActionServerRezoneInit) != 0 {
		t.Fatalf("整批回滚后不应残留 zone.rezone.initiated 审计")
	}
}

// TestV2DefaultEntryRequiresAssignment 未分配 server 置默认入口 → 409；已分配可置 / 清。
func TestV2DefaultEntryRequiresAssignment(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)

	if _, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowID, Value: true, Operator: "admin"}); !errors.Is(err, apperr.ErrDefaultEntryNotAssigned) {
		t.Fatalf("未分配 server 置默认入口应 409 not_assigned，实际 %v", err)
	}
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)
	view, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowID, Value: true, Operator: "admin"})
	if err != nil || !view.IsDefaultEntry {
		t.Fatalf("已分配 server 置默认入口应成功且视图 isDefaultEntry=true，实际 %+v err=%v", view, err)
	}
	view, err = f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowID, Value: false, Operator: "admin"})
	if err != nil || view.IsDefaultEntry {
		t.Fatalf("清默认入口应成功且视图 isDefaultEntry=false，实际 %+v err=%v", view, err)
	}
	if f.auditCount(t, model.ActionZoneSetDefaultEntry) != 1 || f.auditCount(t, model.ActionZoneClearDefaultEntry) != 1 {
		t.Fatalf("默认入口 set/clear 各应记 1 条审计")
	}
}

// TestV2DefaultEntryOnePerZone 同小区至多一台默认入口：后设自动顶替先前。
func TestV2DefaultEntryOnePerZone(t *testing.T) {
	f := setupFR155Cluster(t)
	rowA := f.approveServer(t, fr155IdentityA, "lobby-a", model.ServerKindBackend)
	rowB := f.approveServer(t, fr155IdentityB, "lobby-b", model.ServerKindBackend)
	f.assign(t, rowA, model.AssignmentTargetZone, f.zoneA.ID, false)
	f.assign(t, rowB, model.AssignmentTargetZone, f.zoneA.ID, false)

	if _, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowA, Value: true, Operator: "admin"}); err != nil {
		t.Fatalf("A 置默认入口失败: %v", err)
	}
	if !f.reloadServer(t, rowA).IsDefaultEntry {
		t.Fatalf("A 置后应为默认入口")
	}
	if _, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowB, Value: true, Operator: "admin"}); err != nil {
		t.Fatalf("B 置默认入口失败: %v", err)
	}
	if f.reloadServer(t, rowA).IsDefaultEntry {
		t.Fatalf("B 置默认后 A 应被清掉")
	}
	if !f.reloadServer(t, rowB).IsDefaultEntry {
		t.Fatalf("B 应是唯一默认入口")
	}
	// 不同小区互不影响
	rowC := f.approveServer(t, fr155IdentityC, "lobby-c", model.ServerKindBackend)
	f.assign(t, rowC, model.AssignmentTargetZone, f.zoneB.ID, false)
	if _, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowC, Value: true, Operator: "admin"}); err != nil {
		t.Fatalf("C 在另一小区置默认失败: %v", err)
	}
	if !f.reloadServer(t, rowB).IsDefaultEntry || !f.reloadServer(t, rowC).IsDefaultEntry {
		t.Fatalf("不同小区应各自保留默认入口，B=%v C=%v", f.reloadServer(t, rowB).IsDefaultEntry, f.reloadServer(t, rowC).IsDefaultEntry)
	}
}

// TestV2SetServerDraining 排空标记切换 + 审计 + 富化视图。
func TestV2SetServerDraining(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)

	view, err := f.svc.SetServerDraining(SetServerDrainingParams{ServerID: "lobby-1", Draining: true, Reason: "维护", Operator: "admin"})
	if err != nil || !view.Draining {
		t.Fatalf("置排空应成功且视图 draining=true，实际 %+v err=%v", view, err)
	}
	if !f.reloadServer(t, rowID).Draining {
		t.Fatalf("置排空后 server.draining 应为 true")
	}
	if f.auditCount(t, model.ActionServerSetDraining) != 1 {
		t.Fatalf("排空切换应记 1 条审计")
	}
	if _, err := f.svc.SetServerDraining(SetServerDrainingParams{ServerID: "no-such", Draining: true, Operator: "admin"}); !errors.Is(err, apperr.ErrInstanceNotFound) {
		t.Fatalf("不存在 serverId 置排空应 404，实际 %v", err)
	}
}

// TestV2ApproveExplicitNullDuringRezoneKeepsUnassigned 换区重确认显式 target:null → 确认但暂不分配。
func TestV2ApproveExplicitNullDuringRezoneKeepsUnassigned(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)
	if _, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{rowID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID, Reason: "换区", Operator: "admin",
	}); err != nil {
		t.Fatalf("发起换区失败: %v", err)
	}
	if _, err := f.svc.ApproveAgentIdentity(fr155IdentityA, ApproveAgentIdentityParams{Operator: "admin", TargetExplicitNull: true}); err != nil {
		t.Fatalf("换区确认（暂不分配）应成功: %v", err)
	}
	server := f.reloadServer(t, rowID)
	if server.ZoneID != nil || server.PendingZoneID != nil {
		t.Fatalf("暂不分配确认后应保持未分配且清 pending，实际 %+v", server)
	}
}

// TestV2ApproveExplicitTargetDuringRezoneOverridesPrefill 换区重确认显式对象目标覆盖预填。
func TestV2ApproveExplicitTargetDuringRezoneOverridesPrefill(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)
	if _, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{rowID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID, Reason: "换区", Operator: "admin",
	}); err != nil {
		t.Fatalf("发起换区失败: %v", err)
	}
	target := f.zoneA.ID
	if _, err := f.svc.ApproveAgentIdentity(fr155IdentityA, ApproveAgentIdentityParams{
		Operator: "admin", TargetKind: model.AssignmentTargetZone, TargetID: &target,
	}); err != nil {
		t.Fatalf("换区确认（显式目标）应成功: %v", err)
	}
	server := f.reloadServer(t, rowID)
	if server.ZoneID == nil || *server.ZoneID != f.zoneA.ID || server.PendingZoneID != nil {
		t.Fatalf("显式目标应落区 zoneA 覆盖预填，实际 %+v", server)
	}
}

// TestV2ZoneTreeAggregates 结构树聚合计数正确。
func TestV2ZoneTreeAggregates(t *testing.T) {
	f := setupFR155Cluster(t)
	backendID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, backendID, model.AssignmentTargetZone, f.zoneA.ID, true)
	proxyID := f.approveServer(t, fr155IdentityP, "proxy-1", model.ServerKindProxy)
	f.assign(t, proxyID, model.AssignmentTargetBCCluster, f.cluster.ID, false)
	f.approveServer(t, fr155IdentityB, "lobby-2", model.ServerKindBackend) // 未分配

	tree, err := f.svc.ZoneTree(f.ns.ID)
	if err != nil {
		t.Fatalf("拉结构树失败: %v", err)
	}
	if tree.NamespaceID != f.ns.ID || len(tree.Clusters) != 1 {
		t.Fatalf("结构树应回显 namespace 且含 1 个集群，实际 %+v", tree)
	}
	cluster := tree.Clusters[0]
	if cluster.ProxyCount != 1 {
		t.Fatalf("集群 proxyCount 应为 1，实际 %d", cluster.ProxyCount)
	}
	if len(cluster.Regions) != 1 || len(cluster.Regions[0].Zones) != 2 {
		t.Fatalf("集群应含 1 大区 2 小区，实际 %+v", cluster.Regions)
	}
	var zoneA ZoneTreeZone
	for _, z := range cluster.Regions[0].Zones {
		if z.ID == f.zoneA.ID {
			zoneA = z
		}
	}
	if zoneA.ServerCount != 1 || zoneA.DefaultEntryCount != 1 {
		t.Fatalf("zoneA 应含 1 服务器且 1 默认入口，实际 %+v", zoneA)
	}
	if tree.UnassignedCount != 1 {
		t.Fatalf("未分配计数应为 1，实际 %d", tree.UnassignedCount)
	}
}

// TestV2ListServersEnriched server 列表富化视图含归属名与在线态。
func TestV2ListServersEnriched(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)

	items, total, err := f.svc.ListServers(ListServersParams{NamespaceID: f.ns.ID})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("列表应含 1 台，实际 total=%d items=%d err=%v", total, len(items), err)
	}
	view := items[0]
	if view.ZoneName == nil || *view.ZoneName != f.zoneA.Name {
		t.Fatalf("视图应带 zoneName=%s，实际 %v", f.zoneA.Name, view.ZoneName)
	}
	if view.RegionName == nil || *view.RegionName != f.region.Name {
		t.Fatalf("视图应带 regionName=%s，实际 %v", f.region.Name, view.RegionName)
	}
	if view.BCClusterName != nil {
		t.Fatalf("backend 不应带 bcClusterName，实际 %v", *view.BCClusterName)
	}
	if !view.Assigned || !view.Online {
		t.Fatalf("已分配 active 身份的 server 应 assigned+online，实际 %+v", view)
	}
}

// TestV2ListNamespaceTrustsEnriched 信任列表 / 授予返回富化视图：双方 namespace 名（code 口径），active 行 revoked* 为 nil。
func TestV2ListNamespaceTrustsEnriched(t *testing.T) {
	f := setupFR155Cluster(t)
	other, _, err := f.svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "test", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建第二 namespace 失败: %v", err)
	}
	granted, err := f.svc.GrantNamespaceTrust(GrantNamespaceTrustParams{
		FromNamespaceID: f.ns.ID, ToNamespaceID: other.ID, Capability: model.NamespaceTrustCapabilitySchedule,
		Note: "联调", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("授予信任失败: %v", err)
	}
	if granted.FromNamespaceName != f.ns.Code || granted.ToNamespaceName != other.Code {
		t.Fatalf("授予视图名应为 %s→%s，实际 %s→%s", f.ns.Code, other.Code, granted.FromNamespaceName, granted.ToNamespaceName)
	}
	trusts, err := f.svc.ListNamespaceTrusts()
	if err != nil {
		t.Fatalf("列信任失败: %v", err)
	}
	if len(trusts) != 1 {
		t.Fatalf("应有 1 条信任，实际 %d", len(trusts))
	}
	v := trusts[0]
	if v.FromNamespaceName != f.ns.Code || v.ToNamespaceName != other.Code {
		t.Fatalf("信任视图名应为 %s→%s，实际 %s→%s", f.ns.Code, other.Code, v.FromNamespaceName, v.ToNamespaceName)
	}
	if v.Status != model.NamespaceTrustStatusActive {
		t.Fatalf("信任应 active，实际 %s", v.Status)
	}
	if v.RevokedBy != nil || v.RevokedAt != nil || v.RevokeReason != nil {
		t.Fatalf("active 信任 revoked* 应为 nil，实际 by=%v at=%v reason=%v", v.RevokedBy, v.RevokedAt, v.RevokeReason)
	}
}

// TestV2ListNamespacesWithStats namespace 列表统计摘要正确。
func TestV2ListNamespacesWithStats(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)
	other, _, err := f.svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "test", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建第二 namespace 失败: %v", err)
	}
	if _, err := f.svc.GrantNamespaceTrust(GrantNamespaceTrustParams{
		FromNamespaceID: f.ns.ID, ToNamespaceID: other.ID, Capability: model.NamespaceTrustCapabilitySchedule,
		Note: "联调", Operator: "admin",
	}); err != nil {
		t.Fatalf("授予信任失败: %v", err)
	}

	stats, err := f.svc.ListNamespacesWithStats()
	if err != nil {
		t.Fatalf("列 namespace 统计失败: %v", err)
	}
	var prod NamespaceStat
	for i := range stats {
		if stats[i].Namespace.ID == f.ns.ID {
			prod = stats[i]
		}
	}
	if prod.ServerCount != 1 || prod.BCClusterCount != 1 || prod.ActiveTrustCount != 1 {
		t.Fatalf("prod 统计应为 server=1 bc=1 trust=1，实际 %+v", prod)
	}
}

// TestV2GetAgentIdentityPrefill 身份详情在换区中附预填目标、否则为 nil。
func TestV2GetAgentIdentityPrefill(t *testing.T) {
	f := setupFR155Cluster(t)
	rowID := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowID, model.AssignmentTargetZone, f.zoneA.ID, false)

	_, prefill, err := f.svc.GetAgentIdentity(fr155IdentityA)
	if err != nil {
		t.Fatalf("取身份详情失败: %v", err)
	}
	if prefill != nil {
		t.Fatalf("未换区身份不应带预填目标，实际 %+v", prefill)
	}

	if _, err := f.svc.RezoneServers(RezoneServersParams{
		ServerIDs: []uint{rowID}, TargetKind: model.AssignmentTargetZone, TargetID: f.zoneB.ID, Reason: "换区", Operator: "admin",
	}); err != nil {
		t.Fatalf("发起换区失败: %v", err)
	}
	_, prefill, err = f.svc.GetAgentIdentity(fr155IdentityA)
	if err != nil {
		t.Fatalf("取换区中身份详情失败: %v", err)
	}
	if prefill == nil || prefill.TargetKind != model.AssignmentTargetZone || prefill.TargetID != f.zoneB.ID || prefill.TargetName != f.zoneB.Name {
		t.Fatalf("换区中身份应带预填目标 zoneB，实际 %+v", prefill)
	}
}

package service

import (
	"errors"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func newV2ControlPlaneTestService(t *testing.T) (*gorm.DB, *V2ControlPlaneService) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Namespace{},
		&model.NamespaceTrust{},
		&model.Env{},
		&model.EnvNamespace{},
		&model.BCCluster{},
		&model.Region{},
		&model.Zone{},
		&model.Server{},
		&model.AgentIdentity{},
		&model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移 v2 表失败: %v", err)
	}
	return db, NewV2ControlPlaneService(db)
}

func TestV2AgentRegisterApproveCreatesUnassignedServer(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{
		Name: "prod", Description: "生产环境", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	reg, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "11111111-1111-4111-8111-111111111111",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
		AgentVersion: "0.21.0", Addr: "10.0.0.1:25565",
	})
	if err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if reg.Status != model.AgentIdentityStatusPending {
		t.Fatalf("首次注册应进入 pending，实际 %s", reg.Status)
	}

	ident, err := svc.ApproveAgentIdentity("11111111-1111-4111-8111-111111111111", ApproveAgentIdentityParams{
		Operator: "admin",
	})
	if err != nil {
		t.Fatalf("确认身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusActive || ident.BoundAt == nil {
		t.Fatalf("确认后应为 active 且写 boundAt，实际 %+v", ident)
	}

	var server model.Server
	if err := db.Where("namespace_id = ? AND server_id = ?", ns.ID, "lobby-1").First(&server).Error; err != nil {
		t.Fatalf("确认后应创建 server 行: %v", err)
	}
	if server.ZoneID != nil || server.BCClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("首次确认后的 server 应保持未分配，实际 %+v", server)
	}
}

func TestV2NamespaceIsolationAllowsSameServerIDInDifferentNamespaces(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, prodToken, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 prod 失败: %v", err)
	}
	_, testToken, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "test", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 test 失败: %v", err)
	}

	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: prodToken, IdentityID: "22222222-2222-4222-8222-222222222222",
		ServerID: "shared-1", Kind: model.ServerKindBackend, BootID: "boot-prod",
	})
	if err != nil {
		t.Fatalf("prod 注册失败: %v", err)
	}
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: testToken, IdentityID: "33333333-3333-4333-8333-333333333333",
		ServerID: "shared-1", Kind: model.ServerKindBackend, BootID: "boot-test",
	})
	if err != nil {
		t.Fatalf("不同 namespace 应允许同名 serverId，实际失败: %v", err)
	}
}

func TestV2SameNamespacePendingServerIDConflict(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "12121212-1212-4212-8212-121212121212",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
	}); err != nil {
		t.Fatalf("首次 pending 注册失败: %v", err)
	}
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "13131313-1313-4313-8313-131313131313",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-b",
	})
	if !errors.Is(err, apperr.ErrServerIDPendingElsewhere) {
		t.Fatalf("同 namespace/serverId 的第二个 pending 应被拒绝，实际 %v", err)
	}
}

func TestV2ActiveIdentityAuthenticatesLegacyDataPlane(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "14141414-1414-4414-8414-141414141414"
	bootID := "boot-a"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID,
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: bootID,
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if err := svc.AuthenticateAgentV2(token, identityID, bootID); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Fatalf("pending 身份不应通过 legacy 数据面鉴权，实际 %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if err := svc.AuthenticateAgentV2(token, identityID, bootID); err != nil {
		t.Fatalf("active 身份应通过 legacy 数据面鉴权: %v", err)
	}
	if err := svc.AuthenticateAgentV2(token, identityID, "boot-b"); !errors.Is(err, apperr.ErrAgentStaleReregister) {
		t.Fatalf("bootId 不匹配应判陈旧 404 促重注册，实际 %v", err)
	}
}

func TestV2NamespaceTrustGrantRevokeUpdatesAllowedCheck(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	prod, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 prod 失败: %v", err)
	}
	test, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "test", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 test 失败: %v", err)
	}

	trust, err := svc.GrantNamespaceTrust(GrantNamespaceTrustParams{
		FromNamespaceID: prod.ID, ToNamespaceID: test.ID,
		Capability: model.NamespaceTrustCapabilitySchedule,
		Note:       "联调调度", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("授予信任失败: %v", err)
	}
	if !svc.NamespaceTrustAllowed(prod.ID, test.ID, model.NamespaceTrustCapabilitySchedule) {
		t.Fatalf("授予后 schedule 信任应立即生效")
	}
	if svc.NamespaceTrustAllowed(test.ID, prod.ID, model.NamespaceTrustCapabilitySchedule) {
		t.Fatalf("信任关系必须保持单向")
	}

	if err := svc.RevokeNamespaceTrust(trust.ID, "演练结束", "admin"); err != nil {
		t.Fatalf("收回信任失败: %v", err)
	}
	if svc.NamespaceTrustAllowed(prod.ID, test.ID, model.NamespaceTrustCapabilitySchedule) {
		t.Fatalf("收回后新请求应立即失去信任")
	}
}

func TestV2ServerAssignmentRequiresUnassignedServer(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "44444444-4444-4444-8444-444444444444",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("44444444-4444-4444-8444-444444444444", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
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

	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{1}, TargetKind: model.AssignmentTargetZone, TargetID: zoneA.ID,
		IsDefaultEntry: true, Reason: "首次分配", Operator: "admin",
	}); err != nil {
		t.Fatalf("首次分配应成功: %v", err)
	}

	_, err = svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{1}, TargetKind: model.AssignmentTargetZone, TargetID: zoneB.ID,
		Reason: "直接改派", Operator: "admin",
	})
	if !errors.Is(err, apperr.ErrRezoneRequired) {
		t.Fatalf("已分配 server 直接改派应返回 rezone_required，实际 %v", err)
	}
}

// TestV2ServerUnassignViaTargetNull target 零值（对应 API target:null）解除分配并清空默认入口。
func TestV2ServerUnassignViaTargetNull(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "55555555-5555-4555-8555-555555555555",
		ServerID: "lobby-u", Kind: model.ServerKindBackend, BootID: "boot-u",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("55555555-5555-4555-8555-555555555555", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc-u", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r-u", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-u", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{1}, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		IsDefaultEntry: true, Reason: "首次分配", Operator: "admin",
	}); err != nil {
		t.Fatalf("首次分配应成功: %v", err)
	}
	// 解除分配：TargetKind/TargetID 零值
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{1}, Reason: "下线维护", Operator: "admin",
	}); err != nil {
		t.Fatalf("target 零值解除分配应成功: %v", err)
	}
	var server model.Server
	if err := db.First(&server, 1).Error; err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if server.ZoneID != nil || server.BCClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("解除分配后应清空归属与默认入口，实际 zone=%v bc=%v default=%v", server.ZoneID, server.BCClusterID, server.IsDefaultEntry)
	}
}

// TestV2UnbindClearsServerAssignment 解绑身份时同步清空 server 区服归属。
func TestV2UnbindClearsServerAssignment(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "66666666-6666-4666-8666-666666666666"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID,
		ServerID: "lobby-ub", Kind: model.ServerKindBackend, BootID: "boot-ub",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc-ub", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r-ub", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-ub", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{1}, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		IsDefaultEntry: true, Reason: "首次分配", Operator: "admin",
	}); err != nil {
		t.Fatalf("首次分配应成功: %v", err)
	}
	if _, err := svc.UnbindAgentIdentity(identityID, IdentityTransitionParams{Reason: "退役", Operator: "admin"}); err != nil {
		t.Fatalf("解绑应成功: %v", err)
	}
	var server model.Server
	if err := db.First(&server, 1).Error; err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if server.ZoneID != nil || server.IsDefaultEntry {
		t.Fatalf("解绑后应清空归属，实际 zone=%v default=%v", server.ZoneID, server.IsDefaultEntry)
	}
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&ident).Error; err != nil {
		t.Fatalf("读 identity 失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusUnbound {
		t.Fatalf("身份状态应为 unbound，实际 %s", ident.Status)
	}
}

func TestV2ApproveDoesNotAssignServerDirectly(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
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
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-a", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "15151515-1515-4515-8515-151515151515",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	_, err = svc.ApproveAgentIdentity("15151515-1515-4515-8515-151515151515", ApproveAgentIdentityParams{
		Operator: "admin", TargetKind: model.AssignmentTargetZone, TargetID: &zone.ID,
	})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("P1 确认身份不应直接分配 server，实际 %v", err)
	}
}

func TestV2AuthorityCreationValidatesParents(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	if _, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: 404, Name: "bc-a", Operator: "admin"}); !errors.Is(err, apperr.ErrNamespaceNotFound) {
		t.Fatalf("不存在 namespace 不应创建 BC 集群，实际 %v", err)
	}
	if _, err := svc.CreateRegion(CreateRegionParams{BCClusterID: 404, Name: "r1", Operator: "admin"}); !errors.Is(err, apperr.ErrInstanceNotFound) {
		t.Fatalf("不存在 BC 集群不应创建大区，实际 %v", err)
	}
	if _, err := svc.CreateZone(CreateZoneParams{RegionID: 404, Name: "z-a", Operator: "admin"}); !errors.Is(err, apperr.ErrInstanceNotFound) {
		t.Fatalf("不存在大区不应创建小区，实际 %v", err)
	}
}

// TestV2AuthorityNodeDeleteGuards 空节点可删；有子节点 / 已分配服时 409。
func TestV2AuthorityNodeDeleteGuards(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc-del", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r-del", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-del", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}

	// 有大区的集群不可删
	if err := svc.DeleteBCCluster(DeleteNodeParams{ID: cluster.ID, Operator: "admin"}); !errors.Is(err, apperr.ErrBCClusterHasRegions) {
		t.Fatalf("有大区的集群删除应 BC_CLUSTER_HAS_REGIONS，实际 %v", err)
	}
	// 有小区的大区不可删
	if err := svc.DeleteRegion(DeleteNodeParams{ID: region.ID, Operator: "admin"}); !errors.Is(err, apperr.ErrRegionHasZones) {
		t.Fatalf("有小区的大区删除应 REGION_HAS_ZONES，实际 %v", err)
	}

	// 注册并分配子服后小区不可删
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "d0100000-0000-4000-8000-000000000001",
		ServerID: "lobby-del", Kind: model.ServerKindBackend, BootID: "boot-del",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("d0100000-0000-4000-8000-000000000001", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	var server model.Server
	if err := db.Where("server_id = ?", "lobby-del").First(&server).Error; err != nil {
		t.Fatalf("取 server 失败: %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID}, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		Reason: "测删", Operator: "admin",
	}); err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	if err := svc.DeleteZone(DeleteNodeParams{ID: zone.ID, Operator: "admin"}); !errors.Is(err, apperr.ErrZoneHasServers) {
		t.Fatalf("有子服的小区删除应 ZONE_HAS_SERVERS，实际 %v", err)
	}

	// 解除分配后：小区 → 大区 → 集群 可依次删空
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID}, Reason: "解除以便删除", Operator: "admin",
	}); err != nil {
		t.Fatalf("解除分配失败: %v", err)
	}
	if err := svc.DeleteZone(DeleteNodeParams{ID: zone.ID, Operator: "admin"}); err != nil {
		t.Fatalf("空小区应可删: %v", err)
	}
	if err := svc.DeleteRegion(DeleteNodeParams{ID: region.ID, Operator: "admin"}); err != nil {
		t.Fatalf("空大区应可删: %v", err)
	}
	// 代理分配后集群不可删
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "d0100000-0000-4000-8000-000000000002",
		ServerID: "proxy-del", Kind: model.ServerKindProxy, BootID: "boot-proxy-del",
	}); err != nil {
		t.Fatalf("代理注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("d0100000-0000-4000-8000-000000000002", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("代理确认失败: %v", err)
	}
	var proxy model.Server
	if err := db.Where("server_id = ?", "proxy-del").First(&proxy).Error; err != nil {
		t.Fatalf("取 proxy 失败: %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{proxy.ID}, TargetKind: model.AssignmentTargetBCCluster, TargetID: cluster.ID,
		Reason: "测代理删", Operator: "admin",
	}); err != nil {
		t.Fatalf("代理分配失败: %v", err)
	}
	if err := svc.DeleteBCCluster(DeleteNodeParams{ID: cluster.ID, Operator: "admin"}); !errors.Is(err, apperr.ErrBCClusterHasProxies) {
		t.Fatalf("有代理的集群删除应 BC_CLUSTER_HAS_PROXIES，实际 %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{proxy.ID}, Reason: "解除代理", Operator: "admin",
	}); err != nil {
		t.Fatalf("解除代理失败: %v", err)
	}
	if err := svc.DeleteBCCluster(DeleteNodeParams{ID: cluster.ID, Operator: "admin"}); err != nil {
		t.Fatalf("空集群应可删: %v", err)
	}
	// 不存在
	if err := svc.DeleteZone(DeleteNodeParams{ID: 404, Operator: "admin"}); !errors.Is(err, apperr.ErrZoneNotFound) {
		t.Fatalf("不存在小区应 ZONE_NOT_FOUND，实际 %v", err)
	}
}

// TestV2ZoneTreeCountsAssignedServers 分配后 zone-tree 计数与 ListServers 归属一致。
func TestV2ZoneTreeCountsAssignedServers(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc-tree", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r-tree", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "z-tree", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "a1ee0000-0000-4000-8000-000000000001",
		ServerID: "lobby-tree", Kind: model.ServerKindBackend, BootID: "boot-tree",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("a1ee0000-0000-4000-8000-000000000001", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	var server model.Server
	if err := db.Where("server_id = ?", "lobby-tree").First(&server).Error; err != nil {
		t.Fatalf("取 server 失败: %v", err)
	}
	if _, err := svc.AssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID}, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		IsDefaultEntry: true, Reason: "测树计数", Operator: "admin",
	}); err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	tree, err := svc.ZoneTree(ns.ID)
	if err != nil {
		t.Fatalf("zone-tree 失败: %v", err)
	}
	if len(tree.Clusters) != 1 || tree.Clusters[0].ProxyCount != 0 {
		t.Fatalf("树应 1 集群 0 代理，实际 %+v", tree.Clusters)
	}
	if len(tree.Clusters[0].Regions) != 1 || len(tree.Clusters[0].Regions[0].Zones) != 1 {
		t.Fatalf("树应有 1 大区 1 小区，实际 %+v", tree.Clusters[0])
	}
	z := tree.Clusters[0].Regions[0].Zones[0]
	if z.ServerCount != 1 || z.DefaultEntryCount != 1 {
		t.Fatalf("分配后 serverCount/defaultEntry 应为 1，实际 %+v", z)
	}
	if tree.UnassignedCount != 0 {
		t.Fatalf("分配后 unassigned 应为 0，实际 %d", tree.UnassignedCount)
	}
	views, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, PageSize: 50})
	if err != nil || total != 1 {
		t.Fatalf("ListServers 应 1 台，total=%d err=%v", total, err)
	}
	if views[0].ZoneID == nil || *views[0].ZoneID != zone.ID || !views[0].Assigned {
		t.Fatalf("ListServers 应带 zone 且 assigned，实际 %+v", views[0])
	}
}

func TestV2Q3ServerIDOccupiedRequiresForceUnbind(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "55555555-5555-4555-8555-555555555555",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-old",
	}); err != nil {
		t.Fatalf("旧身份注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("55555555-5555-4555-8555-555555555555", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("旧身份确认失败: %v", err)
	}

	reg, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "66666666-6666-4666-8666-666666666666",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-new",
	})
	if err != nil {
		t.Fatalf("新身份抢占已绑定 serverId 应进入 pending，实际失败: %v", err)
	}
	if reg.Status != model.AgentIdentityStatusPending {
		t.Fatalf("新身份应保持 pending，实际 %s", reg.Status)
	}
	var pending model.AgentIdentity
	if err := db.Where("identity_id = ?", "66666666-6666-4666-8666-666666666666").First(&pending).Error; err != nil {
		t.Fatalf("新身份应已落库: %v", err)
	}
	if pending.ConflictReason != "server-id-occupied" {
		t.Fatalf("Q3 pending 应带占用冲突原因，实际 %q", pending.ConflictReason)
	}

	_, err = svc.ApproveAgentIdentity("66666666-6666-4666-8666-666666666666", ApproveAgentIdentityParams{Operator: "admin"})
	if !errors.Is(err, apperr.ErrServerIDOccupied) {
		t.Fatalf("未强制解绑旧身份时确认应失败，实际 %v", err)
	}
	if _, err = svc.ApproveAgentIdentity("66666666-6666-4666-8666-666666666666", ApproveAgentIdentityParams{
		Operator: "admin", ForceUnbindOccupier: true,
	}); err != nil {
		t.Fatalf("强制解绑后确认新身份应成功: %v", err)
	}
	var old model.AgentIdentity
	if err := db.Where("namespace_id = ? AND server_id = ? AND identity_id = ?", ns.ID, "lobby-1", "55555555-5555-4555-8555-555555555555").First(&old).Error; err != nil {
		t.Fatalf("旧身份应仍保留历史行: %v", err)
	}
	if old.Status != model.AgentIdentityStatusUnbound {
		t.Fatalf("旧身份应被解绑，实际 %s", old.Status)
	}
}

func TestV2IdentityAdminTransitions(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "77777777-7777-4777-8777-777777777777",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	rejected, err := svc.RejectAgentIdentity("77777777-7777-4777-8777-777777777777", IdentityTransitionParams{
		Reason: "重复申请", Operator: "admin",
	})
	if err != nil || rejected.Status != model.AgentIdentityStatusRejected {
		t.Fatalf("拒绝 pending 应成功，实际 %+v err=%v", rejected, err)
	}
	reapply, err := svc.AllowAgentIdentityReapply("77777777-7777-4777-8777-777777777777", IdentityTransitionParams{
		Reason: "资料已修正", Operator: "admin",
	})
	if err != nil || reapply.Status != model.AgentIdentityStatusExpired {
		t.Fatalf("允许重新申请应转 expired，实际 %+v err=%v", reapply, err)
	}
}

func TestV2IdentityDisableEnableUnbind(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "88888888-8888-4888-8888-888888888888",
		ServerID: "lobby-1", Kind: model.ServerKindBackend, BootID: "boot-a",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("88888888-8888-4888-8888-888888888888", ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	disabled, err := svc.DisableAgentIdentity("88888888-8888-4888-8888-888888888888", IdentityTransitionParams{
		Reason: "维护", Operator: "admin",
	})
	if err != nil || disabled.Status != model.AgentIdentityStatusDisabled {
		t.Fatalf("禁用 active 应成功，实际 %+v err=%v", disabled, err)
	}
	enabled, err := svc.EnableAgentIdentity("88888888-8888-4888-8888-888888888888", IdentityTransitionParams{Operator: "admin"})
	if err != nil || enabled.Status != model.AgentIdentityStatusActive {
		t.Fatalf("启用 disabled 应成功，实际 %+v err=%v", enabled, err)
	}
	unbound, err := svc.UnbindAgentIdentity("88888888-8888-4888-8888-888888888888", IdentityTransitionParams{
		Reason: "换区", Operator: "admin",
	})
	if err != nil || unbound.Status != model.AgentIdentityStatusUnbound {
		t.Fatalf("解绑 active 应成功，实际 %+v err=%v", unbound, err)
	}
	_, err = svc.DisableAgentIdentity("88888888-8888-4888-8888-888888888888", IdentityTransitionParams{
		Reason: "非法状态", Operator: "admin",
	})
	if !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("unbound 再禁用应 illegal_state，实际 %v", err)
	}
}

package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
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
		&model.LobbyCluster{},
		&model.Server{},
		&model.AgentIdentity{},
		&model.AgentEndpoint{},
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
		Operator: "admin", ServerID: "lobby-1",
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

// approveFR203Identity 以真实强类型审批契约分配 serverId。
func approveFR203Identity(t *testing.T, svc *V2ControlPlaneService, identityID, serverID string) (*model.AgentIdentity, error) {
	t.Helper()
	return svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin", ServerID: serverID})
}

func TestFR203NewIdentityWithoutServerIDEntersPending(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "20300000-0000-4000-8000-000000000001"

	registration, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-pending",
	})
	if err != nil {
		t.Fatalf("新身份无 serverId 注册应进入 pending，实际失败: %v", err)
	}
	if registration.Status != model.AgentIdentityStatusPending {
		t.Fatalf("新身份无 serverId 应为 pending，实际 %s", registration.Status)
	}

	var row struct {
		ServerID *string `gorm:"column:server_id"`
	}
	if err := db.Model(&model.AgentIdentity{}).Select("server_id").Where("identity_id = ?", identityID).Scan(&row).Error; err != nil {
		t.Fatalf("读取待确认身份失败: %v", err)
	}
	if row.ServerID != nil {
		t.Fatalf("pending 身份的 serverId 应为 NULL，实际 %q", *row.ServerID)
	}
}

func TestFR203ApproveRequiresExplicitServerIDAndCommitsBindingAtomically(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "20300000-0000-4000-8000-000000000002"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, ServerID: "legacy-pending-203",
		Kind: model.ServerKindBackend, BootID: "boot-approve",
	}); err != nil {
		t.Fatalf("新身份注册失败: %v", err)
	}

	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin"}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("即使 pending 行有旧 serverId，未显式分配 serverId 的审批也应被拒绝，实际 %v", err)
	}
	var pending model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&pending).Error; err != nil {
		t.Fatalf("读取待确认身份失败: %v", err)
	}
	if pending.Status != model.AgentIdentityStatusPending || pending.ServerID != "legacy-pending-203" {
		t.Fatalf("失败审批不得改变 pending 绑定，实际 %+v", pending)
	}
	var serverCount int64
	if err := db.Model(&model.Server{}).Where("namespace_id = ?", ns.ID).Count(&serverCount).Error; err != nil {
		t.Fatalf("统计 server 失败: %v", err)
	}
	if serverCount != 0 {
		t.Fatalf("失败审批不得创建 server 行，实际 %d", serverCount)
	}

	identity, err := approveFR203Identity(t, svc, identityID, "lobby-203")
	if err != nil {
		t.Fatalf("显式分配 serverId 的审批应成功: %v", err)
	}
	if identity.Status != model.AgentIdentityStatusActive || identity.ServerID != "lobby-203" || identity.BoundAt == nil {
		t.Fatalf("审批成功后应原子写入 active 绑定，实际 %+v", identity)
	}
	var server model.Server
	if err := db.Where("namespace_id = ? AND server_id = ?", ns.ID, "lobby-203").First(&server).Error; err != nil {
		t.Fatalf("审批成功后应创建权威 server 行: %v", err)
	}
	var auditCount int64
	if err := db.Model(&model.AuditLog{}).Where("action = ? AND target_ref = ?", model.ActionIdentityApproved, identityID).Count(&auditCount).Error; err != nil {
		t.Fatalf("统计审批审计失败: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("审批成功后应在同一事务写一条审批审计，实际 %d", auditCount)
	}
}

func TestFR203ExistingIdentityAutoKeepsBindingAndMismatchesFailClosed(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	_, prodToken, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 prod namespace 失败: %v", err)
	}
	_, otherToken, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "other", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 other namespace 失败: %v", err)
	}
	identityID := "20300000-0000-4000-8000-000000000003"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: prodToken, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-first",
	}); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if _, err := approveFR203Identity(t, svc, identityID, "legacy-203"); err != nil {
		t.Fatalf("首次审批失败: %v", err)
	}

	registration, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: prodToken, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-upgrade",
	})
	if err != nil {
		t.Fatalf("既有匹配身份应自动保持 active，实际失败: %v", err)
	}
	if registration.Status != model.AgentIdentityStatusActive || registration.ServerID == nil || *registration.ServerID != "legacy-203" {
		t.Fatalf("既有匹配身份应沿用权威绑定，实际 %+v", registration)
	}

	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", identityID).Update("server_id", nil).Error; err != nil {
		t.Fatalf("构造缺 serverId 的活跃脏数据失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: prodToken, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-invalid-active",
	}); !errors.Is(err, apperr.ErrIdentityBindingMismatch) {
		t.Fatalf("活跃身份缺 serverId 必须 fail-closed，实际 %v", err)
	}
	if _, err := svc.AuthenticateAgentReport(prodToken, identityID, "", ""); !errors.Is(err, apperr.ErrAgentNotConfirmed) {
		t.Fatalf("活跃身份缺 serverId 的数据面必须 fail-closed，实际 %v", err)
	}

	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: prodToken, IdentityID: identityID, Kind: model.ServerKindProxy, BootID: "boot-kind-mismatch",
	}); !errors.Is(err, apperr.ErrIdentityBindingMismatch) {
		t.Fatalf("角色不匹配必须 fail-closed，实际 %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: otherToken, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-namespace-mismatch",
	}); !errors.Is(err, apperr.ErrIdentityBindingMismatch) {
		t.Fatalf("token namespace 不匹配必须 fail-closed，实际 %v", err)
	}
}

func TestFR203ActiveBindingSnapshotUsesAuthority(t *testing.T) {
	_, svc, token, identityID, boundAt := arrangeFR203BindingSnapshot(t)

	poll, err := svc.GetAgentRegistrationV2(token, identityID)
	if err != nil {
		t.Fatalf("轮询身份状态失败: %v", err)
	}
	if poll.BoundAt == nil || !poll.BoundAt.Equal(boundAt) || poll.BindingFingerprint == nil {
		t.Fatalf("active 轮询必须返回权威绑定快照，实际 %+v", poll)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{
		"beacon-binding-v1", identityID, "prod", "lobby-binding", model.ServerKindBackend,
		boundAt.Format(time.RFC3339Nano),
	}, "\n"))))
	if *poll.BindingFingerprint != expected {
		t.Fatalf("绑定指纹必须使用稳定权威字段摘要，期望 %s，实际 %s", expected, *poll.BindingFingerprint)
	}
}

func TestFR203DisabledAndReregisteredBindingSnapshotsUseAuthority(t *testing.T) {
	db, svc, token, identityID, boundAt := arrangeFR203BindingSnapshot(t)
	expected := agentBindingFingerprint(identityID, "prod", "lobby-binding", model.ServerKindBackend, boundAt)

	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", identityID).
		Update("status", model.AgentIdentityStatusDisabled).Error; err != nil {
		t.Fatalf("构造 disabled 身份失败: %v", err)
	}
	disabled, err := svc.GetAgentRegistrationV2(token, identityID)
	if err != nil {
		t.Fatalf("disabled 身份轮询失败: %v", err)
	}
	if disabled.BoundAt == nil || disabled.BindingFingerprint == nil ||
		!disabled.BoundAt.Equal(boundAt) || *disabled.BindingFingerprint != expected {
		t.Fatalf("disabled 身份也必须返回原有权威绑定快照，实际 %+v", disabled)
	}
	disabledRegistration, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-binding-disabled",
	})
	if err != nil {
		t.Fatalf("disabled 身份重注册失败: %v", err)
	}
	if disabledRegistration.BoundAt == nil || disabledRegistration.BindingFingerprint == nil ||
		!disabledRegistration.BoundAt.Equal(boundAt) || *disabledRegistration.BindingFingerprint != expected {
		t.Fatalf("disabled 注册也必须返回原有权威绑定快照，实际 %+v", disabledRegistration)
	}
	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", identityID).
		Update("status", model.AgentIdentityStatusActive).Error; err != nil {
		t.Fatalf("恢复 active 身份失败: %v", err)
	}
	registration, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-binding-reregister",
	})
	if err != nil {
		t.Fatalf("active 身份重注册失败: %v", err)
	}
	if registration.BoundAt == nil || registration.BindingFingerprint == nil ||
		!registration.BoundAt.Equal(boundAt) || *registration.BindingFingerprint != expected {
		t.Fatalf("active 重注册必须返回同一绑定快照，实际 %+v", registration)
	}
}

func TestFR203BindingSnapshotFailsClosedWhenBoundAtMissing(t *testing.T) {
	db, svc, token, identityID, _ := arrangeFR203BindingSnapshot(t)

	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", identityID).Update("bound_at", nil).Error; err != nil {
		t.Fatalf("构造缺 boundAt 的 active 脏数据失败: %v", err)
	}
	poll, err := svc.GetAgentRegistrationV2(token, identityID)
	if err != nil {
		t.Fatalf("缺 boundAt 身份轮询失败: %v", err)
	}
	if poll.BoundAt != nil || poll.BindingFingerprint != nil {
		t.Fatalf("active 身份缺任一绑定事实时不得伪造快照，实际 %+v", poll)
	}
	registration, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-binding-incomplete",
	})
	if err != nil {
		t.Fatalf("缺 boundAt 身份重注册失败: %v", err)
	}
	if registration.BoundAt != nil || registration.BindingFingerprint != nil {
		t.Fatalf("注册响应缺任一绑定事实时不得生成本地快照，实际 %+v", registration)
	}
}

func arrangeFR203BindingSnapshot(t *testing.T) (*gorm.DB, *V2ControlPlaneService, string, string, time.Time) {
	t.Helper()
	db, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "20300000-0000-4000-8000-000000000004"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-binding",
	}); err != nil {
		t.Fatalf("创建待确认身份失败: %v", err)
	}
	if _, err := approveFR203Identity(t, svc, identityID, "lobby-binding"); err != nil {
		t.Fatalf("审批身份失败: %v", err)
	}
	boundAt := time.Date(2026, time.July, 28, 9, 30, 0, 123456789, time.UTC)
	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", identityID).Update("bound_at", boundAt).Error; err != nil {
		t.Fatalf("写入固定权威 boundAt 失败: %v", err)
	}
	return db, svc, token, identityID, boundAt
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
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-1"}); err != nil {
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
	if _, err := svc.ApproveAgentIdentity("44444444-4444-4444-8444-444444444444", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-1"}); err != nil {
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
	if _, err := svc.ApproveAgentIdentity("55555555-5555-4555-8555-555555555555", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-u"}); err != nil {
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
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", 1).Update("lobby_cluster_id", lobby.ID).Error; err != nil {
		t.Fatalf("构造历史双挂数据失败: %v", err)
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
	if server.ZoneID != nil || server.BCClusterID != nil || server.LobbyClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("解除分配后应清空归属与默认入口，实际 zone=%v bc=%v lobby=%v default=%v", server.ZoneID, server.BCClusterID, server.LobbyClusterID, server.IsDefaultEntry)
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
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-ub"}); err != nil {
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
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", 1).Update("lobby_cluster_id", lobby.ID).Error; err != nil {
		t.Fatalf("构造历史双挂数据失败: %v", err)
	}
	if _, err := svc.UnbindAgentIdentity(identityID, IdentityTransitionParams{Reason: "退役", Operator: "admin"}); err != nil {
		t.Fatalf("解绑应成功: %v", err)
	}
	var server model.Server
	if err := db.First(&server, 1).Error; err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if server.ZoneID != nil || server.BCClusterID != nil || server.LobbyClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("解绑后应清空全部归属，实际 zone=%v bc=%v lobby=%v default=%v", server.ZoneID, server.BCClusterID, server.LobbyClusterID, server.IsDefaultEntry)
	}
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&ident).Error; err != nil {
		t.Fatalf("读 identity 失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusUnbound {
		t.Fatalf("身份状态应为 unbound，实际 %s", ident.Status)
	}
}

// TestFR199LobbyMembershipCountsAsAssigned 锁定大厅成员也属于已分配资产，普通分配不得跨越大厅归属。
func TestFR199LobbyMembershipCountsAsAssigned(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	server := model.Server{NamespaceID: ns.ID, ServerID: "lobby-only", Kind: model.ServerKindBackend, LobbyClusterID: &lobby.ID}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("创建大厅成员 server 失败: %v", err)
	}
	assigned := true
	views, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, Assigned: &assigned})
	if err != nil {
		t.Fatalf("查询已分配 server 失败: %v", err)
	}
	if total != 1 || len(views) != 1 || !views[0].Assigned {
		t.Fatalf("大厅成员必须计为已分配，实际 total=%d views=%+v", total, views)
	}
	if err := validateAssignableServer(&server, ns.ID, model.AssignmentTargetZone); !errors.Is(err, apperr.ErrRezoneRequired) {
		t.Fatalf("大厅成员不得走普通分配，实际 %v", err)
	}
	applyAssignment(&server, model.AssignmentTargetZone, 77, true)
	if server.LobbyClusterID != nil || server.ZoneID == nil || *server.ZoneID != 77 {
		t.Fatalf("普通归属写入必须清理大厅归属，实际 %+v", server)
	}
}

// TestFR199ServerViewProjectsNullableLobbyClusterID 锁定大厅归属在列表与单条读视图中一致投影。
func TestFR199ServerViewProjectsNullableLobbyClusterID(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	lobbyServer := model.Server{NamespaceID: ns.ID, ServerID: "lobby-view", Kind: model.ServerKindBackend, LobbyClusterID: &lobby.ID}
	regularServer := model.Server{NamespaceID: ns.ID, ServerID: "regular-view", Kind: model.ServerKindBackend}
	if err := db.Create(&lobbyServer).Error; err != nil {
		t.Fatalf("创建大厅成员失败: %v", err)
	}
	if err := db.Create(&regularServer).Error; err != nil {
		t.Fatalf("创建普通服务器失败: %v", err)
	}

	items, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, PageSize: 20})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("读取 server 列表失败: total=%d items=%d err=%v", total, len(items), err)
	}
	views := map[string]ServerView{}
	for _, item := range items {
		views[item.ServerID] = item
	}
	if view := views[lobbyServer.ServerID]; view.LobbyClusterID == nil || *view.LobbyClusterID != lobby.ID || !view.Assigned {
		t.Fatalf("大厅成员应投影 lobbyClusterId 且 assigned=true，实际 %+v", view)
	}
	if view := views[regularServer.ServerID]; view.LobbyClusterID != nil || view.Assigned {
		t.Fatalf("普通服务器应投影 lobbyClusterId=null 且 assigned=false，实际 %+v", view)
	}

	single, err := enrichSingleServer(db, lobbyServer)
	if err != nil || single.LobbyClusterID == nil || *single.LobbyClusterID != lobby.ID || !single.Assigned {
		t.Fatalf("单条富化读应与列表保持大厅归属一致，view=%+v err=%v", single, err)
	}
}

// TestFR199RezoneClearsStaleLobbyMembership 锁定历史双挂数据走既有换区链时，不能残留大厅归属。
func TestFR199RezoneClearsStaleLobbyMembership(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Name: "bc", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "r", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	from, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "from", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建源小区失败: %v", err)
	}
	to, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "to", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建目标小区失败: %v", err)
	}
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	server := model.Server{NamespaceID: ns.ID, ServerID: "legacy-double", Kind: model.ServerKindBackend, ZoneID: &from.ID, LobbyClusterID: &lobby.ID}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("创建历史双挂 server 失败: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.initRezone(tx, &server, RezoneServersParams{TargetID: to.ID, Reason: "迁移"}, model.AssignmentTargetZone, now, now.Add(time.Hour))
	}); err != nil {
		t.Fatalf("历史双挂 server 发起换区应成功，实际 %v", err)
	}
	var got model.Server
	if err := db.First(&got, server.ID).Error; err != nil {
		t.Fatalf("读取换区后的 server 失败: %v", err)
	}
	if got.ZoneID != nil || got.BCClusterID != nil || got.LobbyClusterID != nil || got.PendingZoneID == nil || *got.PendingZoneID != to.ID {
		t.Fatalf("换区必须清空全部当前归属并保留目标预填，实际 %+v", got)
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
		Operator: "admin", ServerID: "lobby-1", TargetKind: model.AssignmentTargetZone, TargetID: &zone.ID,
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
	if _, err := svc.ApproveAgentIdentity("d0100000-0000-4000-8000-000000000001", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-del"}); err != nil {
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
	if _, err := svc.ApproveAgentIdentity("d0100000-0000-4000-8000-000000000002", ApproveAgentIdentityParams{Operator: "admin", ServerID: "proxy-del"}); err != nil {
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
	if _, err := svc.ApproveAgentIdentity("a1ee0000-0000-4000-8000-000000000001", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-tree"}); err != nil {
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
	if _, err := svc.ApproveAgentIdentity("55555555-5555-4555-8555-555555555555", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-1"}); err != nil {
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

	_, err = svc.ApproveAgentIdentity("66666666-6666-4666-8666-666666666666", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-1"})
	if !errors.Is(err, apperr.ErrServerIDOccupied) {
		t.Fatalf("未强制解绑旧身份时确认应失败，实际 %v", err)
	}
	if _, err = svc.ApproveAgentIdentity("66666666-6666-4666-8666-666666666666", ApproveAgentIdentityParams{
		Operator: "admin", ServerID: "lobby-1", ForceUnbindOccupier: true,
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
	if _, err := svc.ApproveAgentIdentity("88888888-8888-4888-8888-888888888888", ApproveAgentIdentityParams{Operator: "admin", ServerID: "lobby-1"}); err != nil {
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

func TestFR205StableNamesCreateAndPatch(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Code: "prod", DisplayName: "生产", Operator: "admin"})
	if err != nil {
		t.Fatalf("code/displayName 创建 namespace 应成功: %v", err)
	}
	if ns.Code != "prod" || ns.Name != "生产" {
		t.Fatalf("namespace 双名称不符：%+v", ns)
	}
	if _, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "old", Code: "new", Operator: "admin"}); !errors.Is(err, apperr.ErrAmbiguousIdentifier) {
		t.Fatalf("name/code 不一致应返回 AMBIGUOUS_IDENTIFIER，实际 %v", err)
	}

	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Code: "bc", DisplayName: "代理", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	if _, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Code: "bc-2", DisplayName: "代理", Operator: "admin"}); err != nil {
		t.Fatalf("displayName 重复应允许，实际 %v", err)
	}
	if _, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: ns.ID, Code: "bc", DisplayName: "另一个", Operator: "admin"}); !errors.Is(err, apperr.ErrBCClusterConflict) {
		t.Fatalf("同 namespace 下 code 重复应冲突，实际 %v", err)
	}

	nextDisplay := "代理新名"
	updated, err := svc.UpdateBCCluster(UpdateDisplayResourceParams{ID: cluster.ID, DisplayName: &nextDisplay, Operator: "admin"})
	if err != nil {
		t.Fatalf("更新 displayName 应成功: %v", err)
	}
	if updated.Code != "bc" || updated.Name != "代理新名" {
		t.Fatalf("更新后 code 不应变化，实际 %+v", updated)
	}
	changedCode := "bc-new"
	if _, err := svc.UpdateBCCluster(UpdateDisplayResourceParams{ID: cluster.ID, Code: &changedCode, Operator: "admin"}); !errors.Is(err, apperr.ErrImmutableIdentifier) {
		t.Fatalf("修改 code 应返回 IMMUTABLE_IDENTIFIER，实际 %v", err)
	}

	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Code: "region", DisplayName: "大区", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	_, err = svc.CreateZone(CreateZoneParams{RegionID: region.ID, Code: "zone", DisplayName: "小区", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	tree, err := svc.ZoneTree(ns.ID)
	if err != nil {
		t.Fatalf("读取 zone-tree 失败: %v", err)
	}
	gotZone := tree.Clusters[0].Regions[0].Zones[0]
	if tree.Clusters[0].Name != "bc" || tree.Clusters[0].DisplayName != "代理新名" || gotZone.Name != "zone" || gotZone.DisplayName != "小区" {
		t.Fatalf("zone-tree 双名称不符：%+v", tree)
	}
}

func TestFR205ServerDisplayNameUpdateAndKeyword(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := &model.Server{NamespaceID: ns.ID, ServerID: "lobby-1", DisplayName: "大厅一", Kind: model.ServerKindBackend}
	if err := db.Create(server).Error; err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}

	views, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, Keyword: "大厅", PageSize: 20})
	if err != nil || total != 1 || len(views) != 1 || views[0].DisplayName != "大厅一" {
		t.Fatalf("keyword 应匹配 displayName，total=%d views=%+v err=%v", total, views, err)
	}
	next := "大厅新名"
	updated, err := svc.UpdateServerDisplayName(UpdateServerDisplayNameParams{ID: server.ID, DisplayName: &next, Operator: "admin"})
	if err != nil {
		t.Fatalf("更新 server displayName 失败: %v", err)
	}
	if updated.ServerID != "lobby-1" || updated.DisplayName != "大厅新名" {
		t.Fatalf("server 更新视图不符：%+v", updated)
	}
	changedServerID := "lobby-2"
	if _, err := svc.UpdateServerDisplayName(UpdateServerDisplayNameParams{ID: server.ID, ServerID: &changedServerID, Operator: "admin"}); !errors.Is(err, apperr.ErrImmutableIdentifier) {
		t.Fatalf("修改 serverId 应返回 IMMUTABLE_IDENTIFIER，实际 %v", err)
	}
}

// TestArchivedServerRejectsAgentIdentityRuntimePaths 验证归档 server 不可被注册、审批绑定或运行鉴权重新激活。
func TestArchivedServerRejectsAgentIdentityRuntimePaths(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "archived-runtime", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := &model.Server{NamespaceID: ns.ID, ServerID: "archived-1", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleArchived}
	if err := db.Create(server).Error; err != nil {
		t.Fatalf("创建归档 server 失败: %v", err)
	}
	now := time.Now().UTC()
	active := &model.AgentIdentity{
		IdentityID: "a0010000-0000-4000-8000-000000000001", NamespaceID: ns.ID, ServerID: model.NullableServerID(server.ServerID),
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, BootID: "b0010000-0000-4000-8000-000000000001", StatusChangedAt: now,
	}
	if err := db.Create(active).Error; err != nil {
		t.Fatalf("创建归档 server 的活跃身份失败: %v", err)
	}

	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: active.IdentityID, Kind: active.Kind, BootID: active.BootID, Addr: "127.0.0.1:25565",
	})
	if !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("归档 server 的既有身份注册应返回 SERVER_ARCHIVED，实际 %v", err)
	}
	if err := svc.AuthenticateAgentV2(token, active.IdentityID, active.BootID); !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("归档 server 的运行鉴权应返回 SERVER_ARCHIVED，实际 %v", err)
	}
	if _, err := svc.AuthenticateAgentReport(token, active.IdentityID, active.BootID, active.LastAddr); !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("归档 server 的上报鉴权应返回 SERVER_ARCHIVED，实际 %v", err)
	}

	pendingID := "a0010000-0000-4000-8000-000000000002"
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: pendingID, Kind: model.ServerKindBackend, BootID: "b0010000-0000-4000-8000-000000000002",
	}); err != nil {
		t.Fatalf("创建待确认身份失败: %v", err)
	}
	if _, err := svc.RequestApproveAgentIdentity(pendingID, ApproveAgentIdentityParams{ServerID: server.ServerID, Operator: "admin"}, auth.HumanPrincipal("admin"), "archived-approval"); !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("身份审批请求绑定归档 server 应返回 SERVER_ARCHIVED，实际 %v", err)
	}
	_, err = svc.ApproveAgentIdentity(pendingID, ApproveAgentIdentityParams{ServerID: server.ServerID, Operator: "admin"})
	if !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("身份最终执行绑定归档 server 应返回 SERVER_ARCHIVED，实际 %v", err)
	}
	var pending model.AgentIdentity
	if err := db.Where("identity_id = ?", pendingID).First(&pending).Error; err != nil {
		t.Fatalf("读取审批失败后的身份失败: %v", err)
	}
	if pending.Status != model.AgentIdentityStatusPending || pending.ServerID.Assigned() {
		t.Fatalf("审批失败后应保留待确认未绑定状态，实际 status=%s serverId=%q", pending.Status, string(pending.ServerID))
	}

	activeServer := &model.Server{NamespaceID: ns.ID, ServerID: "active-1", Kind: model.ServerKindBackend}
	if err := db.Create(activeServer).Error; err != nil {
		t.Fatalf("创建 active server 失败: %v", err)
	}
	activeIdentity := &model.AgentIdentity{
		IdentityID: "a0010000-0000-4000-8000-000000000003", NamespaceID: ns.ID, ServerID: model.NullableServerID(activeServer.ServerID),
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, BootID: "b0010000-0000-4000-8000-000000000003", StatusChangedAt: now,
	}
	if err := db.Create(activeIdentity).Error; err != nil {
		t.Fatalf("创建 active server 的身份失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "a0010000-0000-4000-8000-000000000004", ServerID: activeServer.ServerID,
		Kind: model.ServerKindBackend, BootID: "b0010000-0000-4000-8000-000000000004",
	}); err != nil {
		t.Fatalf("active server 注册应保持可用，实际 %v", err)
	}
	if err := svc.AuthenticateAgentV2(token, activeIdentity.IdentityID, activeIdentity.BootID); err != nil {
		t.Fatalf("active server 的运行鉴权应保持可用，实际 %v", err)
	}
	if _, err := svc.AuthenticateAgentReport(token, activeIdentity.IdentityID, activeIdentity.BootID, ""); err != nil {
		t.Fatalf("active server 的上报鉴权应保持可用，实际 %v", err)
	}
}

// TestListServerViewsArchivedAlwaysOffline 验证归档 server 保留生命周期展示，但不会因 active identity 被标成在线。
func TestListServerViewsArchivedAlwaysOffline(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "archived-view", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := &model.Server{NamespaceID: ns.ID, ServerID: "archived-view-1", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleArchived}
	if err := db.Create(server).Error; err != nil {
		t.Fatalf("创建归档 server 失败: %v", err)
	}
	if err := db.Create(&model.AgentIdentity{
		IdentityID: "a0020000-0000-4000-8000-000000000001", NamespaceID: ns.ID, ServerID: model.NullableServerID(server.ServerID),
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("创建活跃身份失败: %v", err)
	}

	views, total, err := svc.ListServers(ListServersParams{NamespaceID: ns.ID, PageSize: 20})
	if err != nil {
		t.Fatalf("列出 server 视图失败: %v", err)
	}
	if total != 1 || len(views) != 1 || views[0].Lifecycle != model.ServerLifecycleArchived || views[0].Online {
		t.Fatalf("归档 server 应展示 archived 且 online=false，实际 total=%d views=%+v", total, views)
	}
}

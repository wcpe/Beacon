package service

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// FR-235：控制面预置的占位空壳在审批时应自动让位给真 agent，无需人工 forceUnbindOccupier。
//
// 背景（真机实证）：CP 推送（FR-222 机器注册）会替尚未启动插件的服预先建一行 active 身份，
// 该行不带 bootId（插件还没生成），来源标记为 machine_registered。插件随后启动注册并审批时
// 会撞上这行壳，历史行为要求人工逐台解绑——批量建 60 台即 60 次人工审批，与 FR-222 的
// 立项初衷（消灭 60 次人工审批）直接冲突。本组用例锁定修复后的双向语义。

// newFr235TestService 建一个含 V2ControlPlaneService 的测试栈（机器注册 + v2 审批同库）。
func newFr235TestService(t *testing.T) (*v2ControlPlaneTestService, *gorm.DB) {
	t.Helper()
	db, svc := newV2ControlPlaneTestService(t)
	return svc, db
}

// seedPreplacedIdentity 直接落一行「控制面预置的占位空壳」：machine_registered 来源 + 空 bootId。
func seedPreplacedIdentity(t *testing.T, db *gorm.DB, nsID uint, identityID, serverID, bootID string) {
	t.Helper()
	if err := db.Create(&model.AgentIdentity{
		IdentityID: identityID, NamespaceID: nsID, ServerID: model.NullableServerID(serverID),
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive,
		BootID:        bootID,
		BindingSource: model.AgentIdentityBindingSourceMachineRegistered,
	}).Error; err != nil {
		t.Fatalf("落预置占位身份失败: %v", err)
	}
}

// TestFR235PreplacedOccupierYieldsWithoutForceUnbind 预置空壳（machine_registered + 空 bootId）
// 在审批时应自动让位：不带 ForceUnbindOccupier 也能确认成功，且旧壳转 unbound。
func TestFR235PreplacedOccupierYieldsWithoutForceUnbind(t *testing.T) {
	svc, db := newFr235TestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	const (
		preplacedID = "11111111-1111-4111-8111-111111111111"
		realAgentID = "22222222-2222-4222-8222-222222222222"
		serverID    = "lobby-1"
	)
	seedPreplacedIdentity(t, db, ns.ID, preplacedID, serverID, "")

	// 真 agent 注册：serverId 已被预置壳占用 → 落 pending 且带占用冲突原因（既有行为不变）。
	reg, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: realAgentID, ServerID: serverID,
		Kind: model.ServerKindBackend, BootID: "boot-real",
	})
	if err != nil {
		t.Fatalf("真 agent 注册失败: %v", err)
	}
	if reg.Status != model.AgentIdentityStatusPending {
		t.Fatalf("真 agent 应先落 pending，实际 %s", reg.Status)
	}

	// 关键断言：**不带** ForceUnbindOccupier 也应确认成功（预置壳自动让位）。
	if _, err := svc.ApproveAgentIdentity(realAgentID, ApproveAgentIdentityParams{
		Operator: "admin", ServerID: serverID,
	}); err != nil {
		t.Fatalf("预置空壳应自动让位，无需强制解绑，实际失败: %v", err)
	}

	var preplaced model.AgentIdentity
	if err := db.Where("identity_id = ?", preplacedID).First(&preplaced).Error; err != nil {
		t.Fatalf("预置身份应仍保留历史行: %v", err)
	}
	if preplaced.Status != model.AgentIdentityStatusUnbound {
		t.Fatalf("预置空壳应被自动解绑（unbound），实际 %s", preplaced.Status)
	}
	var approved model.AgentIdentity
	if err := db.Where("identity_id = ?", realAgentID).First(&approved).Error; err != nil {
		t.Fatalf("真 agent 身份应已落库: %v", err)
	}
	if approved.Status != model.AgentIdentityStatusActive {
		t.Fatalf("真 agent 应转 active，实际 %s", approved.Status)
	}
}

// TestFR235RealIdentityStillRequiresForceUnbind 安全边界：**真身份**之间的冲突仍必须人工强制解绑。
// 判据只对「machine_registered 且空 bootId」的预置壳放行；带 bootId 的真身份不得被自动顶掉。
func TestFR235RealIdentityStillRequiresForceUnbind(t *testing.T) {
	svc, db := newFr235TestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	const serverID = "lobby-1"
	// 先建一个**真身份**（有 bootId、来源非预置）：走正常注册 + 审批。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "33333333-3333-4333-8333-333333333333",
		ServerID: serverID, Kind: model.ServerKindBackend, BootID: "boot-old",
	}); err != nil {
		t.Fatalf("旧真身份注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity("33333333-3333-4333-8333-333333333333", ApproveAgentIdentityParams{
		Operator: "admin", ServerID: serverID,
	}); err != nil {
		t.Fatalf("旧真身份确认失败: %v", err)
	}
	// 确认它是真身份来源，而非被误标为预置。
	var old model.AgentIdentity
	if err := db.Where("identity_id = ?", "33333333-3333-4333-8333-333333333333").First(&old).Error; err != nil {
		t.Fatalf("读取旧身份失败: %v", err)
	}
	if old.BindingSource == model.AgentIdentityBindingSourceMachineRegistered {
		t.Fatalf("真 agent 身份不该被标为 machine_registered，实际 %s", old.BindingSource)
	}
	if old.BootID == "" {
		t.Fatal("真 agent 身份应带 bootId")
	}

	// 另一真身份抢占同一 serverId → 不强制解绑必须失败（防线未破）。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "44444444-4444-4444-8444-444444444444",
		ServerID: serverID, Kind: model.ServerKindBackend, BootID: "boot-new",
	}); err != nil {
		t.Fatalf("新身份抢占应进入 pending，实际失败: %v", err)
	}
	_, err = svc.ApproveAgentIdentity("44444444-4444-4444-8444-444444444444", ApproveAgentIdentityParams{
		Operator: "admin", ServerID: serverID,
	})
	if !errors.Is(err, apperr.ErrServerIDOccupied) {
		t.Fatalf("真身份冲突未强制解绑时应报 server_id_occupied，实际 %v", err)
	}
	// 强制解绑后应成功（既有能力保留）。
	if _, err := svc.ApproveAgentIdentity("44444444-4444-4444-8444-444444444444", ApproveAgentIdentityParams{
		Operator: "admin", ServerID: serverID, ForceUnbindOccupier: true,
	}); err != nil {
		t.Fatalf("强制解绑后应成功，实际 %v", err)
	}
	_ = ns
}

// TestFR235MachineRegisterMarksPreplacedSource 机器注册新建的身份应标记为 machine_registered 来源，
// 这是自动让位判据的第一半（第二半为 boot_id 为空）。
func TestFR235MachineRegisterMarksPreplacedSource(t *testing.T) {
	svc, db := machineRegisterTestStack(t, true)
	if _, err := svc.Register(machineRegisterV1Params("lobby-1", true)); err != nil {
		t.Fatalf("机器注册失败: %v", err)
	}
	var ident model.AgentIdentity
	if err := db.Where("server_id = ?", "lobby-1").First(&ident).Error; err != nil {
		t.Fatalf("读取机器注册身份失败: %v", err)
	}
	if ident.BindingSource != model.AgentIdentityBindingSourceMachineRegistered {
		t.Fatalf("机器注册身份应标记为 machine_registered，实际 %q", ident.BindingSource)
	}
	if ident.BootID != "" {
		t.Fatalf("机器注册身份不应带 bootId（判据依赖此特征），实际 %q", ident.BootID)
	}
	if !isPreplacedOccupier(&ident) {
		t.Fatal("该身份应被 isPreplacedOccupier 判为预置壳")
	}
}

// TestFR235IsPreplacedOccupierRequiresBothSignals 判别式必须两个信号同时成立：
// 只有来源正确但带 bootId（真 agent 用过的行被误标）不放行；
// 只有 bootId 为空但来源非预置（历史未迁移行）也不放行——宁可要求人工，不可误伤真身份。
func TestFR235IsPreplacedOccupierRequiresBothSignals(t *testing.T) {
	cases := []struct {
		name   string
		ident  model.AgentIdentity
		expect bool
	}{
		{"预置壳（来源预置 + 空 bootId）", model.AgentIdentity{
			BindingSource: model.AgentIdentityBindingSourceMachineRegistered, BootID: "",
		}, true},
		{"来源预置但有 bootId（已被真 agent 用过）", model.AgentIdentity{
			BindingSource: model.AgentIdentityBindingSourceMachineRegistered, BootID: "boot-x",
		}, false},
		{"空 bootId 但来源为 admin_assigned（历史未迁移行）", model.AgentIdentity{
			BindingSource: model.AgentIdentityBindingSourceAdminAssigned, BootID: "",
		}, false},
		{"空 bootId 但来源为 legacy_local", model.AgentIdentity{
			BindingSource: model.AgentIdentityBindingSourceLegacyLocal, BootID: "",
		}, false},
		{"真身份（其他来源 + 有 bootId）", model.AgentIdentity{
			BindingSource: model.AgentIdentityBindingSourceLegacyLocal, BootID: "boot-y",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPreplacedOccupier(&tc.ident); got != tc.expect {
				t.Fatalf("isPreplacedOccupier = %v，期望 %v", got, tc.expect)
			}
		})
	}
}

package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// FR-222 机器注册通道（见 docs/specs/internal-trust-channel.md）：
// 分支依据是「中间件判定的调用方类型 + 部署开关」，二者都不取自请求体。
//
// 落点说明（真机验证后确定）：共享 token 只到达 v1 数据面 `/beacon/v1/agent/register`（该组挂了
// agentTokenMiddleware），故机器注册的分支落在 InstanceService.Register（v1 路径），
// 由它负责把身份直落 active 并绑定 serverId（v1 路径此前完全不触碰 agent_identity 表）。
//
// 本组用例锁定四条不变量：
//  1. 开关关闭 → 携共享 token 的注册行为与既有分权设计逐字一致（不建身份行），仅留「已提交待审批」审计
//  2. 开关开启 + 受信调用方 → 直落 active 并绑定 serverId + 建未分配 server 行
//  3. 非受信调用方（agent 自持身份）即使开关开启也不受影响（不可伪造调用方类型）
//  4. 机器注册意图一律写 identity.machine_registered 审计，含 serverId / lastAddr / 来源 IP

// machineRegisterTestStack 装配机器注册测试栈（内存 sqlite + 注册表 + 审计仓库）。
func machineRegisterTestStack(t *testing.T, allowed bool) (*InstanceService, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.ServerOffline{},
		&model.ZoneAssignment{}, &model.AgentIdentity{}, &model.AgentEndpoint{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移机器注册表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	svc := NewInstanceService(db, runtime.NewRegistry(),
		repository.NewZoneAssignmentRepository(db), repository.NewServerOfflineRepository(db),
		repository.NewAuditLogRepository(db), 10*time.Second, 30*time.Second)
	svc.SetMachineRegisterAllowed(allowed)
	if err := db.Create(&model.Namespace{Code: "prod", Name: "生产"}).Error; err != nil {
		t.Fatalf("建测试 namespace 失败: %v", err)
	}
	return svc, db
}

// machineRegisterV1Params 组装一次 v1 机器注册入参。
func machineRegisterV1Params(serverID string, trusted bool) RegisterParams {
	return RegisterParams{
		Namespace: "prod", ServerID: serverID, Role: "bukkit", GroupHint: "area1",
		Address: "10.0.0.7:25565", ClientIP: "203.0.113.9", TrustedInternal: trusted,
	}
}

// loadMachineRegisterAudits 读取某身份的机器注册审计（按写入顺序）。
func loadMachineRegisterAudits(t *testing.T, db *gorm.DB, targetRef string) []model.AuditLog {
	t.Helper()
	var audits []model.AuditLog
	if err := db.Where("action = ? AND target_ref = ?", model.ActionIdentityMachineRegistered, targetRef).
		Order("id ASC").Find(&audits).Error; err != nil {
		t.Fatalf("读取机器注册审计失败: %v", err)
	}
	return audits
}

// TestMachineRegisterDisabledKeepsExistingBehavior 锁定验收 1/6：开关关闭时行为与现状一致（不建身份行），
// 仅留一条「已提交待审批」审计。
func TestMachineRegisterDisabledKeepsExistingBehavior(t *testing.T) {
	svc, db := machineRegisterTestStack(t, false)

	res, err := svc.Register(machineRegisterV1Params("lobby-1", true))
	if err != nil {
		t.Fatalf("开关关闭时注册应成功: %v", err)
	}
	if res.MachineRegister != nil {
		t.Fatalf("开关关闭时不得走机器注册通道，实际 %+v", res.MachineRegister)
	}
	// 既有语义：v1 注册不创建 agent_identity 行（身份仍由 v2 注册落 pending 待审批）。
	var count int64
	if err := db.Model(&model.AgentIdentity{}).Count(&count).Error; err != nil {
		t.Fatalf("统计身份行失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("开关关闭时不得创建身份行（分权设计不破），实际 %d 行", count)
	}

	audits := loadMachineRegisterAudits(t, db, "lobby-1")
	if len(audits) != 1 {
		t.Fatalf("机器注册意图应恰好 1 条审计，实际 %d", len(audits))
	}
	assertMachineRegisterAudit(t, audits[0], machineRegisterOutcomePending, "lobby-1", "10.0.0.7:25565", "203.0.113.9")
}

// TestMachineRegisterEnabledActivatesAndBinds 锁定验收 2/5：开关开启 + 受信调用方 → 身份直落 active 并绑定 serverId。
func TestMachineRegisterEnabledActivatesAndBinds(t *testing.T) {
	svc, db := machineRegisterTestStack(t, true)

	res, err := svc.Register(machineRegisterV1Params("lobby-1", true))
	if err != nil {
		t.Fatalf("开关开启时机器注册应成功: %v", err)
	}
	if res.MachineRegister == nil {
		t.Fatal("机器注册应回带权威绑定事实")
	}
	if res.MachineRegister.ServerID != "lobby-1" || res.MachineRegister.IdentityID == "" {
		t.Fatalf("机器注册应绑定 serverId 并生成 identityId，实际 %+v", res.MachineRegister)
	}

	var ident model.AgentIdentity
	if err := db.Where("server_id = ?", "lobby-1").First(&ident).Error; err != nil {
		t.Fatalf("机器注册应创建身份行: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusActive || ident.BoundAt == nil {
		t.Fatalf("身份应直落 active 并写 boundAt，实际 %+v", ident)
	}
	if ident.PendingExpiresAt != nil {
		t.Fatalf("active 身份不应有 pending 过期时间，实际 %v", ident.PendingExpiresAt)
	}
	if ident.BindingSource != model.AgentIdentityBindingSourceAdminAssigned {
		t.Fatalf("机器注册来源应为 admin_assigned，实际 %q", ident.BindingSource)
	}
	if ident.LastAddr != "10.0.0.7:25565" {
		t.Fatalf("身份应记录上报地址，实际 %q", ident.LastAddr)
	}

	// 未分配的 server 行应就绪（分配 / 换区仍走各自审批，故归属字段必须为空）。
	var server model.Server
	if err := db.Where("server_id = ?", "lobby-1").First(&server).Error; err != nil {
		t.Fatalf("机器注册应创建 server 行: %v", err)
	}
	if server.ZoneID != nil || server.BCClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("机器注册不得顺手落区 / 设默认入口，实际 %+v", server)
	}

	audits := loadMachineRegisterAudits(t, db, ident.IdentityID)
	if len(audits) != 1 {
		t.Fatalf("机器注册应恰好 1 条审计，实际 %d", len(audits))
	}
	assertMachineRegisterAudit(t, audits[0], machineRegisterOutcomeActive, "lobby-1", "10.0.0.7:25565", "203.0.113.9")
	// 常规实例注册审计照旧（既有语义不变）。
	var instanceAudits int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", model.ActionInstanceRegister).Count(&instanceAudits).Error; err != nil {
		t.Fatalf("统计实例注册审计失败: %v", err)
	}
	if instanceAudits != 1 {
		t.Fatalf("机器注册仍应保留 instance.register 审计，实际 %d 条", instanceAudits)
	}
}

// TestMachineRegisterIgnoresUntrustedCallers 锁定安全论证「伪造调用方类型」：
// 开关开启但请求非受信内部调用方时，注册不建身份行、不写机器注册审计。
func TestMachineRegisterIgnoresUntrustedCallers(t *testing.T) {
	svc, db := machineRegisterTestStack(t, true)

	res, err := svc.Register(machineRegisterV1Params("lobby-unt", false))
	if err != nil {
		t.Fatalf("非受信调用方注册应成功: %v", err)
	}
	if res.MachineRegister != nil {
		t.Fatalf("非受信调用方不得走机器注册通道，实际 %+v", res.MachineRegister)
	}
	var count int64
	if err := db.Model(&model.AgentIdentity{}).Count(&count).Error; err != nil {
		t.Fatalf("统计身份行失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("非受信调用方不得创建身份行，实际 %d 行", count)
	}
	var audits int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", model.ActionIdentityMachineRegistered).Count(&audits).Error; err != nil {
		t.Fatalf("统计机器注册审计失败: %v", err)
	}
	if audits != 0 {
		t.Fatalf("非受信调用方不应产生机器注册审计，实际 %d 条", audits)
	}
}

// TestMachineRegisterRequiresServerID 机器注册必须完成绑定：缺 serverId 时按身份缺失拒绝，不静默降级。
func TestMachineRegisterRequiresServerID(t *testing.T) {
	svc, _ := machineRegisterTestStack(t, true)

	params := machineRegisterV1Params("", true)
	if _, err := svc.Register(params); !errors.Is(err, apperr.ErrIdentityRequired) {
		t.Fatalf("缺 serverId 的机器注册应拒，实际 %v", err)
	}
}

// TestMachineRegisterIsIdempotentPerServerID 同一 serverId 重复机器注册应复用同一身份行（实例重启 / 重复推送幂等）。
func TestMachineRegisterIsIdempotentPerServerID(t *testing.T) {
	svc, db := machineRegisterTestStack(t, true)

	first, err := svc.Register(machineRegisterV1Params("lobby-1", true))
	if err != nil {
		t.Fatalf("首次机器注册失败: %v", err)
	}
	second, err := svc.Register(machineRegisterV1Params("lobby-1", true))
	if err != nil {
		t.Fatalf("重复机器注册失败: %v", err)
	}
	if first.MachineRegister.IdentityID != second.MachineRegister.IdentityID {
		t.Fatalf("重复注册应复用同一身份，实际 %q vs %q",
			first.MachineRegister.IdentityID, second.MachineRegister.IdentityID)
	}
	var count int64
	if err := db.Model(&model.AgentIdentity{}).Where("server_id = ?", "lobby-1").Count(&count).Error; err != nil {
		t.Fatalf("统计身份行失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复机器注册应复用同一身份行，实际 %d 行", count)
	}
	if audits := loadMachineRegisterAudits(t, db, first.MachineRegister.IdentityID); len(audits) != 2 {
		t.Fatalf("每次机器注册意图都应留审计，实际 %d 条", len(audits))
	}
}

// TestMachineRegisterNotReviveDisabledIdentity 已禁用身份不得被机器注册通道悄悄复活（禁用是止损动作）。
func TestMachineRegisterNotReviveDisabledIdentity(t *testing.T) {
	svc, db := machineRegisterTestStack(t, true)

	res, err := svc.Register(machineRegisterV1Params("lobby-1", true))
	if err != nil {
		t.Fatalf("首次机器注册失败: %v", err)
	}
	if err := db.Model(&model.AgentIdentity{}).Where("identity_id = ?", res.MachineRegister.IdentityID).
		Update("status", model.AgentIdentityStatusDisabled).Error; err != nil {
		t.Fatalf("置禁用态失败: %v", err)
	}

	if _, err := svc.Register(machineRegisterV1Params("lobby-1", true)); !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("已禁用身份不应被机器注册复活，实际 %v", err)
	}
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", res.MachineRegister.IdentityID).First(&ident).Error; err != nil {
		t.Fatalf("读取身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusDisabled {
		t.Fatalf("被拒的注册不得改动身份状态，实际 %s", ident.Status)
	}
}

// TestMachineRegisterRejectsUnknownNamespace 机器注册要写权威身份行，必须能解析命名空间归属。
func TestMachineRegisterRejectsUnknownNamespace(t *testing.T) {
	svc, _ := machineRegisterTestStack(t, true)

	params := machineRegisterV1Params("lobby-x", true)
	params.Namespace = "ghost"
	if _, err := svc.Register(params); !errors.Is(err, apperr.ErrNamespaceNotFound) {
		t.Fatalf("未知命名空间的机器注册应拒，实际 %v", err)
	}
}

// TestMachineRegisterRoleToKind 角色 → v2 kind 映射（与 agent 侧 v2Kind 同口径，供 server 行归属正确）。
func TestMachineRegisterRoleToKind(t *testing.T) {
	cases := map[string]string{
		"bukkit": "backend", "": "backend", "bungee": "proxy", "BUNGEE": "proxy", "velocity": "proxy",
	}
	for role, want := range cases {
		got, err := agentRoleToServerKind(role)
		if err != nil {
			t.Fatalf("角色 %q 映射失败: %v", role, err)
		}
		if got != want {
			t.Fatalf("角色 %q 应映射为 %q，实际 %q", role, want, got)
		}
	}
}

// assertMachineRegisterAudit 校验审计 detail 的四要素（outcome / serverId / lastAddr / 来源 IP）与落库口径。
func assertMachineRegisterAudit(t *testing.T, entry model.AuditLog, outcome, serverID, lastAddr, clientIP string) {
	t.Helper()
	for _, want := range []string{
		`"outcome":"` + outcome + `"`,
		`"serverId":"` + serverID + `"`,
		`"lastAddr":"` + lastAddr + `"`,
		`"clientIp":"` + clientIP + `"`,
	} {
		if !strings.Contains(entry.Detail, want) {
			t.Fatalf("审计 detail 应含 %s，实际 %q", want, entry.Detail)
		}
	}
	if entry.Operator != "system:machine-register" {
		t.Fatalf("审计操作者应为 system:machine-register，实际 %q", entry.Operator)
	}
	if entry.TargetType != model.TargetTypeIdentity {
		t.Fatalf("审计目标类型应为 %s，实际 %s", model.TargetTypeIdentity, entry.TargetType)
	}
	if entry.Result != model.ResultOK {
		t.Fatalf("审计结果应为 ok，实际 %s", entry.Result)
	}
	if entry.NamespaceCode != "prod" {
		t.Fatalf("审计应带 namespace code，实际 %q", entry.NamespaceCode)
	}
	if entry.ClientIP != clientIP {
		t.Fatalf("审计 clientIp 应为 %q，实际 %q", clientIP, entry.ClientIP)
	}
}

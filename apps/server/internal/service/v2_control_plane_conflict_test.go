package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/bootwatch"
)

// spyAlertSink 是告警留痕替身，记录被落库的告警事件供断言。
type spyAlertSink struct {
	events []*model.AlertEvent
}

func (s *spyAlertSink) Record(e *model.AlertEvent) error {
	s.events = append(s.events, e)
	return nil
}

const conflictTestIdentity = "11111111-1111-4111-8111-111111111111"

// newConflictTestService 构造装配了冲突检测（固定 10 分钟窗口 + 告警替身）的 v2 服务。
func newConflictTestService(t *testing.T) (*gorm.DB, *V2ControlPlaneService, *spyAlertSink) {
	t.Helper()
	db, svc := newV2ControlPlaneTestService(t)
	spy := &spyAlertSink{}
	svc.SetConflictWatch(bootwatch.New(), func() time.Duration { return 10 * time.Minute }, spy)
	return db, svc, spy
}

// registerApproveActive 注册并确认一个 active 身份（前置装配）。
func registerApproveActive(t *testing.T, svc *V2ControlPlaneService, token, serverID, boot string) {
	t.Helper()
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: serverID,
		Kind: model.ServerKindBackend, BootID: boot, Addr: "10.0.0.1:25565",
	}); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(conflictTestIdentity, ApproveAgentIdentityParams{Operator: "admin"}); err != nil {
		t.Fatalf("确认身份失败: %v", err)
	}
}

func conflictTestNamespace(t *testing.T, svc *V2ControlPlaneService) string {
	t.Helper()
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	return token
}

// TestOneWaySwitchDoesNotFlagConflict 是关键回归：故障换机（同 identityId/serverId，新 boot 新址）不误判冲突（spec §4.6）。
func TestOneWaySwitchDoesNotFlagConflict(t *testing.T) {
	db, svc, spy := newConflictTestService(t)
	token := conflictTestNamespace(t, svc)
	registerApproveActive(t, svc, token, "lobby-1", "boot-A")

	// 故障换机：数据目录迁到新机，新进程新 bootId、新地址注册 → Q1 直接恢复 active，不判冲突。
	res, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-B", Addr: "10.0.0.2:25565",
	})
	if err != nil {
		t.Fatalf("换机注册应成功恢复，实际错误: %v", err)
	}
	if res.Status != model.AgentIdentityStatusActive {
		t.Fatalf("换机注册应恢复 active，实际 %s", res.Status)
	}

	// 旧机 A 已死，仅有一次在途拖尾上报到达 → 判陈旧 404 促重注册；但死机不会真重注册，故绝不触发冲突（spec §4.5/§4.6）。
	if _, err := svc.AuthenticateAgentReport(token, conflictTestIdentity, "boot-A", "10.0.0.1:25565"); !errors.Is(err, apperr.ErrAgentStaleReregister) {
		t.Fatalf("拖尾上报应判陈旧 404 促重注册，实际 %v", err)
	}
	// 幸存者 B 上报正常。
	if _, err := svc.AuthenticateAgentReport(token, conflictTestIdentity, "boot-B", "10.0.0.2:25565"); err != nil {
		t.Fatalf("幸存者 B 上报应正常，实际 %v", err)
	}

	// 身份仍为 active，无冲突、无告警。
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", conflictTestIdentity).First(&ident).Error; err != nil {
		t.Fatalf("查身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusActive {
		t.Fatalf("单向切换后身份应仍为 active，实际 %s", ident.Status)
	}
	if len(spy.events) != 0 {
		t.Fatalf("单向切换不应产生告警，实际 %d 条", len(spy.events))
	}
}

// TestAlternatingBootTransitionsToConflict 验证并发双实例往复（A→B→A 再注册）转 conflict（T12）：
// 写 conflict_reason/conflict_peers + system 审计 + 告警。
func TestAlternatingBootTransitionsToConflict(t *testing.T) {
	db, svc, spy := newConflictTestService(t)
	token := conflictTestNamespace(t, svc)
	registerApproveActive(t, svc, token, "lobby-1", "boot-A")

	// 副本 B 注册顶替（Q1 刷新 active）。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-B", Addr: "10.0.0.2:25565",
	}); err != nil {
		t.Fatalf("副本 B 注册失败: %v", err)
	}
	// 被顶替的 A 重新注册（真实系统里由数据面陈旧 404 促其重注册）→ 往复 → 冲突。
	_, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-A", Addr: "10.0.0.1:25565",
	})
	if !errors.Is(err, apperr.ErrIdentityConflict) {
		t.Fatalf("往复注册应返回冲突 409，实际 %v", err)
	}

	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", conflictTestIdentity).First(&ident).Error; err != nil {
		t.Fatalf("查身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusConflict {
		t.Fatalf("应转 conflict，实际 %s", ident.Status)
	}
	if ident.ConflictReason != "duplicate-boot-id" {
		t.Fatalf("conflict_reason 应为 duplicate-boot-id，实际 %q", ident.ConflictReason)
	}
	peers := ParseConflictPeers(ident.ConflictPeers)
	if len(peers) < 2 {
		t.Fatalf("conflict_peers 应含 ≥2 个 boot，实际 %d", len(peers))
	}
	// 告警：一条 identity-conflict。
	if len(spy.events) != 1 || spy.events[0].Type != model.AlertEventTypeIdentityConflict {
		t.Fatalf("应产生 1 条 identity-conflict 告警，实际 %+v", spy.events)
	}
	// 审计：system 操作者的 identity.conflict_detected。
	var count int64
	db.Model(&model.AuditLog{}).Where("action = ? AND operator = ?", model.ActionIdentityConflict, "system").Count(&count)
	if count != 1 {
		t.Fatalf("应写 1 条 conflict_detected(system) 审计，实际 %d", count)
	}
}

// TestStaleReportPromptsReregisterThenConflict 验证 FR-177 真机缺口修复（stale→404 促重注册喂养往复）：
// 活着的旧实例被顶替后，其数据面上报判陈旧 404（而非旧的 401），agent 据此走既有「404→重注册」路径重注册 →
// 往复 → 转 conflict。串起真机端到端链路，让并发双实例被及时检测（旧的 401 agent 不识别、只重试同 boot 不重注册）。
func TestStaleReportPromptsReregisterThenConflict(t *testing.T) {
	db, svc, spy := newConflictTestService(t)
	token := conflictTestNamespace(t, svc)
	registerApproveActive(t, svc, token, "lobby-1", "boot-A") // A active，current=A

	// 副本 B 注册顶替（Q1 刷新 active，current=B）。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-B", Addr: "10.0.0.2:25565",
	}); err != nil {
		t.Fatalf("副本 B 注册失败: %v", err)
	}

	// 活着的 A 持续上报 boot-A（≠current B）→ 判陈旧 404 促重注册（修复核心：非旧的静态 401）。
	if _, err := svc.AuthenticateAgentReport(token, conflictTestIdentity, "boot-A", "10.0.0.1:25565"); !errors.Is(err, apperr.ErrAgentStaleReregister) {
		t.Fatalf("活实例陈旧上报应判 404 促重注册，实际 %v", err)
	}

	// A 收 404 后重注册（agent 既有「404→重注册」）→ A 曾被顶替（wasCurrent）→ 往复 → conflict。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-A", Addr: "10.0.0.1:25565",
	}); !errors.Is(err, apperr.ErrIdentityConflict) {
		t.Fatalf("重注册往复应转 conflict，实际 %v", err)
	}

	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", conflictTestIdentity).First(&ident).Error; err != nil {
		t.Fatalf("查身份失败: %v", err)
	}
	if ident.Status != model.AgentIdentityStatusConflict {
		t.Fatalf("应转 conflict，实际 %s", ident.Status)
	}
	if len(spy.events) != 1 || spy.events[0].Type != model.AlertEventTypeIdentityConflict {
		t.Fatalf("应产生 1 条 identity-conflict 告警，实际 %+v", spy.events)
	}
}

// setupConflict 造出一个处于 conflict 态的身份，返回 token 与冲突双方 boot。
func setupConflict(t *testing.T, svc *V2ControlPlaneService) string {
	t.Helper()
	token := conflictTestNamespace(t, svc)
	registerApproveActive(t, svc, token, "lobby-1", "boot-A")
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-B", Addr: "10.0.0.2:25565",
	}); err != nil {
		t.Fatalf("副本注册失败: %v", err)
	}
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-A", Addr: "10.0.0.1:25565",
	}); !errors.Is(err, apperr.ErrIdentityConflict) {
		t.Fatalf("前置：应先进入冲突，实际 %v", err)
	}
	return token
}

// TestResolveConflictNonConflictIs409 验证对非 conflict（active）身份处置返回 409 illegal_state。
func TestResolveConflictNonConflictIs409(t *testing.T) {
	_, svc, _ := newConflictTestService(t)
	token := conflictTestNamespace(t, svc)
	registerApproveActive(t, svc, token, "lobby-1", "boot-A")

	if _, err := svc.ResolveAgentIdentityConflict(conflictTestIdentity, ResolveConflictParams{
		KeepBootID: "boot-A", Reason: "保留主实例", Operator: "admin",
	}); !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("非 conflict 处置应 409 illegal_state，实际 %v", err)
	}
}

// TestResolveConflictKeepBootInvalidAndReason 验证 conflict 态下 keepBootId 不在双方 → 400、缺原因 → 400。
func TestResolveConflictKeepBootInvalidAndReason(t *testing.T) {
	_, svc, _ := newConflictTestService(t)
	setupConflict(t, svc)

	if _, err := svc.ResolveAgentIdentityConflict(conflictTestIdentity, ResolveConflictParams{
		KeepBootID: "boot-X", Reason: "保留", Operator: "admin",
	}); !errors.Is(err, apperr.ErrConflictKeepBootInvalid) {
		t.Fatalf("keepBootId 不在双方应 400 boot_id_not_in_conflict，实际 %v", err)
	}
	if _, err := svc.ResolveAgentIdentityConflict(conflictTestIdentity, ResolveConflictParams{
		KeepBootID: "boot-A", Reason: "", Operator: "admin",
	}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺原因应 400，实际 %v", err)
	}
}

// TestResolveConflictKeepsWinnerLoserStays409 验证处置保留一方后：保留方恢复 active、可上报；落败方后续持续 409。
func TestResolveConflictKeepsWinnerLoserStays409(t *testing.T) {
	db, svc, _ := newConflictTestService(t)
	token := setupConflict(t, svc)

	ident, err := svc.ResolveAgentIdentityConflict(conflictTestIdentity, ResolveConflictParams{
		KeepBootID: "boot-A", Reason: "保留原主实例，下线副本", Operator: "admin", ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("处置应成功，实际 %v", err)
	}
	if ident.Status != model.AgentIdentityStatusActive || ident.BootID != "boot-A" {
		t.Fatalf("处置后应 active 且 boot_id=boot-A，实际 %s/%s", ident.Status, ident.BootID)
	}
	if ident.ConflictReason != "" || ident.ConflictPeers != "" {
		t.Fatalf("处置后应清空冲突字段，实际 reason=%q peers=%q", ident.ConflictReason, ident.ConflictPeers)
	}

	// 保留方 A 上报正常。
	if _, err := svc.AuthenticateAgentReport(token, conflictTestIdentity, "boot-A", "10.0.0.1:25565"); err != nil {
		t.Fatalf("保留方 A 上报应正常，实际 %v", err)
	}
	// 落败方 B 上报持续 409（附指引）。
	if _, err := svc.AuthenticateAgentReport(token, conflictTestIdentity, "boot-B", "10.0.0.2:25565"); !errors.Is(err, apperr.ErrIdentityConflictLoser) {
		t.Fatalf("落败方 B 上报应持续 409 指引，实际 %v", err)
	}
	// 落败方 B 以同 boot 重新注册被拒（持续 409）。
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: conflictTestIdentity, ServerID: "lobby-1",
		Kind: model.ServerKindBackend, BootID: "boot-B", Addr: "10.0.0.2:25565",
	}); !errors.Is(err, apperr.ErrIdentityConflictLoser) {
		t.Fatalf("落败方 B 重新注册应被拒 409，实际 %v", err)
	}

	// 审计：一条 identity.conflict_resolved。
	var count int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionIdentityConflictResolve).Count(&count)
	if count != 1 {
		t.Fatalf("应写 1 条 conflict_resolved 审计，实际 %d", count)
	}
}

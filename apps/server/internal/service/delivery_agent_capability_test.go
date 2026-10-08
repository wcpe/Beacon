package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// —— FR-264：agent 交付能力版本守卫（落实 ADR-0069 L58）——

// deliveryTestMinAgentVersion 是测试中显式开启能力守卫所用的下限（FR-264）：
// 出厂默认为空串（不校验，避免真机上报串未核对时全量拒服），故测试必须给一个确定值才能验证守卫行为。
const deliveryTestMinAgentVersion = "0.29.0"

// TestDeliveryGuardDisabledByDefault 出厂默认（空下限）即关闭守卫——
// 真机 agent 上报串形态尚未核对，守卫又是 fail-closed 的，拍猜测值作默认等于上线即全量拒服。
func TestDeliveryGuardDisabledByDefault(t *testing.T) {
	if deliveryDefaultMinAgentVersion != "" {
		t.Fatalf("出厂默认下限应为空串（显式配置才启用守卫），实际 %q", deliveryDefaultMinAgentVersion)
	}
	// 守卫对空下限一律放行（含未上报版本的旧 agent）。
	if !deliveryAgentSupportsStreaming("", "") {
		t.Fatal("下限为空时未上报版本的 agent 也应放行")
	}
}

// TestDeliveryAgentSupportsStreaming 版本下限判定纯函数（空版本 / 旧版本一律不支持，空下限即不校验）。
func TestDeliveryAgentSupportsStreaming(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		min      string
		want     bool
	}{
		{"空版本视为旧 agent 不支持", "", "0.29.0", false},
		{"低于下限不支持", "0.28.9", "0.29.0", false},
		{"等于下限支持", "0.29.0", "0.29.0", true},
		{"高于下限支持", "1.4.0", "0.29.0", true},
		{"段数不足补零比较", "1.4", "1.4.0", true},
		{"预发布后缀不影响主版本判定", "1.4.0-rc.1", "0.29.0", true},
		{"次版本小也不支持", "0.29.0", "0.30.0", false},
		{"下限为空即不校验", "", "", true},
		{"下限为空时旧版本也放行", "0.1.0", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deliveryAgentSupportsStreaming(c.version, c.min); got != c.want {
				t.Fatalf("deliveryAgentSupportsStreaming(%q,%q)=%v，期望 %v", c.version, c.min, got, c.want)
			}
		})
	}
}

// TestDeliveryGuardAgentCapability 启动守卫：目标或模板源不支持流式交付时整单拒绝启动、一条命令都不建。
func TestDeliveryGuardAgentCapability(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 100)
	// 全部目标与模板源都给一个低于下限的旧版本（fixture 默认没上报版本 → 空串亦按不支持处理）。
	h.seedAgentVersions(t, map[string]string{"t-1": "0.28.0", "t-2": "0.28.0", "src-1": "0.28.0"})

	_, startErr := h.orch.applyStart(order.ID, "上线", "ops", "10.0.0.1")
	if startErr == nil {
		t.Fatal("旧 agent 目标应拒绝启动，实际通过了")
	}
	// 错误码与形态一并锁定（mustAppErr 只校验码与状态，原因可读性另断言）。
	ae := mustAppErr(t, startErr, apperr.ErrDeliveryAgentCapabilityUnsupported.Code, http.StatusConflict)
	if !containsAll(ae.Message, "t-1", "t-2", "src-1") {
		t.Fatalf("拒绝原因应点名全部不合格目标，实际 %q", ae.Message)
	}

	// 拒绝后不得留下任何交付命令（不下发即不留账）。
	var cmdCount int64
	h.env.db.Model(&model.AgentCommand{}).Where("type LIKE ?", "delivery_%").Count(&cmdCount)
	if cmdCount != 0 {
		t.Fatalf("被拒启动不应建任何交付命令，实际 %d 条", cmdCount)
	}
}

// TestDeliveryGuardAgentCapabilityAllowsNewAgent 新版 agent 行为完全不变：守卫放行、命令照常下发。
func TestDeliveryGuardAgentCapabilityAllowsNewAgent(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 100)
	h.seedAgentVersions(t, map[string]string{"t-1": "1.4.0", "t-2": "1.4.0", "src-1": "1.4.0"})

	if _, err := h.orch.applyStart(order.ID, "上线", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("新版 agent 应正常启动，实际 %v", err)
	}
	h.tick() // 推送命令由推进器下发（启动本身只固化批次与目标）
	var cmdCount int64
	h.env.db.Model(&model.AgentCommand{}).Where("type = ?", model.CommandTypeDeliveryPush).Count(&cmdCount)
	if cmdCount != 2 {
		t.Fatalf("新版 agent 应正常下发 2 条推送命令，实际 %d 条", cmdCount)
	}
	// 两台目标都应被推进到 pushing（未被守卫误伤——守卫若误判会置 failed）。
	statuses := h.targetStatuses(order.ID)
	if statuses[model.ChangeTargetStatusPushing] != 2 {
		t.Fatalf("新版 agent 的 2 台目标都应 pushing，实际 %+v", statuses)
	}
}

// TestDeliveryGuardAgentCapabilitySkipsTarget 下发守卫：批内单台旧 agent 被拒且原因可读，其余照常推进。
func TestDeliveryGuardAgentCapabilitySkipsTarget(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 100)
	// 先按新版放行启动，再把 t-2 压成旧版本，使下发起的守卫命中单台。
	h.seedAgentVersions(t, map[string]string{"t-1": "1.4.0", "t-2": "1.4.0", "src-1": "1.4.0"})
	if _, err := h.orch.applyStart(order.ID, "上线", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.seedAgentVersions(t, map[string]string{"t-2": "0.20.0"})
	h.tick()

	var t2 model.ChangeTarget
	if err := h.env.db.Where("order_id = ? AND server_id = ?", order.ID, "t-2").First(&t2).Error; err != nil {
		t.Fatalf("查 t-2 目标失败: %v", err)
	}
	if t2.Status != model.ChangeTargetStatusFailed {
		t.Fatalf("旧 agent 目标应被置 failed，实际 %s", t2.Status)
	}
	if t2.Error == "" {
		t.Fatal("被拒目标必须给出可读原因，实际为空")
	}
	// 被拒目标不得收到命令。
	var cmdCount int64
	h.env.db.Model(&model.AgentCommand{}).Where("server_id = ? AND type = ?", "t-2", model.CommandTypeDeliveryPush).Count(&cmdCount)
	if cmdCount != 0 {
		t.Fatalf("被拒目标不应收到推送命令，实际 %d 条", cmdCount)
	}
	// 合格目标不受牵连。
	var t1 model.ChangeTarget
	if err := h.env.db.Where("order_id = ? AND server_id = ?", order.ID, "t-1").First(&t1).Error; err != nil {
		t.Fatalf("查 t-1 目标失败: %v", err)
	}
	if t1.Status == model.ChangeTargetStatusFailed {
		t.Fatalf("合格目标不应被牵连失败，实际 error=%q", t1.Error)
	}
}

// TestDeliveryGuardAgentCapabilityMinVersionSettingOff 下限设为空串即关闭守卫（运维逃生口）。
func TestDeliveryGuardAgentCapabilityMinVersionSettingOff(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 100)
	h.seedAgentVersions(t, map[string]string{"t-1": "", "t-2": "", "src-1": ""})
	h.env.settings.Set(SettingDeliveryMinAgentVersion, "")

	if _, err := h.orch.applyStart(order.ID, "上线", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("守卫关闭时未上报版本的 agent 也应放行，实际 %v", err)
	}
}

// seedAgentVersions 覆盖写入 agent 上报版本（fixture 已建身份行则更新，未建则补建 active 身份）。
func (h *orchestratorHarness) seedAgentVersions(t *testing.T, versions map[string]string) {
	t.Helper()
	for serverID, version := range versions {
		row := model.AgentIdentity{
			IdentityID: "idn-" + serverID, NamespaceID: h.f.nsID,
			ServerID: model.NullableServerID(serverID), Kind: model.ServerKindBackend,
			Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC(),
		}
		if err := h.env.db.Where("namespace_id = ? AND server_id = ?", h.f.nsID, serverID).
			Assign(model.AgentIdentity{AgentVersion: version}).FirstOrCreate(&row).Error; err != nil {
			t.Fatalf("写入 agent 版本失败: %v", err)
		}
	}
}

// TestDeliveryCapabilityRejectReasonReadable 被拒原因必须含实际版本与最低要求（运维看得懂、可处置）。
func TestDeliveryCapabilityRejectReasonReadable(t *testing.T) {
	reason := deliveryCapabilityRejectReason("0.28.0", "0.29.0")
	if !containsAll(reason, "0.28.0", "0.29.0") {
		t.Fatalf("拒绝原因应含实际版本与最低要求，实际 %q", reason)
	}
	// 未上报版本不得显示空串——那与「版本就是空」无从区分，运维看不懂。
	missing := deliveryCapabilityRejectReason("", "0.29.0")
	if !containsAll(missing, "未提供", "0.29.0") {
		t.Fatalf("未上报版本的原因应以「未提供」表述，实际 %q", missing)
	}
}

// containsAll 判定 s 是否同时包含全部子串（测试断言小工具）。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

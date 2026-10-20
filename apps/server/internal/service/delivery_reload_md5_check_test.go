package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// —— 交付生效回执「回执成功但版本不符」判定（FR-273 收尾）——
//
// 控制面独立重算期望摘要，与插件自报 appliedMd5 比对：不一致即判 failed（不得留 activated），
// 原因显式写清是哪个插件、自报什么、期望什么；插件已报失败 / 未自报 / 期望值不可得三种情况一律不叠加。

// prepareConfigHotReloadActivatingOrder 建一张含 1 个配置工件的 hot_reload 单并推进到 activating（单目标 t-1）。
// 返回 harness、单 id 与**按 agent 同款算法手工重算**的期望摘要（不依赖被测函数，避免自证）。
func prepareConfigHotReloadActivatingOrder(t *testing.T) (*orchestratorHarness, uint, string) {
	t.Helper()
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	orderID := h.createApprovedConfigOrder(t, "配置热更回执核对", []string{"t-1"},
		model.ConfigScopeZone, h.f.zone1ID, versionID)
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", orderID).Updates(map[string]any{
		"activation_method": model.ActivationMethodHotReload, "batch_sizes": encodeBatchSizes([]int{100}),
		"activate_timeout_sec": 60, "observe_window_sec": 5,
	}).Error; err != nil {
		t.Fatalf("置单为 hot_reload 失败: %v", err)
	}
	if _, err := h.orch.applyStart(orderID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动配置单失败: %v", err)
	}
	h.tick() // payload 就绪 + 首批 running
	h.completeAllPushes(t, orderID)
	h.tick() // 推送落定 → 下发生效命令 → activating
	if got := h.targetStatuses(orderID)[model.ChangeTargetStatusActivating]; got != 1 {
		t.Fatalf("hot_reload 推送落定后应有 1 个 activating 目标，实际 %d", got)
	}

	var arts []model.DeliveryConfigArtifact
	if err := h.env.db.Where("order_id = ? AND server_id = ?", orderID, "t-1").Find(&arts).Error; err != nil {
		t.Fatalf("查配置工件失败: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("夹具应有 1 个配置工件，实际 %d", len(arts))
	}
	expected := merge.MD5Hex(arts[0].Path + "\n" + arts[0].SHA256 + "\n")
	return h, orderID, expected
}

// activateResultWithReload 组装生效阶段成功回执（带 reload 段），插件条目按 (listener, status, appliedMd5) 传入。
func activateResultWithReload(plugins [][3]string) string {
	entries := make([]string, 0, len(plugins))
	for _, p := range plugins {
		entries = append(entries, fmt.Sprintf(`{"listener":%q,"status":%q,"appliedMd5":%q}`, p[0], p[1], p[2]))
	}
	return fmt.Sprintf(`{"changedFileCount":1,"skippedFileCount":0,"backupPresent":true,`+
		`"reload":{"total":%d,"okCount":%d,"failedCount":0,"timeoutMs":30000,"plugins":[%s]}}`,
		len(plugins), len(plugins), strings.Join(entries, ","))
}

// otherMD5 造一个与期望值不同的 32 位 md5（模拟插件实际加载了旧内容）。
func otherMD5(expected string) string {
	if strings.HasPrefix(expected, "0") {
		return strings.Repeat("1", 32)
	}
	return strings.Repeat("0", 32)
}

// targetByServer 取某单指定目标的库内行。
func targetByServer(t *testing.T, h *orchestratorHarness, orderID uint, serverID string) model.ChangeTarget {
	t.Helper()
	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	if err != nil {
		t.Fatalf("查目标失败: %v", err)
	}
	for _, tg := range targets {
		if tg.ServerID == serverID {
			return tg
		}
	}
	t.Fatalf("未找到目标 %s", serverID)
	return model.ChangeTarget{}
}

// TestOrchestratorHotReloadMD5MatchKeepsActivated 自报 md5 与期望一致 → 保持 activated。
func TestOrchestratorHotReloadMD5MatchKeepsActivated(t *testing.T) {
	h, orderID, expected := prepareConfigHotReloadActivatingOrder(t)
	h.completeAllActivatesWithResult(t, orderID, model.CommandStatusDone, activateResultWithReload([][3]string{
		{"top.wcpe.allin.bp.ReloadHook", "ok", expected},
	}))
	h.tick()

	tg := targetByServer(t, h, orderID, "t-1")
	if tg.Status != model.ChangeTargetStatusActivated {
		t.Fatalf("md5 相符应保持 activated，实际 %s（error=%s）", tg.Status, tg.Error)
	}
	if tg.Error != "" {
		t.Fatalf("相符不得写失败原因，实际 %s", tg.Error)
	}
	if tg.ReloadReport == "" {
		t.Fatal("成功回执也要落 reload_report（明细可见）")
	}
}

// TestOrchestratorHotReloadMD5MismatchFailsTarget 自报 md5 与期望不一致 → failed，原因显式点明两个 md5。
func TestOrchestratorHotReloadMD5MismatchFailsTarget(t *testing.T) {
	h, orderID, expected := prepareConfigHotReloadActivatingOrder(t)
	applied := otherMD5(expected)
	h.completeAllActivatesWithResult(t, orderID, model.CommandStatusDone, activateResultWithReload([][3]string{
		{"top.wcpe.allin.bp.ReloadHook", "ok", applied},
	}))
	h.tick()

	tg := targetByServer(t, h, orderID, "t-1")
	if tg.Status != model.ChangeTargetStatusFailed {
		t.Fatalf("md5 不符应判 failed（不得留 activated），实际 %s", tg.Status)
	}
	if !strings.Contains(tg.Error, "回执成功但版本不符") {
		t.Fatalf("原因须点明「回执成功但版本不符」，实际 %s", tg.Error)
	}
	if !strings.Contains(tg.Error, "top.wcpe.allin.bp.ReloadHook") {
		t.Fatalf("原因须点明哪个插件，实际 %s", tg.Error)
	}
	// 两个 md5 的可辨识前缀都要在场：运维据此直接看出插件加载的是别的版本。
	if !strings.Contains(tg.Error, shortMd5(applied)) || !strings.Contains(tg.Error, shortMd5(expected)) {
		t.Fatalf("原因须同时含自报与期望 md5 片段，实际 %s", tg.Error)
	}
	if tg.ReloadReport == "" {
		t.Fatal("判 failed 仍须保留 reload_report（明细可见）")
	}
	if tg.ActivatedAt != nil {
		t.Fatal("判 failed 的目标不得留 activated_at")
	}
}

// TestOrchestratorHotReloadPluginFailedNotOverridden 插件已报 failed → 原因仍为插件给的 reason，不被版本不符覆盖。
//
// 覆盖场景：多插件并存时一个插件失败、另一个插件自报的 md5 也不符——控制面的结论必须是
// 「插件 X 失败：<插件原因>」，而不是「版本不符」，否则真实失败根因被掩盖。
func TestOrchestratorHotReloadPluginFailedNotOverridden(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodHotReload, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick()

	pluginReason := "schedule.json 解析失败：unexpected token at line 8"
	result := fmt.Sprintf(`{"changedFileCount":1,"skippedFileCount":0,"backupPresent":true,`+
		`"error":"文件已落盘（本单变更已生效于磁盘），仅配置变更通知失败：1/2 插件重载失败：%s",`+
		`"reload":{"total":2,"okCount":1,"failedCount":1,"timeoutMs":30000,"plugins":[`+
		`{"listener":"top.wcpe.allin.aie.ReloadHook","status":"failed","reason":%q},`+
		`{"listener":"top.wcpe.allin.bp.ReloadHook","status":"ok","appliedMd5":%q}]}}`,
		pluginReason, pluginReason, strings.Repeat("d", 32))
	h.completeAllActivatesWithResult(t, order.ID, model.CommandStatusFailed, result)
	h.tick()

	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("查目标失败: %v", err)
	}
	for i := range targets {
		if targets[i].Status != model.ChangeTargetStatusFailed {
			t.Fatalf("插件失败应判 failed，实际 %s", targets[i].Status)
		}
		if !strings.Contains(targets[i].Error, pluginReason) {
			t.Fatalf("失败原因须保留插件给出的 reason，实际 %s", targets[i].Error)
		}
		if strings.Contains(targets[i].Error, "回执成功但版本不符") {
			t.Fatalf("已报失败的插件不得被版本不符结论覆盖，实际 %s", targets[i].Error)
		}
		if targets[i].ReloadReport == "" {
			t.Fatal("失败目标仍须保留 reload_report（逐插件明细可见）")
		}
	}
}

// errMD5Source 固定返回错误的期望值来源（模拟核对本身故障）。
type errMD5Source struct{}

func (errMD5Source) ExpectedConfigArtifactMD5(uint, string) (string, error) {
	return "", errors.New("解析工件摘要失败（模拟）")
}

// TestOrchestratorHotReloadMD5ExpectedUnavailableDegrades 期望值不可得一律放行（核对不可用不阻断交付）。
//
// 三种不可得来源分别覆盖：工件缺失（纯文件单 / 工件未就绪）、来源查询失败、来源未装配。
func TestOrchestratorHotReloadMD5ExpectedUnavailableDegrades(t *testing.T) {
	cases := []struct {
		name  string
		break_ func(t *testing.T, h *orchestratorHarness, orderID uint)
	}{
		{name: "工件缺失", break_: func(t *testing.T, h *orchestratorHarness, orderID uint) {
			if err := h.env.db.Where("order_id = ?", orderID).Delete(&model.DeliveryConfigArtifact{}).Error; err != nil {
				t.Fatalf("删工件失败: %v", err)
			}
		}},
		{name: "来源查询失败", break_: func(t *testing.T, h *orchestratorHarness, _ uint) {
			h.orch.SetExpectedMD5Source(errMD5Source{})
		}},
		{name: "来源未装配", break_: func(t *testing.T, h *orchestratorHarness, _ uint) {
			h.orch.SetExpectedMD5Source(nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, orderID, expected := prepareConfigHotReloadActivatingOrder(t)
			tc.break_(t, h, orderID)
			// 插件自报一个与期望不符的 md5：期望值不可得时不得据此判失败。
			h.completeAllActivatesWithResult(t, orderID, model.CommandStatusDone, activateResultWithReload([][3]string{
				{"top.wcpe.allin.bp.ReloadHook", "ok", otherMD5(expected)},
			}))
			h.tick()

			tg := targetByServer(t, h, orderID, "t-1")
			if tg.Status != model.ChangeTargetStatusActivated {
				t.Fatalf("期望值不可得应放行（仍 activated），实际 %s（error=%s）", tg.Status, tg.Error)
			}
			if tg.ReloadReport == "" {
				t.Fatal("放行时仍须落 reload_report（明细可见）")
			}
		})
	}
}

// TestOrchestratorHotReloadMD5UnreportedSkipsCheck 插件未自报 appliedMd5 → 无从核对，不判不符。
func TestOrchestratorHotReloadMD5UnreportedSkipsCheck(t *testing.T) {
	h, orderID, _ := prepareConfigHotReloadActivatingOrder(t)
	h.completeAllActivatesWithResult(t, orderID, model.CommandStatusDone, activateResultWithReload([][3]string{
		{"top.wcpe.allin.bp.ReloadHook", "ok", ""},
	}))
	h.tick()

	if tg := targetByServer(t, h, orderID, "t-1"); tg.Status != model.ChangeTargetStatusActivated {
		t.Fatalf("未自报 appliedMd5 不得判不符，实际 %s（error=%s）", tg.Status, tg.Error)
	}
}

// TestReloadMD5MismatchReason 纯函数穷举：状态过滤、未自报、大小写、原因串形状与条数上限。
func TestReloadMD5MismatchReason(t *testing.T) {
	expected := strings.Repeat("a", 32)
	bad := strings.Repeat("b", 32)

	cases := []struct {
		name    string
		plugins []deliveryReloadPlugin
		want    bool
		wantSub []string
	}{
		{
			name:    "相符不判不符",
			plugins: []deliveryReloadPlugin{{Listener: "A", Status: "ok", AppliedMd5: expected}},
			want:    false,
		},
		{
			name:    "大小写不敏感视为相符（agent 产出小写，插件可能回显大写）",
			plugins: []deliveryReloadPlugin{{Listener: "A", Status: "ok", AppliedMd5: strings.ToUpper(expected)}},
			want:    false,
		},
		{
			name:    "未自报不判不符",
			plugins: []deliveryReloadPlugin{{Listener: "A", Status: "ok", AppliedMd5: ""}},
			want:    false,
		},
		{
			name:    "插件已报失败不叠加版本结论",
			plugins: []deliveryReloadPlugin{{Listener: "A", Status: "failed", Reason: "解析失败", AppliedMd5: bad}},
			want:    false,
		},
		{
			name:    "不符命中并点明插件与两个片段",
			plugins: []deliveryReloadPlugin{{Listener: "A", Status: "ok", AppliedMd5: bad}},
			want:    true,
			wantSub: []string{"回执成功但版本不符", "插件 A", shortMd5(bad), shortMd5(expected)},
		},
		{
			name: "超限只追加数量",
			plugins: []deliveryReloadPlugin{
				{Listener: "A", Status: "ok", AppliedMd5: bad}, {Listener: "B", Status: "ok", AppliedMd5: bad},
				{Listener: "C", Status: "ok", AppliedMd5: bad}, {Listener: "D", Status: "ok", AppliedMd5: bad},
				{Listener: "E", Status: "ok", AppliedMd5: bad}, {Listener: "F", Status: "ok", AppliedMd5: bad},
				{Listener: "G", Status: "ok", AppliedMd5: bad},
			},
			want:    true,
			wantSub: []string{"另有 2 个插件同样不符"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, hit := reloadMD5MismatchReason(tc.plugins, expected)
			if hit != tc.want {
				t.Fatalf("命中判定不符：期望 %v 实际 %v（reason=%s）", tc.want, hit, reason)
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(reason, sub) {
					t.Fatalf("原因缺少片段 %q，实际 %s", sub, reason)
				}
			}
			if hit && strings.Count(reason, "插件 ") > 5 {
				t.Fatalf("原因串须受条数上限约束，实际 %s", reason)
			}
		})
	}
}

// TestConfigArtifactMD5MirrorsAgentDigest 锁定期望摘要算法与 agent 侧 `configArtifactMd5` 逐字一致：
// 按 path 升序拼接 `path "\n" sha "\n"`、同 path 去重、无工件返回空串（= 期望值不可得）。
func TestConfigArtifactMD5MirrorsAgentDigest(t *testing.T) {
	arts := []model.DeliveryConfigArtifact{
		// 刻意逆序投放：结果必须与库内顺序无关（DB 排序受排序规则影响，不足以作对拍依据）。
		{Path: "plugins/B/config.yml", SHA256: strings.Repeat("2", 64)},
		{Path: "plugins/A/config.yml", SHA256: strings.Repeat("1", 64)},
	}
	want := merge.MD5Hex("plugins/A/config.yml\n" + strings.Repeat("1", 64) + "\n" +
		"plugins/B/config.yml\n" + strings.Repeat("2", 64) + "\n")
	if got := configArtifactMD5(arts); got != want {
		t.Fatalf("摘要算法应与 agent 一致：期望 %s 实际 %s", want, got)
	}

	// 同 path 重复项按 (path, sha) 序取首个（对齐 agent 的 distinctBy { path }）。
	dup := append(arts, model.DeliveryConfigArtifact{Path: "plugins/A/config.yml", SHA256: strings.Repeat("9", 64)})
	if got := configArtifactMD5(dup); got != want {
		t.Fatalf("同 path 重复项应按首个计：期望 %s 实际 %s", want, got)
	}

	// 无工件 = 期望值不可得（调用方据此放行）。
	if got := configArtifactMD5(nil); got != "" {
		t.Fatalf("无工件应返回空串，实际 %s", got)
	}
}

// TestExpectedConfigArtifactMD5ReadsFrozenArtifacts 期望值来源读冻结工件表：按目标取、跨目标互不干扰。
func TestExpectedConfigArtifactMD5ReadsFrozenArtifacts(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	orderID := h.createApprovedConfigOrder(t, "工件摘要来源", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	mustCreate(t, h.env.db, &model.DeliveryConfigArtifact{
		OrderID: orderID, ServerID: "t-1", Path: "plugins/Gray/config.yml", SHA256: strings.Repeat("a", 64), SizeBytes: 6,
	})
	mustCreate(t, h.env.db, &model.DeliveryConfigArtifact{
		OrderID: orderID, ServerID: "t-2", Path: "plugins/Gray/config.yml", SHA256: strings.Repeat("b", 64), SizeBytes: 6,
	})

	for serverID, want := range map[string]string{
		"t-1": merge.MD5Hex("plugins/Gray/config.yml\n" + strings.Repeat("a", 64) + "\n"),
		"t-2": merge.MD5Hex("plugins/Gray/config.yml\n" + strings.Repeat("b", 64) + "\n"),
	} {
		got, err := h.blob.ExpectedConfigArtifactMD5(orderID, serverID)
		if err != nil {
			t.Fatalf("取期望摘要失败: %v", err)
		}
		if got != want {
			t.Fatalf("目标 %s 期望摘要不符：期望 %s 实际 %s", serverID, want, got)
		}
	}

	// 无工件目标（不在本单）返回空串而非错误：调用方据此判「期望值不可得」。
	got, err := h.blob.ExpectedConfigArtifactMD5(orderID, "t-9")
	if err != nil || got != "" {
		t.Fatalf("无工件目标应返回空串与 nil，实际 %q / %v", got, err)
	}
}

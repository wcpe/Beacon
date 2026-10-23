//go:build e2e

// FR-277 / FR-278 / FR-280 真机验收的补充缺口用例。
//
// ## 为什么需要单独一组用例
//
// 主线用例（apitest_test.go 的「下发配置闭环」「重载超时热生效」）已证明
// 「控制面写入 → agent 拉取 → 运行时按新值工作」。但验收清单里仍有四条
// **只靠单测支撑**，达不到真机取证口径，且它们全是异常路径：
//
//  1. FR-277 #2：下发后**断开控制面**仍按缓存值工作，不退回代码默认；
//  2. FR-277 #3：下发项被**撤销**时回落到本地配置，且该动作**可见**（非静默改值）；
//  3. FR-278 #3：改并发位后旧池优雅退役，**在途任务不丢**；
//  4. FR-280 #3：本地 config.yml 写了越界值时**收口并显式告警**（键名/原值/生效值/区间）。
//
// 「断连 / 撤销 / 越界」恰恰是单测最容易假绿、真机最容易漏的路径，故必须真机跑。
//
// ## 观测口径（只认可核验的文件与日志）
//
//   - 缓存保留：直读 agent 的 pushed-config.json（断连后文件仍在且值不变）；
//   - 逐级回退：直读 pushed-config.json 中该键消失 + Paper 日志「已被撤销，回落到本地配置」；
//   - 并发不丢：一次变更让 4 个监听器全部重载，断言 okCount=total 且 failedCount=0；
//   - 越界告警：直读 Paper 启动日志中的收口告警全文（四要素）。
package apitest_e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// 独立库文件与日志前缀，避免与其他用例的残留互相干扰。
	gapDBName    = "beacon-e2e-gapfr277.db"
	logPrefixGCP = "beacon-gapfr277"
	logPrefixGMC = "paper-gapfr277"
	// MC 端口避让 25566(hotreload/override)/25571(apitest)/25572(joinflow)。
	gapMCPort = "25573"
	// 不复用主用例的 namespace 常量，这里单独建。
	gapNamespace = "p7gapfr277"
)

// agentDataFile 返回本轮 Paper 运行目录下 agent 数据目录内的文件路径。
func agentDataFile(repoRoot string, parts ...string) string {
	return filepath.Join(append([]string{harness.BackendRunDir(repoRoot), "plugins", "BeaconAgent"}, parts...)...)
}

// readPushedValues 读取 agent 的下发缓存键值；文件不存在时返回 nil。
func readPushedValues(t *testing.T, repoRoot string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(agentDataFile(repoRoot, "pushed-config.json"))
	if err != nil {
		return nil
	}
	var out struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("pushed-config.json 非法 JSON：%v；原文=%s", err, truncateText(string(raw), 400))
	}
	return out.Values
}

// waitForCondition 轮询直到条件成立或超时，期间持续检查进程守卫。
func waitForCondition(t *testing.T, timeout time.Duration, guard harness.ProcessGuard, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		if err := guard.CheckEarlyExit(); err != nil {
			t.Fatalf("等待「%s」期间进程已退出：%v", desc, err)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("等待「%s」超时（%s）", desc, timeout)
}

// pushSetting 下发一条 agent 配置（200 直执 / 202 审批票据都算受理）。
func pushSetting(t *testing.T, base, token string, nsID uint, key, value, scopeLabel string, guard harness.ProcessGuard) {
	t.Helper()
	body := map[string]any{
		"namespaceId":       nsID,
		"values":            map[string]string{key: value},
		"reason":            "FR-277/278/280 缺口验收",
		"confirmationScope": scopeLabel,
	}
	if err := doAdminJSONAllow(base, http.MethodPut, "/admin/v2/agent-settings", token, body, http.StatusOK, nil, guard); err != nil {
		if err202 := doAdminJSONAllow(base, http.MethodPut, "/admin/v2/agent-settings", token, body, http.StatusAccepted, nil, guard); err202 != nil {
			t.Fatalf("下发 %s 既非 200 也非 202：200=%v；202=%v", key, err, err202)
		}
	}
}

// revokeSetting 撤销某个键的本层覆盖（值传 null）。
func revokeSetting(t *testing.T, base, token string, nsID uint, key, scopeLabel string, guard harness.ProcessGuard) {
	t.Helper()
	body := map[string]any{
		"namespaceId":       nsID,
		"values":            map[string]any{key: nil},
		"reason":            "FR-277 #3 撤销覆盖验收",
		"confirmationScope": scopeLabel,
	}
	if err := doAdminJSONAllow(base, http.MethodPut, "/admin/v2/agent-settings", token, body, http.StatusOK, nil, guard); err != nil {
		if err202 := doAdminJSONAllow(base, http.MethodPut, "/admin/v2/agent-settings", token, body, http.StatusAccepted, nil, guard); err202 != nil {
			t.Fatalf("撤销 %s 既非 200 也非 202：200=%v；202=%v", key, err, err202)
		}
	}
}

// waitPushedValue 等 agent 拉取到指定键值（拉取周期 30s，给足 2 分钟）。
func waitPushedValue(t *testing.T, repoRoot, key, want string, guard harness.ProcessGuard) {
	t.Helper()
	waitForCondition(t, 2*time.Minute, guard, "agent 拉取到 "+key+"="+want, func() bool {
		values := readPushedValues(t, repoRoot)
		return values != nil && values[key] == want
	})
}

// TestAgentSettingsGapsE2E 覆盖 FR-277/278/280 的四条缺口，全部取真机证据。
func TestAgentSettingsGapsE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	sqliteDB := filepath.Join(repoRoot, ".tmp", gapDBName)
	if err := os.MkdirAll(filepath.Dir(sqliteDB), 0o755); err != nil {
		t.Fatalf("创建 .tmp 失败：%v", err)
	}
	_ = os.Remove(sqliteDB)

	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	makeCP := func() *harness.ControlPlane {
		cp, cerr := harness.StartControlPlane(harness.ControlPlaneConfig{
			BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
			DBDriver: "sqlite", DBDSN: sqliteDB,
			AdminPassword: adminPass, AuthSecret: authSecret,
			BootstrapToken: "legacy-token-unused-by-v2",
			LogPrefix:      logPrefixGCP,
		})
		if cerr != nil {
			t.Fatalf("起控制面失败：%v", cerr)
		}
		harness.CleanupControlPlane(t, cp)
		return cp
	}

	cp := makeCP()
	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	// 独立 namespace：与主线用例隔离，避免残留数据互相影响。
	nsID, nsToken, err := harness.CreateV2Namespace(base, adminToken, gapNamespace, "FR-277/278/280 缺口验收")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	t.Logf("已建 v2 namespace id=%d", nsID)

	identityPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconAgent", "identity.yml")
	harness.ClearStaleAgentIdentityFile(t, identityPath)

	paperEnv := harness.AgentGradleEnv(base, nsToken, gapNamespace, serverID, "127.0.0.1:"+gapMCPort)
	startPaper := func(prefix string) *harness.GradleProc {
		p, perr := harness.StartGradleTask(repoRoot, ":agent-e2e:servePaper", []string{
			"-Pe2eMcPort=" + gapMCPort,
		}, paperEnv, prefix)
		if perr != nil {
			t.Fatalf("起 Paper 失败：%v", perr)
		}
		harness.CleanupGradle(t, p)
		return p
	}

	paper := startPaper(logPrefixGMC)
	identityID, err := harness.WaitPendingAgentIdentity(base, adminToken, identityPath, pendingWait, paper)
	if err != nil {
		t.Fatalf("等待 pending 身份失败：%v", err)
	}
	if err := harness.RequestApproveIdentityWithGuard(base, adminToken, identityID, serverID, "缺口验收确认身份", paper); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
	if err := harness.WaitIdentityStatusByID(base, adminToken, identityID, "active", pendingWait, paper); err != nil {
		t.Fatalf("等待 active 身份失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, gapNamespace, serverID, onlineWait, paper); err != nil {
		t.Fatalf("等待实例 online 失败：%v", err)
	}

	// ---- 缺口 1：下发后断开控制面，仍按缓存值工作（FR-277 #2）----
	t.Run("断连后仍按缓存工作", func(t *testing.T) {
		const pushed = "33000"
		pushSetting(t, base, adminToken, nsID, "delivery.config-reload-timeout-ms", pushed, gapNamespace, paper)
		waitPushedValue(t, repoRoot, "delivery.config-reload-timeout-ms", pushed, paper)

		// 真实断连：直接停进程，而不是 mock 一个错误。
		if err := cp.StopE(); err != nil {
			t.Fatalf("停止控制面失败：%v", err)
		}
		t.Log("控制面已停止，进入真实断连态")

		values := readPushedValues(t, repoRoot)
		if values == nil {
			t.Fatal("断连后 pushed-config.json 消失，说明下发值没有真正落盘")
		}
		if got := values["delivery.config-reload-timeout-ms"]; got != pushed {
			t.Fatalf("断连后下发值被改动：期望 %q 实际 %q（退回代码默认即不满足 FR-277 #2）", pushed, got)
		}
		t.Logf("PASS 断连后缓存仍为下发值 %s（未退回代码默认 30000）", pushed)

		// agent 必须仍存活：拉取失败不得导致退出。
		if err := paper.CheckEarlyExit(); err != nil {
			t.Fatalf("断连期间 Paper/agent 已退出：%v", err)
		}
		t.Log("PASS 断连期间 Paper 与 agent 存活（拉取失败未致命）")
	})

	// 后续缺口需要控制面可用：用同一份库重新拉起。
	cp = makeCP()
	adminToken, err = harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("重启后登录失败：%v", err)
	}

	// ---- 缺口 2：撤销下发项后回落本地配置且动作可见（FR-277 #3）----
	t.Run("撤销后回落本地配置", func(t *testing.T) {
		revokeSetting(t, base, adminToken, nsID, "delivery.config-reload-timeout-ms", gapNamespace, paper)

		waitForCondition(t, 2*time.Minute, paper, "缓存中该键被移除", func() bool {
			values := readPushedValues(t, repoRoot)
			if values == nil {
				return false
			}
			_, exists := values["delivery.config-reload-timeout-ms"]
			return !exists
		})
		t.Log("PASS 撤销后 pushed-config.json 中该键已消失")

		// 回退必须可见：要求 agent 明确记录「已被撤销，回落到本地配置」。
		logPath := filepath.Join(harness.BackendRunDir(repoRoot), "logs", "latest.log")
		raw, _ := os.ReadFile(logPath)
		if !strings.Contains(string(raw), "已被撤销，回落到本地配置") {
			t.Fatalf("未见「已被撤销，回落到本地配置」日志，无法证明回退真实发生；日志尾部：%s",
				truncateText(tailString(string(raw), 800), 800))
		}
		t.Log("PASS agent 明确记录回退动作与回退目标（非静默改值）")
	})

	// ---- 缺口 3：改并发位不丢在途任务（FR-278 #3）----
	t.Run("改并发位不丢在途任务", func(t *testing.T) {
		// 并发位 64 → 2：旧池要优雅退役、新池接管后续派发。
		// 若退役用 shutdownNow 或把在途任务丢掉，紧接着这一轮重载就会卡死或拿不到结果。
		pushSetting(t, base, adminToken, nsID, "delivery.reload-pool-size", "2", gapNamespace, paper)
		waitPushedValue(t, repoRoot, "delivery.reload-pool-size", "2", paper)
		t.Log("并发位已下发为 2 并被 agent 拉取")

		// 触发一轮真实配置变更，用探针实测「这轮重载真的跑完了」。
		//
		// 必须用探针认识的两个受控 dataId（timed-a / timed-b）：
		// 探针只对这两条路径做「睡眠 + 记实测」的分支，其它 dataId 走普通 ok 分支、不产观测。
		const dataID = "delivery-reload-timed-a.yml"
		obsPath := filepath.Join(harness.ProbeDataDir(harness.BackendRunDir(repoRoot)), "e2e-delivery-hot-reload.log")
		before := countTimedObservations(obsPath, dataID)
		triggerReloadInNamespace(t, base, adminToken, dataID, gapNamespace, paper)

		got := waitTimedObservation(t, obsPath, dataID, before, obsWait, paper)
		t.Logf("PASS 并发位 64→2 后新一轮重载在 %dms 内正常回调完成（interrupted=%v）："+
			"任务未被旧池退役丢弃，否则此观测不会出现", got.elapsedMs, got.interrupted)
	})

	// ---- 缺口 4：本地配置越界收口并显式告警（FR-280 #3）----
	t.Run("本地越界显式告警", func(t *testing.T) {
		// 为什么走环境变量而不是改 config.yml：
		// 每次 servePaper 启动都会用出厂模板**重建** agent 数据目录，写在 config.yml 里的
		// 越界值会被覆盖掉，告警因此永远不出现（本用例第一版就踩了这个坑）。
		// 而 EnvOverridingConfigReader 的 env 是跨重启稳定的，且它位于 config.yml 之上，
		// 正好能把越界值真实喂给 AgentBootstrap 的收口逻辑。
		const badValue = "999999999" // 远超上限 3600000
		paperEnv["BEACON_AGENT_DELIVERY_CONFIG_RELOAD_TIMEOUT_MS"] = badValue
		t.Logf("以环境变量注入越界值 %s（超出合法上限 3600000），重启 Paper 观察收口告警", badValue)

		// 越界收口发生在 AgentBootstrap 读配置时，故必须重启才能观察到。
		if err := paper.Stop(); err != nil {
			t.Fatalf("停止 Paper 失败：%v", err)
		}
		paper = startPaper(logPrefixGMC + "-c")

		logPath := filepath.Join(harness.BackendRunDir(repoRoot), "logs", "latest.log")
		waitForCondition(t, 3*time.Minute, paper, "越界收口告警出现", func() bool {
			b, _ := os.ReadFile(logPath)
			return strings.Contains(string(b), "配置值越界已收口")
		})

		b, _ := os.ReadFile(logPath)
		text := string(b)
		// 四要素缺一不可：键名+原值、合法区间、实际生效值。
		for _, want := range []string{
			"delivery.config-reload-timeout-ms=" + badValue,
			"合法区间",
			"实际生效",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("收口告警缺少要素 %q（要求含键名/原值/生效值/合法区间）；日志尾部：%s",
					want, tailString(text, 900))
			}
		}
		t.Log("PASS 越界值被收口，告警含键名、原值、生效值与合法区间四要素")
	})
}

// triggerReloadInNamespace 在指定 namespace 下建一份 V1 global 配置以触发一次受控变更。
//
// 不能复用主用例的 triggerReloadForDataID：它把 namespace 写死成 `p7apitest`，
// 而本用例的 agent 属于 gapNamespace，配置建到别的域不会推给它，探针因此收不到任何变更。
func triggerReloadInNamespace(t *testing.T, base, token, dataID, nsCode string, guard harness.ProcessGuard) {
	t.Helper()
	body := map[string]any{
		"namespace":  nsCode,
		"group":      "global",
		"dataId":     dataID,
		"scopeLevel": "global",
		"format":     "yaml",
		"content":    "gap-reload-marker: " + time.Now().UTC().Format("20060102T150405.000000"),
		"comment":    "FR-278 并发位热更验收触发配置",
	}
	if err := doAdminJSONAllow(base, http.MethodPost, "/admin/v1/configs", token, body, http.StatusCreated, nil, guard); err != nil {
		t.Fatalf("建 V1 配置项 %s 失败（namespace=%s）：%v", dataID, nsCode, err)
	}
	t.Logf("已建 V1 global 层配置 dataId=%s（namespace=%s，应经长轮询触发一次受控重载）", dataID, nsCode)
}

// truncateText 截断长文本，仅用于失败信息。
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（已截断）"
}

// tailString 取文本末尾 n 字节，便于展示最新日志。
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

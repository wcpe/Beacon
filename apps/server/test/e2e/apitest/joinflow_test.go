//go:build e2e

// FR-276「零配置接入」真机端到端验收：`/beacon join <控制面地址> <令牌>` 全链路。
//
// ## 这条测试补的是哪个缺口
//
// 既有的 pending → approve → active 链路（TestAgentApiRemainingSurfaceE2E）走的是
// **环境变量注入**接入信息：[harness.AgentGradleEnv] 把 endpoints/token 塞进服务端进程环境，
// agent 启动即接入。也就是说「join 命令」这一步从来没有被真机执行过——
// 而 FR-276 的验收标准恰恰是「join 写凭据 → 接入 → pending → 批准后 active」。
//
// 本用例把注入全部拿掉，改为：
//  1. 运行目录恢复成**全新出厂态**（无凭据、无身份、config.yml 留着模板占位符）；
//  2. 由外部驱动投递 join 请求文件，探针插件在主线程经**控制台**派发真实的 `/beacon join`；
//  3. 断言凭据落盘内容、控制面 pending 身份、批准后 active、以及实例开始上报（online）。
//
// ## 顺带锁住的回归
//
// 出厂 config.yml 曾把 `<CONTROL_PLANE_URL>` / `<NAMESPACE_ACCESS_TOKEN>` 当示例值，
// 两者都非空 —— 于是被当成「运维已显式配置」：join 被静默忽略，占位符还被写进凭据，
// 之后即使清空 config.yml 也救不回来。本用例**故意**把占位符留在 config.yml 里，
// 断言 join 仍成功、且凭据不含占位符字面量。
//
// ## 为什么单独成一个顶层 Test 而不是主测试里的子测试
//
// 它需要「干净运行目录 + 不注入接入信息」，而主测试的编排已经把运行目录用旧了、
// 并且全程注入。两者前置互斥，只能各起一套。控制面端口与 MC 端口复用主测试的值是安全的：
// 同包内顶层 Test 串行执行，且主测试的进程在 t.Cleanup 里已经停掉。
package apitest_e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// join 用例独占的 namespace / 库文件 / 日志前缀，避免与主测试的残留互相干扰。
	joinNamespace = "p7joine2e"
	// join 用例的 MC 端口：避让主测试的 25571 与其它 e2e 包占用的端口。
	joinMCPort = "25572"
	// join 用例的 serverId：由**运维在批准时**指定（身份里的 serverId 由控制面确认，
	// agent 不自行声明），与主测试保持一致即可。
	joinServerID  = "e2e-bukkit-1"
	joinDBName    = "beacon-e2e-joine2e.db"
	logPrefixJCP  = "beacon-joine2e"
	logPrefixJMC  = "paper-joine2e"
	joinProbeGate = "1"
)

// TestBeaconJoinFullFlowE2E 验证 `/beacon join` 从命令到上岸的完整链路。
func TestBeaconJoinFullFlowE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	runDir := harness.BackendRunDir(repoRoot)

	// ---- 前置 1：干净的全新出厂态 ----
	//
	// 必须在 Paper 启动之前完成：agent 只在文件缺失时生成默认配置，已存在的 config.yml
	// 会被原样保留——这正是「出厂模板被复制出来后一直留着占位符」的真实形态。
	harness.ResetAgentDataDir(t, runDir)
	harness.WriteAgentBootstrapConfig(t, runDir, harness.PlaceholderAgentConfigYAML)
	harness.RemoveStaleJoinRequest(t, runDir)
	t.Log("已把 agent 数据目录重置为出厂态（config.yml 保留模板占位符，无凭据、无身份）")

	// ---- 前置 2：控制面 ----
	sqliteDB := filepath.Join(repoRoot, ".tmp", joinDBName)
	if err := os.MkdirAll(filepath.Dir(sqliteDB), 0o755); err != nil {
		t.Fatalf("创建 .tmp 目录失败：%v", err)
	}
	_ = os.Remove(sqliteDB)

	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret,
		BootstrapToken: "legacy-token-unused-by-v2",
		LogPrefix:      logPrefixJCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp)

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	nsID, nsToken, err := harness.CreateV2Namespace(base, adminToken, joinNamespace, "FR-276 join 全链路 E2E")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	t.Logf("已建 v2 namespace id=%d（令牌仅经 join 请求文件传递，不落日志）", nsID)

	// ---- 前置 3：起 Paper，**不注入任何接入信息** ----
	//
	// 只放探针门控与端口：endpoints/token/namespace/身份一律不注入，
	// 接入信息必须由 join 命令写进凭据后自行解析出来。
	paperEnv := map[string]string{
		"BEACON_E2E_JOIN": joinProbeGate,
	}
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:servePaper", []string{
		"-Pe2eMcPort=" + joinMCPort,
	}, paperEnv, logPrefixJMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	harness.CleanupGradle(t, paper)

	// ---- 步骤 1：等 agent 生成身份（此时**尚未接入**，只是有了身份） ----
	//
	// 身份在 enable 阶段就会落盘（凭据 > 旧文件 > 新生成），与是否接入无关。
	// 先拿到它，join 之后才能按 identityId 精确等待 pending，而不是靠「碰巧出现的 pending」。
	identityPath := harness.AgentIdentityPath(runDir)
	identityID, err := harness.WaitAgentIdentityID(identityPath, pendingWait, paper)
	if err != nil {
		t.Fatalf("等待 agent 身份文件失败（%v）：%v", identityPath, err)
	}
	t.Logf("agent 已生成身份 identityId=%s（未注入接入信息，尚未注册）", identityID)

	// 未 join 前**不得**已经具备接入条件：凭据文件此时已存在（agent 在 enable 阶段就会把新身份
	// 落盘），但 endpoint/token 必须是空的。若这里非空，说明接入信息是从别处漏进来的，
	// 后面的断言就会退化成「验证注入路径」而不是 join 路径。
	if err := harness.AssertCredentialsNotJoined(runDir); err != nil {
		t.Fatalf("%v", err)
	}
	t.Log("PASS join 之前凭据不含接入信息（endpoint/token 为空），接入信息确实还没到")

	// ---- 步骤 2：投递 join 请求，由探针经控制台派发真实命令 ----
	harness.WriteJoinRequest(t, runDir, base, nsToken)
	joinObs := harness.JoinObservationPath(runDir)
	t.Log("已投递 join 请求，等待探针经控制台派发 /beacon join")

	waitObservation(t, joinObs, 0, obsWait, paper, func(o observation) bool {
		return o.source == "JOIN_DISPATCHED"
	}, "控制台已接受 /beacon join 命令")

	// ---- 步骤 3：断言凭据被 join 更新且不含占位符 ----
	//
	// 这是本用例的核心断言之一：join 的产物是**凭据文件**，
	// 且其中绝不能出现 config.yml 的模板占位符（见文件头的回归说明）。
	credentials := waitCredentials(t, runDir, base, nsToken, obsWait, paper)
	if credentials.IdentityID != identityID {
		t.Fatalf("凭据里的身份应与 identity.yml 一致（%s），实际 %s", identityID, credentials.IdentityID)
	}
	t.Logf("PASS 凭据已被 join 写入且 endpoint/token 正确、不含模板占位符：%s", harness.AgentCredentialsPath(runDir))

	// ---- 步骤 4：控制面出现 pending 身份 ----
	if err := harness.WaitIdentityStatusByID(base, adminToken, identityID, "pending", pendingWait, paper); err != nil {
		t.Fatalf("join 后控制面应出现 pending 身份（%s）：%v", identityID, err)
	}
	t.Logf("PASS 控制面已出现 pending 身份 identityId=%s", identityID)

	// ---- 步骤 5：批准后转 active，并开始上报（online） ----
	if err := harness.RequestApproveIdentityWithGuard(base, adminToken, identityID, joinServerID, "FR-276 join 全链路 E2E 确认身份", paper); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
	if err := harness.WaitIdentityStatusByID(base, adminToken, identityID, "active", pendingWait, paper); err != nil {
		t.Fatalf("批准后应转 active：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, joinNamespace, joinServerID, onlineWait, paper); err != nil {
		t.Fatalf("active 后应衔接数据面 online（说明 agent 已开始上报）：%v", err)
	}
	t.Logf("PASS join 全链路成立：凭据落盘 → pending → approve → active → online（serverId=%s）", joinServerID)
}

// waitCredentials 等待凭据被 join 更新为「地址/令牌正确 + 无占位符」。
//
// 为什么轮询而不是读一次：写凭据与派发回执都在 join 命令的调用栈里完成，
// 但探针的观测行与文件落盘之间仍有极短的时序差；读一次会偶发抢跑。
// 这里把**断言**做成轮询条件，失败时才用最后一次错误报错——不会因为抢跑而假失败。
func waitCredentials(
	t *testing.T,
	runDir, wantEndpoint, wantToken string,
	timeout time.Duration,
	guard *harness.GradleProc,
) harness.AgentCredentials {
	t.Helper()
	var lastErr error
	var got harness.AgentCredentials
	err := waitUntil(timeout, guard, func(context.Context) bool {
		if err := harness.AssertCredentialsUsable(runDir, wantEndpoint, wantToken); err != nil {
			lastErr = err
			return false
		}
		cred, err := harness.ReadAgentCredentials(runDir)
		if err != nil {
			lastErr = err
			return false
		}
		got = cred
		return true
	})
	if err != nil {
		t.Fatalf("等待 join 写入可用凭据失败（%s）：最近一次错误=%v", timeout, lastErr)
	}
	return got
}

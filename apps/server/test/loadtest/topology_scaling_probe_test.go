//go:build e2e && loadtest

// 单端点归因探测：证明「随注册规模增长的那部分成本」具体落在哪一个端点上。
//
// 为什么需要它：主规模对照（scale_1000_test.go）跑的是混合负载、且两个规模不在同一时刻，
// 宿主机负载无法完全对齐（64 台那轮 load1 均值 14.9、1000 台那轮 18.8）。
// 于是「1000 台时 RPS 掉了 X%」这句话里混着两个来源：注册表规模，与环境漂移。
//
// 本探测把变量压到只剩一个：**同一台宿主机、同一次登录、同一批客户端，
// 在同一控制面进程上先注册 64 台、测一轮，再补注册到 1000 台、再测一轮**。
// 两轮之间没有别的负载介入，宿主机条件近乎连续；若某端点的时延在两轮之间跳变，
// 而其它端点不动，那就只能是「该端点的成本随注册表规模变化」——环境漂移不会挑端点。
//
// 探测的是**单请求串行**（并发 1）：并发 >1 时连接池排队会盖住端点自身的成本差异；
// 并发 1 且池等待为 0 时，测出来的就是纯端点成本，可直接按实例数折算「每实例微秒」。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=20m -v -run '^TestTopologyScalingProbe' ./apps/server/test/loadtest/
package loadtest_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// topologyProbeDBName 是本探查专用的库名（不与其它用例共用，避免互相删文件）。
	topologyProbeDBName = "beacon-topoprobe.db"
	// topologyProbeSmall / topologyProbeLarge 是两轮的注册规模。
	// 取 64 → 1000：与主对照同口径，便于把结论直接接上。
	topologyProbeSmall = 64
	topologyProbeLarge = 1000
	// topologyProbeDuration 是每轮每端点的采样时长（并发 1 下足够收集上千个样本）。
	topologyProbeDuration = 8 * time.Second
)

// topologyProbeRow 是单端点在一轮里的测量。
type topologyProbeRow struct {
	Scale int     `json:"scale"`
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Count int     `json:"count"`
	RPS   float64 `json:"rps"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	// BodyBytes 是该端点在并发 1 下的响应体字节数（取首条成功响应的长度）。
	// 响应体大小是「成本随规模增长」最直观的物证：若它随注册数线性增长，
	// 那么序列化 + 网络传输 + 客户端读体的成本也必然随之增长。
	BodyBytes int `json:"bodyBytes"`
	// PerInstanceUs 是相对小规模轮次的每实例边际时延（微秒/实例）。
	// 只在 p50 有可比性时给出，用于回答「多一个子服，这条端点贵多少」。
	PerInstanceUs float64 `json:"perInstanceUs"`
}

// TestTopologyScalingProbe 在同一控制面上做「64 台 vs 1000 台」的单端点归因。
func TestTopologyScalingProbe(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", scaleWorkDirName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建探测工作目录失败：%v", err)
	}
	dbPath := filepath.Join(workDir, topologyProbeDBName)
	removeStaleDBFile(t, dbPath)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: "beacon-topoprobe",
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": defaultCPLogLevel},
	}, controlPlaneStartAttempts)
	defer func() {
		if err := cp.StopE(); err != nil {
			t.Errorf("停止控制面失败：%v", err)
		}
	}()

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	client := newLoadClient(4)
	defer client.CloseIdleConnections()

	rows := make([]topologyProbeRow, 0, 24)
	for _, scale := range []int{topologyProbeSmall, topologyProbeLarge} {
		t.Logf("---- 注册规模 %d ----", scale)
		// serverId 是确定性生成的（loadtest-agent-<i>），故大规模那轮会把小规模那批原样重注册一遍；
		// 同址重连在注册表里是幂等覆盖（Registry.Register 对同 address 直接覆盖，
		// 不做重复 serverId 拒绝），因此两轮的注册表内容确实只差「数量」这一个变量。
		reg := registerVirtualAgentsConcurrent(t, client, baseURL, scale, 16, cp.PID(), os.Getpid())
		if reg.Succeeded < scale {
			t.Fatalf("规模 %d 注册未达标：成功 %d，失败 %d", scale, reg.Succeeded, len(reg.Failures))
		}
		serverIDs := reg.IDs
		t.Logf("注册完成：%d 台，耗时 %dms（%.1f 台/秒）", reg.Succeeded, reg.ElapsedMs, reg.RPS)
		// 等注册写审计落盘、注册表稳定（健康扫描周期 5s），避免把注册尾部的 IO 混进测量。
		time.Sleep(2 * time.Second)

		scenarioCfg := scenarioConfig{
			BaseURL: baseURL, AdminToken: token,
			BootstrapToken: bootstrapToken, Namespace: agentNamespace, ServerIDs: serverIDs,
		}
		full := buildScenarioSet(scenarioCfg)
		// 逐个端点单独压（并发 1）：只有隔离成单端点，成本差异才不会被混合负载平均掉。
		for i := range full.items {
			item := full.items[i]
			single := newScenarioSetFrom([]scenario{item})
			res, runErr := runLoad(context.Background(), &driverConfig{
				label: fmt.Sprintf("规模 %d / %s", scale, item.Name), client: client, scenarios: single,
				concurrent: 1, duration: topologyProbeDuration,
				requestTimeout: requestTimeout, guard: cp,
			})
			if runErr != nil {
				t.Errorf("端点 %s（规模 %d）测量失败：%v", item.Name, scale, runErr)
				continue
			}
			body := measureBodyBytes(t, client, item)
			rows = append(rows, topologyProbeRow{
				Scale: scale, Name: item.Name, Path: item.Path,
				Count: res.TotalRequests, RPS: res.RPS,
				P50Ms: res.Latency.P50Ms, P95Ms: res.Latency.P95Ms, BodyBytes: body,
			})
			t.Logf("  %-22s 请求 %6d  RPS %7.0f  p50 %6.2fms  p95 %7.2fms  响应体 %7d 字节",
				item.Name, res.TotalRequests, res.RPS, res.Latency.P50Ms, res.Latency.P95Ms, body)
		}
	}

	// 折算每实例边际成本并给结论。
	for i := range rows {
		small, ok := findProbeRow(rows, topologyProbeSmall, rows[i].Name)
		if ok && rows[i].Scale > topologyProbeSmall {
			deltaInstances := float64(rows[i].Scale - topologyProbeSmall)
			if deltaInstances > 0 {
				rows[i].PerInstanceUs = (rows[i].P50Ms - small.P50Ms) * 1000 / deltaInstances
			}
		}
	}

	t.Log("================ 单端点归因结论（并发 1，同一控制面进程）================")
	t.Logf("%-22s | %10s | %10s | %9s | %11s | %11s", "端点", "64台 p50", "1000台 p50", "倍数", "响应体64", "响应体1000")
	for _, row := range rows {
		if row.Scale != topologyProbeSmall {
			continue
		}
		large, ok := findProbeRow(rows, topologyProbeLarge, row.Name)
		if !ok {
			continue
		}
		ratio := 0.0
		if row.P50Ms > 0.0001 {
			ratio = large.P50Ms / row.P50Ms
		}
		t.Logf("%-22s | %8.2fms | %8.2fms | %8.1fx | %9d | %9d",
			row.Name, row.P50Ms, large.P50Ms, ratio, row.BodyBytes, large.BodyBytes)
	}
	for _, row := range rows {
		if row.Scale == topologyProbeLarge && row.PerInstanceUs > 1 {
			t.Logf("端点 %s：每多一个子服，p50 增加约 %.2f 微秒", row.Name, row.PerInstanceUs)
		}
	}
}

// findProbeRow 在结果里找指定规模 + 端点的行。
func findProbeRow(rows []topologyProbeRow, scale int, name string) (topologyProbeRow, bool) {
	for _, row := range rows {
		if row.Scale == scale && row.Name == name {
			return row, true
		}
	}
	return topologyProbeRow{}, false
}

// measureBodyBytes 发一发该端点并返回响应体字节数（失败返回 -1）。
func measureBodyBytes(t *testing.T, client *http.Client, item scenario) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	var body io.Reader
	if len(item.bodies) > 0 {
		body = bytes.NewReader(item.bodies[0])
	}
	req, err := http.NewRequestWithContext(ctx, item.Method, item.URL, body)
	if err != nil {
		return -1
	}
	for _, h := range item.headers {
		req.Header.Set(h.key, h.value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return -1
	}
	// 拓扑类响应是 JSON 数组，这里附带把节点/成员数数出来，作为「规模真的生效」的交叉验证。
	if item.Name == "topology" || item.Name == "zone_tree" {
		t.Logf("    （%s 响应体 %d 字节；%s）", item.Name, len(raw), summarizeTopologyJSON(raw))
	}
	return len(raw)
}

// summarizeTopologyJSON 尽力数出响应体里的实例成分，用于交叉验证注册规模。
func summarizeTopologyJSON(raw []byte) string {
	var probe struct {
		Nodes  []json.RawMessage `json:"nodes"`
		Groups []struct {
			Members []string `json:"members"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "（非预期结构，无法统计）"
	}
	members := 0
	for _, g := range probe.Groups {
		members += len(g.Members)
	}
	return fmt.Sprintf("nodes=%d groups=%d members=%d", len(probe.Nodes), len(probe.Groups), members)
}

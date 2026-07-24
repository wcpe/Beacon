//go:build e2e

// FR-155 区服权威缺口链路的纯 Go 端到端测试：起真实控制面二进制（SQLite、独立端口），
// 经 admin HTTP 走完整链路并断言——
//
//	agent 注册 → approve 首次接入 → 首次分配落小区 → 换区工单(rezone) → 换区重确认落新区 →
//	draining 排空标记 → default-entry 默认入口（含未分配 409 负例）→ zone-tree 结构树计数。
//
// 不依赖真实 MC agent：agent 身份注册用 HTTP POST /beacon/v2/agent/register 模拟（带 namespace token）。
// 铁律：只调既有 admin/agent REST，绝不旁路或弱化任一 FR-155 约束来「让断言通过」。
package rezone_e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// 控制面地址：独立端口 18850，避开 8848 生产 / 18848 前端真后端 e2e / 3307 MySQL；可经 E2E_BEACON_URL 覆盖。
var beaconURL = e2eBeaconURL()

// e2e 测试库（非生产）鉴权常量：可经 env 覆盖，缺省用测试值使 -tags=e2e 无需注入密钥即可跑通。
const (
	adminUser      = "admin"
	defaultAdminPW = "test-pass"
	defaultSecret  = "test-secret"
	bootstrapToken = "beacon-e2e-bootstrap"
	logPrefixCP    = "beacon-rezone"
	sqliteDBName   = "beacon-e2e-rezone.db"
	adminV2        = "/admin/v2"
)

// 换区链路的固定夹具（namespace / 身份 / 服务 / 小区名）。
// nsName 用独立名避开控制面启动预置的 prod / test（一次性接入 token 仅在新建时返回，故须自建 namespace）。
const (
	nsName        = "rezone-e2e"
	identityMain  = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa" // 主链路服务器身份
	identityBare  = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb" // 未分配 409 负例身份
	serverMain    = "lobby-1"
	serverBare    = "lobby-9"
	kindBackend   = "backend"
	clusterName   = "bc-a"
	regionName    = "r1"
	zoneAName     = "z-a"
	zoneBName     = "z-b"
	registerReady = 30 * time.Second
)

// e2eBeaconURL 取控制面地址：优先 E2E_BEACON_URL，缺省 http://localhost:18850。
func e2eBeaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// TestRezoneE2E 按相位编排整套 FR-155 区服权威链路：构建 → 起控制面(SQLite) → 登录 →
// 注册/approve → 首次分配 → 换区 → 重确认 → draining → default-entry → zone-tree。defer 收口杀控制面。
func TestRezoneE2E(t *testing.T) {
	adminPass := envOr("E2E_ADMIN_PASS", defaultAdminPW)
	authSecret := envOr("E2E_AUTH_SECRET", defaultSecret)

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}

	// 每轮从干净库开始，消除跨轮残留干扰（连跑防脆）。
	sqliteDB := filepath.Join(repoRoot, ".tmp", sqliteDBName)
	if err := os.MkdirAll(filepath.Dir(sqliteDB), 0o755); err != nil {
		t.Fatalf("创建 .tmp 目录失败：%v", err)
	}
	_ = os.Remove(sqliteDB)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（SQLite，端口 " + beaconURL + "）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: beaconURL,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: logPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	defer cp.Stop()

	token, err := harness.Login(beaconURL, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	// 建 namespace（拿一次性接入 token 供 agent 注册）。
	nsID, nsToken := createNamespace(t, token, nsName)

	// 建区服权威结构：bc 集群 → 大区 → 小区 A/B。
	clusterID := createNode(t, token, "/bc-clusters", map[string]any{"namespaceId": nsID, "name": clusterName})
	regionID := createNode(t, token, "/regions", map[string]any{"bcClusterId": clusterID, "name": regionName})
	zoneA := createNode(t, token, "/zones", map[string]any{"regionId": regionID, "name": zoneAName})
	zoneB := createNode(t, token, "/zones", map[string]any{"regionId": regionID, "name": zoneBName})

	// 相位一：注册 → pending；approve → active。
	registerAgent(t, nsToken, identityMain, serverMain, kindBackend)
	assertIdentityStatus(t, token, identityMain, "pending")
	approved := approveIdentity(t, token, identityMain, nil)
	if approved["status"] != "active" {
		t.Fatalf("approve 后身份应 active，实际 %v", approved["status"])
	}
	rowID := serverRowID(t, token, nsID, serverMain)

	// 相位二：首次分配到小区 A（非默认入口，留待 default-entry 相位单独验证）。
	assignServer(t, token, rowID, "zone", zoneA, false)
	row := serverRow(t, token, nsID, serverMain)
	if row["assigned"] != true || asString(row["zoneName"]) != zoneAName {
		t.Fatalf("首次分配后应 assigned=true 且 zoneName=%s，实际 %v", zoneAName, row)
	}

	// 相位三：换区工单 → 小区 B。解绑清归属 + 写预填 + 身份重入 pending。
	rezoneServer(t, token, rowID, "zone", zoneB)
	row = serverRow(t, token, nsID, serverMain)
	if row["assigned"] != false || asString(row["pendingZoneName"]) != zoneBName {
		t.Fatalf("换区后应 assigned=false 且 pendingZoneName=%s，实际 %v", zoneBName, row)
	}
	assertIdentityStatus(t, token, identityMain, "pending")
	assertRezonePrefill(t, token, identityMain, zoneB)

	// 相位四：换区重确认（缺省取预填目标）→ 落区 B、清 pending、身份回 active。
	reconfirmed := approveIdentity(t, token, identityMain, nil)
	if reconfirmed["status"] != "active" {
		t.Fatalf("换区重确认后身份应 active，实际 %v", reconfirmed["status"])
	}
	row = serverRow(t, token, nsID, serverMain)
	if row["assigned"] != true || asString(row["zoneName"]) != zoneBName || row["pendingZoneName"] != nil {
		t.Fatalf("重确认后应 assigned=true、zoneName=%s、pendingZoneName 空，实际 %v", zoneBName, row)
	}

	// 相位五：draining 排空标记切换。
	drainView := setDraining(t, token, serverMain, true)
	if drainView["draining"] != true {
		t.Fatalf("draining 切换后响应应 draining=true，实际 %v", drainView)
	}
	row = serverRow(t, token, nsID, serverMain)
	if row["draining"] != true {
		t.Fatalf("draining 切换后列表应 draining=true，实际 %v", row)
	}

	// 相位六：default-entry 默认入口切换（已分配小区，应成功）。
	entryView := setDefaultEntry(t, token, rowID, true, http.StatusOK)
	if entryView["isDefaultEntry"] != true {
		t.Fatalf("default-entry 切换后响应应 isDefaultEntry=true，实际 %v", entryView)
	}

	// 相位六下发：v2 默认入口必须贯通到 v1 发现（BC fallback 注入消费链，ADR-0067）。
	// 先经 v1 register 把实例放进内存注册表（BC 目录同步的数据前提），再断言 discovery 打 zoneDefaultEntry 标。
	registerAgentV1(t, nsName, serverMain)
	assertDiscoveryDefaultEntry(t, nsName, serverMain, true)
	// v1 只读列表（Legacy 消费）同步反映 v2 真源：(group, zone) 取大区名 / 小区名。
	assertDefaultEntryList(t, token, nsName, regionName, zoneBName, serverMain)

	// 相位六负例：未分配小区的 server 置默认入口应 409 not_assigned。
	registerAgent(t, nsToken, identityBare, serverBare, kindBackend)
	approveIdentity(t, token, identityBare, nil)
	bareRowID := serverRowID(t, token, nsID, serverBare)
	conflict := setDefaultEntry(t, token, bareRowID, true, http.StatusConflict)
	if conflict["code"] != "not_assigned" {
		t.Fatalf("未分配 server 置默认入口应 409 not_assigned，实际 %v", conflict)
	}

	// 相位七：zone-tree 结构树含建好的 cluster/region/zone 且计数正确。
	assertZoneTree(t, token, nsID, zoneBName, zoneAName)

	t.Log("PASS 换区/draining/default-entry 端到端链路全通")
}

// ---- 链路步骤 ----

// createNamespace 建 namespace，返回其 id 与一次性明文接入 token。
func createNamespace(t *testing.T, token, name string) (uint, string) {
	t.Helper()
	var out struct {
		ID          uint   `json:"id"`
		AccessToken string `json:"accessToken"`
	}
	doAdmin(t, http.MethodPost, adminV2+"/namespaces", token, map[string]any{"name": name}, http.StatusCreated, &out)
	if out.ID == 0 || out.AccessToken == "" {
		t.Fatalf("建 namespace 应返回 id 与 accessToken，实际 %+v", out)
	}
	return out.ID, out.AccessToken
}

// createNode 建区服权威节点（bc-cluster / region / zone），返回其数字 id（响应 camelCase id）。
func createNode(t *testing.T, token, path string, body map[string]any) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	doAdmin(t, http.MethodPost, adminV2+path, token, body, http.StatusCreated, &out)
	if out.ID == 0 {
		t.Fatalf("建 %s 应返回数字 id，实际 %+v", path, out)
	}
	return out.ID
}

// registerAgent 以 namespace token 模拟 agent 注册，期望进入 pending（202）。
func registerAgent(t *testing.T, nsToken, identityID, serverID, kind string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"identityId": identityID, "serverId": serverID, "kind": kind, "bootId": "boot-" + serverID,
	})
	req, _ := http.NewRequest(http.MethodPost, beaconURL+"/beacon/v2/agent/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beacon-Token", nsToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("注册 %s 失败：%v", serverID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("注册 %s 应 202 pending，实际 %d：%s", serverID, resp.StatusCode, string(data))
	}
}

// approveIdentity 确认身份（target 为 nil 时发空对象 {}，即首次接入 / 换区重确认取预填）。返回身份视图。
func approveIdentity(t *testing.T, token, identityID string, target map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{}
	if target != nil {
		body["target"] = target
	}
	var out map[string]any
	doAdmin(t, http.MethodPost, adminV2+"/agent-identities/"+identityID+"/approve", token, body, http.StatusOK, &out)
	return out
}

// assignServer 首次分配一台 server 到目标（zone / bc_cluster）。
func assignServer(t *testing.T, token string, rowID uint, targetKind string, targetID uint, isDefaultEntry bool) {
	t.Helper()
	doAdmin(t, http.MethodPost, adminV2+"/server-assignments", token, map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": targetKind, "id": targetID},
		"isDefaultEntry": isDefaultEntry, "reason": "首次分配",
	}, http.StatusOK, nil)
}

// rezoneServer 发起换区工单并断言逐台 ok。
func rezoneServer(t *testing.T, token string, rowID uint, targetKind string, targetID uint) {
	t.Helper()
	var out struct {
		Results []struct {
			Ok bool `json:"ok"`
		} `json:"results"`
	}
	doAdmin(t, http.MethodPost, adminV2+"/server-rezones", token, map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": targetKind, "id": targetID}, "reason": "扩容换区",
	}, http.StatusOK, &out)
	if len(out.Results) != 1 || !out.Results[0].Ok {
		t.Fatalf("换区结果应逐台 ok，实际 %+v", out.Results)
	}
}

// setDraining 切换 server 排空标记（路径为业务 serverId），返回富化视图。
func setDraining(t *testing.T, token, serverID string, draining bool) map[string]any {
	t.Helper()
	var out map[string]any
	doAdmin(t, http.MethodPut, adminV2+"/servers/"+serverID+"/draining", token,
		map[string]any{"draining": draining, "reason": "排空演练"}, http.StatusOK, &out)
	return out
}

// setDefaultEntry 切换 server 默认入口（路径为行数字 id），按期望状态码返回响应体（成功视图或错误体）。
func setDefaultEntry(t *testing.T, token string, rowID uint, value bool, wantStatus int) map[string]any {
	t.Helper()
	var out map[string]any
	doAdmin(t, http.MethodPut, adminV2+"/servers/"+utoa(rowID)+"/default-entry", token,
		map[string]any{"value": value}, wantStatus, &out)
	return out
}

// registerAgentV1 以共享 agent 令牌把实例注册进 v1 内存注册表（发现视图的数据前提）。
func registerAgentV1(t *testing.T, ns, serverID string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"namespace": ns, "serverId": serverID, "role": "bukkit", "address": "127.0.0.1:25565",
	})
	req, _ := http.NewRequest(http.MethodPost, beaconURL+"/beacon/v1/agent/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beacon-Token", bootstrapToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("v1 注册 %s 失败：%v", serverID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("v1 注册 %s 应 200，实际 %d：%s", serverID, resp.StatusCode, string(data))
	}
}

// assertDiscoveryDefaultEntry 断言 v1 发现输出中某实例的 zoneDefaultEntry 标志（BC 注入消费的字段）。
func assertDiscoveryDefaultEntry(t *testing.T, ns, serverID string, want bool) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, beaconURL+"/beacon/v1/agent/discovery?namespace="+ns, nil)
	req.Header.Set("X-Beacon-Token", bootstrapToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("拉取发现失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Instances []struct {
			ServerID         string `json:"serverId"`
			ZoneDefaultEntry bool   `json:"zoneDefaultEntry"`
		} `json:"instances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析发现响应失败：%v", err)
	}
	for _, inst := range out.Instances {
		if inst.ServerID == serverID {
			if inst.ZoneDefaultEntry != want {
				t.Fatalf("发现输出 %s 的 zoneDefaultEntry 应为 %v，实际 %v（v2 默认入口未贯通 v1 下发）", serverID, want, inst.ZoneDefaultEntry)
			}
			return
		}
	}
	t.Fatalf("发现输出应含实例 %s，实际 %+v", serverID, out.Instances)
}

// assertDefaultEntryList 断言 v1 默认入口只读列表含指定 (group, zone, serverId) 行。
func assertDefaultEntryList(t *testing.T, token, ns, group, zone, serverID string) {
	t.Helper()
	var out struct {
		Items []struct {
			Group           string `json:"group"`
			Zone            string `json:"zone"`
			DefaultServerID string `json:"defaultServerId"`
		} `json:"items"`
	}
	doAdmin(t, http.MethodGet, "/admin/v1/zones/default-entry?namespace="+ns, token, nil, http.StatusOK, &out)
	for _, item := range out.Items {
		if item.Group == group && item.Zone == zone && item.DefaultServerID == serverID {
			return
		}
	}
	t.Fatalf("v1 默认入口列表应含 (%s, %s, %s)，实际 %+v", group, zone, serverID, out.Items)
}

// ---- 断言助手 ----

// assertIdentityStatus 断某身份当前状态。
func assertIdentityStatus(t *testing.T, token, identityID, want string) {
	t.Helper()
	detail := identityDetail(t, token, identityID)
	if detail["status"] != want {
		t.Fatalf("身份 %s 状态应为 %s，实际 %v", identityID, want, detail["status"])
	}
}

// assertRezonePrefill 断身份详情带换区预填目标（targetId 为新区）。
func assertRezonePrefill(t *testing.T, token, identityID string, wantZoneID uint) {
	t.Helper()
	detail := identityDetail(t, token, identityID)
	prefill, _ := detail["rezonePrefill"].(map[string]any)
	if prefill == nil || uint(asFloat(prefill["targetId"])) != wantZoneID {
		t.Fatalf("身份 %s 详情应带预填目标 zone=%d，实际 %v", identityID, wantZoneID, detail["rezonePrefill"])
	}
}

// assertZoneTree 断结构树含建好的 cluster/region/zone 且小区计数正确（当前落在 assignedZone、腾空 emptyZone）。
func assertZoneTree(t *testing.T, token string, nsID uint, assignedZone, emptyZone string) {
	t.Helper()
	var tree struct {
		Clusters []struct {
			Name    string `json:"name"`
			Regions []struct {
				Name  string `json:"name"`
				Zones []struct {
					Name        string `json:"name"`
					ServerCount int    `json:"serverCount"`
				} `json:"zones"`
			} `json:"regions"`
		} `json:"clusters"`
	}
	doAdmin(t, http.MethodGet, adminV2+"/zone-tree?namespaceId="+utoa(nsID), token, nil, http.StatusOK, &tree)
	if len(tree.Clusters) != 1 || tree.Clusters[0].Name != clusterName {
		t.Fatalf("zone-tree 应含唯一集群 %s，实际 %+v", clusterName, tree.Clusters)
	}
	regions := tree.Clusters[0].Regions
	if len(regions) != 1 || regions[0].Name != regionName {
		t.Fatalf("zone-tree 集群应含唯一大区 %s，实际 %+v", regionName, regions)
	}
	counts := map[string]int{}
	for _, z := range regions[0].Zones {
		counts[z.Name] = z.ServerCount
	}
	if counts[assignedZone] != 1 || counts[emptyZone] != 0 {
		t.Fatalf("zone-tree 小区计数应 %s=1、%s=0，实际 %v", assignedZone, emptyZone, counts)
	}
}

// identityDetail 取单条身份详情。
func identityDetail(t *testing.T, token, identityID string) map[string]any {
	t.Helper()
	var out map[string]any
	doAdmin(t, http.MethodGet, adminV2+"/agent-identities/"+identityID, token, nil, http.StatusOK, &out)
	return out
}

// serverRowID 取某 namespace 下指定 serverId 的 server 行数字 id。
func serverRowID(t *testing.T, token string, nsID uint, serverID string) uint {
	t.Helper()
	return uint(asFloat(serverRow(t, token, nsID, serverID)["id"]))
}

// serverRow 取某 namespace 下指定 serverId 的 server 富化视图行。
func serverRow(t *testing.T, token string, nsID uint, serverID string) map[string]any {
	t.Helper()
	var out struct {
		Items []map[string]any `json:"items"`
	}
	doAdmin(t, http.MethodGet, adminV2+"/servers?namespaceId="+utoa(nsID), token, nil, http.StatusOK, &out)
	for _, item := range out.Items {
		if item["serverId"] == serverID {
			return item
		}
	}
	t.Fatalf("未找到 server %s：%v", serverID, out.Items)
	return nil
}

// ---- HTTP 与小工具 ----

// doAdmin 发一个带 Bearer 的 admin 请求，校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doAdmin(t *testing.T, method, path, token string, body any, wantStatus int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, beaconURL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// asString 把 JSON 值取为字符串（nil 返回空串），用于比对可空名称字段。
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asFloat 把 JSON 数值取为 float64（JSON 数字统一解析为 float64）。
func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// utoa 把 uint 转十进制字符串（拼 URL / 路径用）。
func utoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// envOr 取环境变量，缺省回退给定默认值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

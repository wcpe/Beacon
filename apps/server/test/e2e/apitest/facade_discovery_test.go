//go:build e2e

// 门面连通性与服务发现的真机断言。
package apitest_e2e

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// assertFacadeConnectivity 验证 Connected/EffectiveMd5/AwaitRegistered 三个门面入口。
//
// 断言的要点不是「最终为 true」，而是：
//   - Connected 必须在在线后为 true（否则门面整体不可信）；
//   - awaitRegistered 返回 true 时**耗时远小于超时上限**——这才能证明它是「立即返回」
//     而不是「卡满超时才侥幸成功」；只断言返回值会漏掉这类性能/死锁缺陷。
func assertFacadeConnectivity(t *testing.T, repoRoot string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-facade-connectivity.log")

	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "FACADE_CONNECTED" && o.field3 == "true"
	}, "门面 connected() 为 true")

	// effectiveMd5：在线且有配置时应非空。这里只断言「被观测到且不抛异常」——
	// 无配置时 EMPTY 是合法状态，不能当作失败。
	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "FACADE_EFFECTIVE_MD5"
	}, "门面 effectiveMd5() 被观测到")

	idx := waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "AWAIT_REGISTERED_OK" && o.field3 == "true"
	}, "awaitRegistered 返回 true")
	assertAwaitFastEnough(t, readObservations(obsPath), idx)

	assertNoErrorSource(t, readObservations(obsPath), "FACADE_ERROR")
}

// assertAwaitFastEnough 断言 awaitRegistered 成功时的耗时不接近超时上限。
//
// 阈值取超时的 1/3：正常注册在秒级完成，若耗时接近上限说明它是在「空转等超时」，
// 属于缺陷（调用方会被无谓阻塞）。留 1/3 余量避免 CI 抖动误报。
func assertAwaitFastEnough(t *testing.T, obs []observation, hitIdx int) {
	t.Helper()
	if hitIdx <= 0 || hitIdx > len(obs) {
		t.Fatalf("观测索引越界：hitIdx=%d，共 %d 行", hitIdx, len(obs))
	}
	line := obs[hitIdx-1]
	elapsed := 0
	timeoutMs := 0
	for _, kv := range strings.Split(line.detail, ";") {
		if strings.HasPrefix(kv, "elapsedMs=") {
			elapsed = atoiSafe(strings.TrimPrefix(kv, "elapsedMs="))
		}
		if strings.HasPrefix(kv, "timeoutMs=") {
			timeoutMs = atoiSafe(strings.TrimPrefix(kv, "timeoutMs="))
		}
	}
	if timeoutMs <= 0 {
		t.Fatalf("观测行缺少 timeoutMs：%s", line.detail)
	}
	if elapsed >= timeoutMs/3 {
		t.Fatalf("awaitRegistered 成功但耗时 %dms 接近超时上限 %dms：说明它在空转等超时而非立即返回（契约要求已注册时立刻返回 true）", elapsed, timeoutMs)
	}
	t.Logf("PASS awaitRegistered 在 %dms 内返回 true（超时上限 %dms，未空转）", elapsed, timeoutMs)
}

// assertDiscovery 验证服务发现，并与 admin 实例视图逐字段比对。
//
// ## 为什么以 query() 为主断言
//
// `instancesInGroup(group)` / `instancesInZone(group, zone)` 都要求传入 group 名，而 group 是
// **控制面解析回填**的（agent 自身不声明 group）。实例刚注册时 group 可能尚未回填，
// 按 group 查询必然空——这是合法时序，不是发现能力缺陷。用 group 维度做主断言会把
// 「还没回填」误判成「查询坏了」，从而掩盖真正的缺陷或产生假失败。
//
// 故主断言用不带约束的 `query()`：等价「本 namespace 内可见实例」，不依赖回填时序，
// 能稳定证明发现通道可用且返回真实实例。group/zone 维度作为补充信息记录，有值即断言。
func assertDiscovery(t *testing.T, repoRoot, base, token string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-discovery.log")

	// 主断言：不带约束的 query 必须返回本服。
	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "DISCOVERY_QUERY" && strings.Contains(o.detail, "serverId="+serverID)
	}, "query() 能取到本服 "+serverID)

	// 与 admin 实例视图比对：取 agent 侧最新一条 QUERY 观测里的本服片段，逐字段核对。
	obs := readObservations(obsPath)
	segment := latestSelfSegment(obs, "DISCOVERY_QUERY")
	if segment == "" {
		t.Fatalf("agent 侧发现结果里找不到 %s 的条目：观测文件=%s", serverID, obsPath)
	}

	admin := adminInstanceView(t, base, token, guard)
	assertDiscoveryMatchesAdmin(t, segment, admin)

	assertNoErrorSource(t, readObservations(obsPath), "DISCOVERY_ERROR")
}

// latestSelfSegment 从某语义类型的观测里取本服那一段字段串（取最新一条非空）。
//
// 单独抽出来是因为：探针每轮都产一条观测，早期的行可能因为实例尚未 online 而为空，
// 直接取第一条会把「启动早期的合法空态」当成最终结论。
func latestSelfSegment(obs []observation, source string) string {
	var segment string
	for _, o := range obs {
		if o.source != source {
			continue
		}
		for _, seg := range strings.Split(o.detail, ";") {
			if strings.Contains(seg, "serverId="+serverID+",") {
				segment = seg
			}
		}
	}
	return segment
}

// adminInstanceView 从 admin 实例视图取该服的期望字段值。
type adminInstance struct {
	serverID    string
	role        string
	address     string
	capacity    int
	playerCount int
}

// adminInstanceView 调 admin 实例接口取该服的权威视图（与 agent discovery 同源）。
func adminInstanceView(t *testing.T, base, token string, guard *harness.GradleProc) adminInstance {
	t.Helper()
	var out struct {
		ServerID    string `json:"serverId"`
		Role        string `json:"role"`
		Address     string `json:"address"`
		Capacity    int    `json:"capacity"`
		PlayerCount int    `json:"playerCount"`
	}
	path := "/admin/v1/instances/" + url.PathEscape(serverID) + "?namespace=" + url.QueryEscape(namespace)
	if err := doAdminJSONAllow(base, "GET", path, token, nil, 200, &out, guard); err != nil {
		t.Fatalf("读取 admin 实例视图失败：%v", err)
	}
	if out.ServerID == "" {
		t.Fatalf("admin 实例视图未返回 %s（可能尚未 online）", serverID)
	}
	return adminInstance{
		serverID: out.ServerID, role: out.Role, address: out.Address,
		capacity: out.Capacity, playerCount: out.PlayerCount,
	}
}

// assertDiscoveryMatchesAdmin 逐字段比对 agent 侧发现结果与 admin 权威视图。
//
// 刻意**不比对 playerCount**：它是易变的事实（玩家进出），两侧采样时刻不同必然抖动，
// 拿它做严格相等会产生假失败。只比对结构性字段（role/address/capacity）。
func assertDiscoveryMatchesAdmin(t *testing.T, agentSegment string, admin adminInstance) {
	t.Helper()
	fields := map[string]string{}
	for _, kv := range strings.Split(agentSegment, ",") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			fields[parts[0]] = parts[1]
		}
	}
	check := func(key, want string) {
		got := fields[key]
		if got != want {
			t.Fatalf("agent 发现结果的 %s 与 admin 视图不一致：agent=%q admin=%q（整段=%s）", key, got, want, agentSegment)
		}
	}
	check("serverId", admin.serverID)
	check("role", admin.role)
	check("address", admin.address)
	check("capacity", itoa(admin.capacity))
	t.Logf("PASS agent 发现结果与 admin 实例视图字段一致：serverId=%s role=%s address=%s capacity=%d",
		admin.serverID, admin.role, admin.address, admin.capacity)
}

// assertRosterDegradesToEmpty 验证无人在线时 roster 返回空而非报错（fail-static 降级形态）。
//
// 独立成函数是因为这条断言的语义与其他不同：它证明的是**降级行为**，
// 而不是「取到了数据」。空名册是合法状态，不得当失败。
func assertRosterDegradesToEmpty(t *testing.T, obs []observation) bool {
	t.Helper()
	for i, o := range filterBySource(obs, "ROSTER_ALL") {
		if o.detail == "EMPTY" {
			t.Logf("PASS 名册在无人在线时返回空 Map（非 null 非异常）：第 %d 条 ROSTER_ALL 观测", i+1)
			return true
		}
	}
	return false
}

// itoa 把 int 转十进制字符串。
func itoa(v int) string {
	return strconv.Itoa(v)
}

// 保证 time 包被使用（waitObservation 的超时来自调用方，此处保留显式引用以免误删）。
var _ = time.Second

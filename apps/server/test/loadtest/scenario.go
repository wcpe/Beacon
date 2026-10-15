//go:build e2e && loadtest

// 端到端 HTTP 压测的场景定义（压测 harness 的「压什么」部分）。
//
// 双 tag（e2e + loadtest）：既复用 e2e tag 下的 harness（BuildBeacon / StartControlPlane / Login），
// 又保证 `go test -tags=e2e ./...` 不会把压测当常规 E2E 顺带跑起来。
//
// 铁律：只压 GET 与幂等写。admin 写端点经 auditWriteMiddleware 逐条落审计，
// 压它们测出来的是「审计写入能力」而非接口能力，还会污染 audit_log 表，故一律不纳入。
package loadtest_e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 场景分组：报告按分组聚合，把「纯内存读 / DB 读 / agent 心跳 / 指标抓取」分开看，
// 避免混合负载把某一类的退化（如连接池等待）在平均值里抹掉。
const (
	groupMemoryRead     = "内存读"
	groupDBRead         = "DB 读"
	groupAgentHeartbeat = "agent 心跳"
	groupMetricsScrape  = "指标抓取"
	// v2 上报 / v2 调度 / 写路径三个分组名在 v2_write_scenario.go 定义：
	// 它们是后加的、依赖运行期身份引导才能构造，与这里的常驻只读场景不同生命周期。
)

// 权重是混合负载的轮转配比（越大越频繁），按真实控制面流量形态给值：
// agent 心跳是最高频的数据面路径，内存快照读是管理台高频路径，Prometheus 抓取是低频旁路。
const (
	weightHeartbeat     = 4
	weightSystemStatus  = 3
	weightTopology      = 3
	weightListServers   = 2
	weightZoneTree      = 2
	weightMetricsScrape = 1
)

// authMode 声明场景的鉴权方式。
type authMode uint8

const (
	// authNone 无鉴权（如未设凭据的抓取端点、以及本地压测用的公开只读端点）。
	authNone authMode = iota
	// authAdminBearer 管理面：Authorization: Bearer <登录令牌>。
	authAdminBearer
	// authAgentToken agent 数据面：X-Beacon-Token: <部署共享 token>。
	authAgentToken
	// authMetricsBearer 抓取端点（独立监听，见 ADR-0090）的可选凭据：Authorization: Bearer <token>。
	// 取值建在 authV2AgentToken 之后：后者由 v2_write_scenario.go 以 `authAgentToken + 1` 占位，
	// 直接插在中间会让两者撞成同一个值（枚举重复，报告里两种鉴权方式会互相冒充）。
	authMetricsBearer authMode = authV2AgentToken + 1
)

// name 返回鉴权方式的中文名（报告里展示）。
func (m authMode) name() string {
	switch m {
	case authNone:
		return "无"
	case authAdminBearer:
		return "admin 令牌"
	case authAgentToken:
		return "agent 共享令牌"
	case authV2AgentToken:
		return "v2 身份令牌"
	case authMetricsBearer:
		return "抓取端点令牌"
	default:
		return "未知"
	}
}

// scrapeMetricsURL 由抓取端点基址拼出完整抓取 URL；基址为空返回空串（表示未启用抓取端点）。
func scrapeMetricsURL(metricsBase string) string {
	if strings.TrimSpace(metricsBase) == "" {
		return ""
	}
	return strings.TrimRight(metricsBase, "/") + "/metrics"
}

// metricsAuthMode 返回抓取场景的鉴权方式（仅用于报告展示）：配了凭据即「抓取端点令牌」，否则「无」。
func metricsAuthMode(metricsBase string) authMode {
	if strings.TrimSpace(metricsBase) == "" || metricsToken() == "" {
		return authNone
	}
	return authMetricsBearer
}

// metricsAuthHeaders 返回抓取场景要预置的请求头。
// 未配凭据时返回 nil（不加任何头）——空 Bearer 会被启用鉴权的端点判为不匹配（401）。
func metricsAuthHeaders() []header {
	token := metricsToken()
	if token == "" {
		return nil
	}
	return []header{{key: "Authorization", value: "Bearer " + token}}
}

// headersToMap 把预置头转成 map；空切片返回 nil，与「不带任何头」语义一致。
// 场景用 []header（热路径不克隆 map），而鉴权风暴用例的 authPath 用 map 承载头，故需要这一层转换。
func headersToMap(hs []header) map[string]string {
	if len(hs) == 0 {
		return nil
	}
	out := make(map[string]string, len(hs))
	for _, h := range hs {
		out[h.key] = h.value
	}
	return out
}

// header 是一条预置请求头。
// 用切片而非 http.Header：压测热路径上无需为每次请求克隆一个 map。
type header struct {
	key   string
	value string
}

// scenario 是一个可被 driver 加权轮转的端点定义：路径 / 方法 / 请求体 / 期望状态码。
// 构建完成后只读，可被多个 worker 并发使用。
type scenario struct {
	// Group / Name 是报告里的分组与端点标识；Path 是用于展示的路径，URL 是构建期拼好的完整地址。
	Group  string
	Name   string
	Path   string
	URL    string
	Method string
	// Weight 是加权轮转权重；WantStatus 是唯一算作「成功」的状态码。
	Weight     int
	WantStatus int
	Auth       authMode
	// headers 是预置鉴权头（心跳另加 Content-Type）。
	headers []header
	// bodies 是按请求序号轮转的请求体集合（无请求体的场景为 nil）。
	// 构建期一次性编码，热路径只做切片取址——心跳靠它把负载摊到一批已注册的虚拟 serverId 上，
	// 避免每次请求都付一次 JSON 编码开销。
	bodies [][]byte
}

// scenarioSet 是绑定运行期凭据后的场景集合。
type scenarioSet struct {
	// items 是全部场景（顺序稳定，供按端点聚合）。
	items []scenario
	// rotation 是权重展开后的下标序列（长度 = 权重之和）。取 seq % len(rotation) 即得
	// 确定性加权轮转：不引入随机数依赖，同一次压测可复现，也不会有 rand 的锁竞争。
	rotation []int
}

// pick 按请求序号取出本轮要打的端点，返回下标（供按端点聚合）与场景本身。
func (s *scenarioSet) pick(seq uint64) (int, *scenario) {
	idx := s.rotation[seq%uint64(len(s.rotation))]
	return idx, &s.items[idx]
}

// scenarioConfig 是构造场景集合所需的运行期凭据（由用例在登录 / 注册前置之后填入）。
type scenarioConfig struct {
	BaseURL        string
	AdminToken     string
	BootstrapToken string
	Namespace      string
	ServerIDs      []string
	// MetricsBase 是抓取端点基址（独立监听，形如 http://127.0.0.1:49152，见 ADR-0090）。
	// 与 BaseURL 是两个不同端口：主端口上不再提供 /metrics，故抓取场景必须走这里。
	// 留空表示调用方不纳入抓取场景——按端点名裁剪后（如只压心跳 / 只读）本项无用。
	MetricsBase string
}

// buildScenarioSet 构造全部压测场景：4 组共 6 个端点。
// 端点取控制面既有路由（见 internal/server/router.go），不新增任何生产代码。
func buildScenarioSet(cfg scenarioConfig) *scenarioSet {
	adminAuth := []header{{key: "Authorization", value: "Bearer " + cfg.AdminToken}}
	agentAuth := []header{
		{key: "X-Beacon-Token", value: cfg.BootstrapToken},
		{key: "Content-Type", value: "application/json"},
	}
	items := []scenario{
		{
			Group: groupMemoryRead, Name: "system_status",
			Path: "/admin/v1/system/status", URL: cfg.BaseURL + "/admin/v1/system/status",
			Method: http.MethodGet, Weight: weightSystemStatus, WantStatus: http.StatusOK,
			Auth: authAdminBearer, headers: adminAuth,
		},
		{
			Group: groupMemoryRead, Name: "topology",
			// topology 必须带 namespace 查询参数（缺失即 400 INVALID_PARAM）；
			// 取虚拟 agent 所在环境，响应里才会带上这批已注册实例，而不是一份空快照。
			Path:   "/admin/v1/topology?namespace=" + cfg.Namespace,
			URL:    cfg.BaseURL + "/admin/v1/topology?namespace=" + cfg.Namespace,
			Method: http.MethodGet, Weight: weightTopology, WantStatus: http.StatusOK,
			Auth: authAdminBearer, headers: adminAuth,
		},
		{
			Group: groupDBRead, Name: "list_servers",
			Path: "/admin/v2/servers", URL: cfg.BaseURL + "/admin/v2/servers",
			Method: http.MethodGet, Weight: weightListServers, WantStatus: http.StatusOK,
			Auth: authAdminBearer, headers: adminAuth,
		},
		{
			Group: groupDBRead, Name: "zone_tree",
			Path: "/admin/v2/zone-tree", URL: cfg.BaseURL + "/admin/v2/zone-tree",
			Method: http.MethodGet, Weight: weightZoneTree, WantStatus: http.StatusOK,
			Auth: authAdminBearer, headers: adminAuth,
		},
		{
			Group: groupAgentHeartbeat, Name: "agent_heartbeat",
			Path: "/beacon/v1/agent/heartbeat", URL: cfg.BaseURL + "/beacon/v1/agent/heartbeat",
			Method: http.MethodPost, Weight: weightHeartbeat, WantStatus: http.StatusOK,
			Auth: authAgentToken, headers: agentAuth,
			bodies: heartbeatBodies(cfg.Namespace, cfg.ServerIDs),
		},
		{
			Group: groupMetricsScrape, Name: "metrics_scrape",
			// 抓取端点已迁到独立监听地址（ADR-0090），主端口上不再有 /metrics；
			// 基址为空（调用方未起抓取端点）时 URL 保持空串，由调用方按端点名裁剪掉本项。
			Path: "/metrics", URL: scrapeMetricsURL(cfg.MetricsBase),
			Method: http.MethodGet, Weight: weightMetricsScrape, WantStatus: http.StatusOK,
			Auth: metricsAuthMode(cfg.MetricsBase),
			// 端点仅可选地校验 Authorization: Bearer；未配置凭据时不带任何头。
			headers: metricsAuthHeaders(),
		},
	}

	set := &scenarioSet{items: items}
	for i := range items {
		for range items[i].Weight {
			set.rotation = append(set.rotation, i)
		}
	}
	return set
}

// rows 导出场景清单供报告展示。
func (s *scenarioSet) rows() []scenarioRow {
	rows := make([]scenarioRow, 0, len(s.items))
	for i := range s.items {
		item := &s.items[i]
		rows = append(rows, scenarioRow{
			Group: item.Group, Name: item.Name, Method: item.Method, Path: item.Path,
			Weight: item.Weight, WantStatus: item.WantStatus, Auth: item.Auth.name(),
		})
	}
	return rows
}

// appendScenarios 把额外场景（v2 上报 / 写路径）追加进集合并重建加权轮转序列。
// 单独抽成方法而不是改 buildScenarioSet 签名：v2 / 写场景依赖运行期引导（namespace 审批、
// 身份 active）才能构造，把它们的入参塞进 buildScenarioSet 会让「只压只读」的老调用点被迫
// 准备一堆用不到的凭据。
func (s *scenarioSet) appendScenarios(extra []scenario) {
	if len(extra) == 0 {
		return
	}
	s.items = append(s.items, extra...)
	for i := len(s.items) - len(extra); i < len(s.items); i++ {
		for range s.items[i].Weight {
			s.rotation = append(s.rotation, i)
		}
	}
}

// heartbeatBodies 预编码每个虚拟 serverId 的心跳请求体（字段与 handler.heartbeatRequest 对齐）。
// map[string]string 的编码不可能失败；真失败即为编程错误，构建期直接 panic，不让它进热路径。
func heartbeatBodies(namespace string, serverIDs []string) [][]byte {
	bodies := make([][]byte, 0, len(serverIDs))
	for _, id := range serverIDs {
		raw, err := json.Marshal(map[string]string{"namespace": namespace, "serverId": id})
		if err != nil {
			panic(fmt.Sprintf("编码心跳请求体失败：%v", err))
		}
		bodies = append(bodies, raw)
	}
	return bodies
}

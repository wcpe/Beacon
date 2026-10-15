package config

import (
	"net"
	"strings"
	"testing"
)

// setAuthEnv 注入合法的鉴权环境变量，避免校验因缺鉴权凭据而失败（鉴权前移后口令/密钥必填）。
func setAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEACON_ADMIN_PASSWORD", "test-pass")
	t.Setenv("BEACON_AUTH_SECRET", "test-secret")
}

// TestLoadDefaults 验证无文件、仅注入必填鉴权凭据时返回内置默认且校验通过。
func TestLoadDefaults(t *testing.T) {
	setAuthEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载默认配置应成功，却报错: %v", err)
	}
	if cfg.HTTPAddr != ":8848" {
		t.Errorf("默认监听地址应为 :8848，实际 %q", cfg.HTTPAddr)
	}
	if cfg.Database.MaxOpenConns != 4 {
		t.Errorf("默认最大连接数应为 4（SQLite WAL 并发读），实际 %d", cfg.Database.MaxOpenConns)
	}
}

// TestEnvOverride 验证环境变量覆盖优先级最高。
func TestEnvOverride(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_HTTP_ADDR", ":9090")
	t.Setenv("BEACON_DB_DSN", "user:pwd@tcp(db:3306)/beacon?parseTime=true&loc=UTC")
	t.Setenv("BEACON_LOG_LEVEL", "DEBUG")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置应成功，却报错: %v", err)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("环境变量未覆盖监听地址，实际 %q", cfg.HTTPAddr)
	}
	if cfg.Log.Level != "DEBUG" {
		t.Errorf("环境变量未覆盖日志级别，实际 %q", cfg.Log.Level)
	}
}

// TestAuthEnvOverride 验证鉴权凭据走环境变量覆盖。
func TestAuthEnvOverride(t *testing.T) {
	t.Setenv("BEACON_ADMIN_USERNAME", "ops")
	t.Setenv("BEACON_ADMIN_PASSWORD", "p@ss")
	t.Setenv("BEACON_AUTH_SECRET", "sig-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置应成功，却报错: %v", err)
	}
	if cfg.Auth.Username != "ops" {
		t.Errorf("环境变量未覆盖操作者用户名，实际 %q", cfg.Auth.Username)
	}
	if cfg.Auth.Password != "p@ss" {
		t.Errorf("环境变量未覆盖操作者口令，实际 %q", cfg.Auth.Password)
	}
	if cfg.Auth.Secret != "sig-key" {
		t.Errorf("环境变量未覆盖签名密钥，实际 %q", cfg.Auth.Secret)
	}
}

// TestValidateRejectsMissingAuthPassword 验证缺操作者口令时校验失败（禁空凭据空跑）。
func TestValidateRejectsMissingAuthPassword(t *testing.T) {
	t.Setenv("BEACON_AUTH_SECRET", "sig-key")
	if _, err := Load(""); err == nil {
		t.Fatal("缺操作者口令应导致校验失败，却通过了")
	}
}

// TestValidateRejectsMissingAuthSecret 验证缺签名密钥时校验失败。
func TestValidateRejectsMissingAuthSecret(t *testing.T) {
	t.Setenv("BEACON_ADMIN_PASSWORD", "test-pass")
	if _, err := Load(""); err == nil {
		t.Fatal("缺签名密钥应导致校验失败，却通过了")
	}
}

// TestValidateRejectsUnknownLogLevel 验证未知日志级别被拒绝。
func TestValidateRejectsUnknownLogLevel(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_LOG_LEVEL", "VERBOSE")
	if _, err := Load(""); err == nil {
		t.Fatal("未知日志级别应导致校验失败，却通过了")
	}
}

// TestDefaultHealthThresholdsValid 默认健康阈值满足 degraded<ttl<offline 序关系（FR-28）。
func TestDefaultHealthThresholdsValid(t *testing.T) {
	setAuthEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("默认配置应通过校验，却报错: %v", err)
	}
	h := cfg.Health
	if h.DegradedAfterSec >= h.TTLSec || h.TTLSec >= h.OfflineGraceSec {
		t.Fatalf("默认阈值序关系不满足：degraded=%d ttl=%d offline=%d", h.DegradedAfterSec, h.TTLSec, h.OfflineGraceSec)
	}
	if cfg.Alert.InboxCapacity <= 0 {
		t.Fatalf("默认站内信容量应为正，实际 %d", cfg.Alert.InboxCapacity)
	}
}

// TestValidateRejectsBadHealthThresholdOrder 阈值序关系错误（degraded>=ttl）应被拒（FR-28）。
func TestValidateRejectsBadHealthThresholdOrder(t *testing.T) {
	setAuthEnv(t)
	cfg := Default()
	cfg.Auth.Password = "p"
	cfg.Auth.Secret = "s"
	cfg.Health.DegradedAfterSec = 40 // 大于 ttl(30)，非法
	if err := cfg.validate(); err == nil {
		t.Fatal("degraded-after-sec 不小于 ttl-sec 应导致校验失败，却通过了")
	}
}

// TestDefaultMetricConfig 验证指标采样默认值：开启、间隔 30s、保留 168h（7d）。
func TestDefaultMetricConfig(t *testing.T) {
	setAuthEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("默认配置应通过校验，却报错: %v", err)
	}
	m := cfg.Metric
	if !m.Enabled {
		t.Fatalf("指标采样默认应开启，实际 %v", m.Enabled)
	}
	if m.SampleIntervalSec != 30 {
		t.Fatalf("默认采样间隔应为 30s，实际 %d", m.SampleIntervalSec)
	}
	if m.RetentionHours != 168 {
		t.Fatalf("默认保留期应为 168h（7d），实际 %d", m.RetentionHours)
	}
}

// TestMetricEnvOverride 验证指标采样配置走环境变量覆盖。
func TestMetricEnvOverride(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_METRIC_ENABLED", "false")
	t.Setenv("BEACON_METRIC_SAMPLE_INTERVAL_SEC", "15")
	t.Setenv("BEACON_METRIC_RETENTION_HOURS", "24")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置应成功，却报错: %v", err)
	}
	if cfg.Metric.Enabled {
		t.Fatalf("环境变量未关闭采样，实际 enabled=%v", cfg.Metric.Enabled)
	}
	if cfg.Metric.SampleIntervalSec != 15 {
		t.Fatalf("环境变量未覆盖采样间隔，实际 %d", cfg.Metric.SampleIntervalSec)
	}
	if cfg.Metric.RetentionHours != 24 {
		t.Fatalf("环境变量未覆盖保留期，实际 %d", cfg.Metric.RetentionHours)
	}
}

// TestValidateRejectsBadMetricConfig 启用采样时，非正的采样间隔或保留期应被拒。
func TestValidateRejectsBadMetricConfig(t *testing.T) {
	setAuthEnv(t)
	base := Default()
	base.Auth.Password = "p"
	base.Auth.Secret = "s"

	cfg := base
	cfg.Metric.SampleIntervalSec = 0
	if err := cfg.validate(); err == nil {
		t.Fatal("启用采样时间隔<=0 应导致校验失败，却通过了")
	}

	cfg = base
	cfg.Metric.RetentionHours = 0
	if err := cfg.validate(); err == nil {
		t.Fatal("启用采样时保留期<=0 应导致校验失败，却通过了")
	}
}

// TestMetricDisabledSkipsValidation 关闭采样时，非正间隔/保留期不触发校验失败（不启用不约束）。
func TestMetricDisabledSkipsValidation(t *testing.T) {
	cfg := Default()
	cfg.Auth.Password = "p"
	cfg.Auth.Secret = "s"
	cfg.Metric.Enabled = false
	cfg.Metric.SampleIntervalSec = 0
	cfg.Metric.RetentionHours = 0
	if err := cfg.validate(); err != nil {
		t.Fatalf("关闭采样时不应校验采样参数，却报错: %v", err)
	}
}

// TestValidateRejectsBadAlertOrphanTimeout 失联孤儿告警自动关闭阈值须为正：
// 0 / 负值会让「已超阈值」判据恒真，外部删除的实例告警当轮即被自动关闭（FR-232 降噪）。
func TestValidateRejectsBadAlertOrphanTimeout(t *testing.T) {
	setAuthEnv(t)
	base := Default()
	base.Auth.Password = "p"
	base.Auth.Secret = "s"
	if base.Alert.OrphanTimeoutHours != 24 {
		t.Fatalf("默认阈值应为 24 小时，实际 %d", base.Alert.OrphanTimeoutHours)
	}
	for _, hours := range []int{0, -1} {
		cfg := base
		cfg.Alert.OrphanTimeoutHours = hours
		if err := cfg.validate(); err == nil {
			t.Fatalf("阈值 %d 应导致校验失败，却通过了", hours)
		}
	}
}

// TestDefaultMetricsEndpointBindsLoopbackOnly 锁定抓取端点的默认监听地址只绑回环（见 ADR-0090）。
//
// 为什么这条是安全底线：抓取端点默认必须**外部不可达**。若默认值被改成 `:8849` / `0.0.0.0:8849`
// （例如有人图方便省掉跨机抓取时的一行配置），所有按默认部署的实例会立刻把组织拓扑与容量画像
// 匿名对外开放——而文档里写着"内网信任面"，部署者不会察觉。
func TestDefaultMetricsEndpointBindsLoopbackOnly(t *testing.T) {
	cfg := Default()
	if cfg.MetricsEndpoint.ListenAddr != "127.0.0.1:8849" {
		t.Fatalf("抓取端点默认监听地址应为 127.0.0.1:8849（仅回环），实际 %q", cfg.MetricsEndpoint.ListenAddr)
	}
	if !isLoopbackListenAddr(cfg.MetricsEndpoint.ListenAddr) {
		t.Fatalf("默认监听地址必须被判定为回环，实际 %q", cfg.MetricsEndpoint.ListenAddr)
	}
	// 凭据默认留空 = 不鉴权；收敛依赖上方的回环地址。两者是配套的，不能只改一个。
	if cfg.MetricsEndpoint.BearerToken != "" {
		t.Fatalf("抓取凭据默认应为空（不鉴权），实际 %q", cfg.MetricsEndpoint.BearerToken)
	}
}

// TestMetricsEndpointEnvOverride 验证监听地址与凭据均可经环境变量注入（凭据不入库 yaml）。
func TestMetricsEndpointEnvOverride(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_METRICS_ENDPOINT_LISTEN_ADDR", "10.0.0.5:9999")
	t.Setenv("BEACON_METRICS_ENDPOINT_BEARER_TOKEN", "tok-123")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.MetricsEndpoint.ListenAddr != "10.0.0.5:9999" {
		t.Errorf("监听地址应被 env 覆盖，实际 %q", cfg.MetricsEndpoint.ListenAddr)
	}
	if cfg.MetricsEndpoint.BearerToken != "tok-123" {
		t.Errorf("凭据应被 env 注入，实际 %q", cfg.MetricsEndpoint.BearerToken)
	}
}

// TestMetricsEndpointEnvCanDisable 验证显式置空可关闭抓取端点。
//
// 必须用 LookupEnv 而非 Getenv 才能区分「未设」与「显式置空」：否则
// `BEACON_METRICS_ENDPOINT_LISTEN_ADDR=""` 会被当成未设、回落到默认回环地址，
// 运维"关掉端点"的意图静默失效——端点仍在监听。
func TestMetricsEndpointEnvCanDisable(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_METRICS_ENDPOINT_LISTEN_ADDR", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("置空关闭端点时仍应加载成功: %v", err)
	}
	if cfg.MetricsEndpoint.ListenAddr != "" {
		t.Fatalf("显式置空应关闭端点，实际却仍为 %q（Getenv 无法区分未设与置空）", cfg.MetricsEndpoint.ListenAddr)
	}
}

// TestValidateRejectsBadMetricsEndpointAddr 非法监听地址应被拒（fail-fast，且原因可读）。
func TestValidateRejectsBadMetricsEndpointAddr(t *testing.T) {
	base := Default()
	base.Auth.Password = "p"
	base.Auth.Secret = "s"

	// 缺端口：裸 IP 会被 net.Listen 拒，但那时已到启动后期，应提前到校验期。
	cfg := base
	cfg.MetricsEndpoint.ListenAddr = "127.0.0.1"
	if err := cfg.validate(); err == nil {
		t.Fatal("缺端口的监听地址应被拒，却通过了")
	}

	// 与主端口相同：后监听者必然 bind 失败，且等于把抓取端点又挂回业务面。
	cfg = base
	cfg.HTTPAddr = ":8848"
	cfg.MetricsEndpoint.ListenAddr = ":8848"
	if err := cfg.validate(); err == nil {
		t.Fatal("监听地址与 http-addr 相同应被拒，却通过了")
	}

	// 置空是合法语义（不启动端点），不得被当成配置错误。
	cfg = base
	cfg.MetricsEndpoint.ListenAddr = ""
	if err := cfg.validate(); err != nil {
		t.Fatalf("置空应表示不启动端点、属合法配置，却报错: %v", err)
	}
}

// TestValidateRejectsEmptyPortMetricsEndpoint 冒号后缺端口号的写法的应被拒（见 ADR-0090）。
//
// 为什么这类写法必须专门锁死：`127.0.0.1:` 与 `:` 都能通过 net.SplitHostPort（err=nil、port=""），
// 因此仅凭 SplitHostPort 是否报错不足以防住它们。放行的后果不是"启动失败"，而是**静默错绑**：
// net.Listen 把端口 0 交给内核，绑到随机端口（实测 127.0.0.1: → 127.0.0.1:36671、: → 0.0.0.0:35289）。
// 抓取地址从此不可预测——Prometheus 按固定端口配好却永远抓不到，看板一片空白，而进程日志只打
// 一行「已就绪 <随机端口>」，很难联想到是配置少写了个数字。更糟的是 `127.0.0.1:` 的 host 仍是
// 回环，非回环告警也不会触发，整个错误完全静默。
//
// 判别力：断言的不只是「报错」，还要求错误文本点明"端口"——否则运维无从知道该改哪里。
func TestValidateRejectsEmptyPortMetricsEndpoint(t *testing.T) {
	base := Default()
	base.Auth.Password = "p"
	base.Auth.Secret = "s"

	for _, addr := range []string{
		"127.0.0.1:",  // 裸回环地址 + 空端口（最可能被误写的一种）
		":",           // 全网卡 + 空端口
		"10.0.0.5:",   // 内网地址 + 空端口
		"[::1]:",      // IPv6 回环 + 空端口
		"localhost: ", // 空白端口（TrimSpace 后为空，与空端口同义）
	} {
		t.Run(addr, func(t *testing.T) {
			cfg := base
			cfg.MetricsEndpoint.ListenAddr = addr
			err := cfg.validate()
			if err == nil {
				t.Fatalf("端口为空的监听地址 %q 应被拒（否则内核会分配随机端口、抓取地址不可预测），却通过了", addr)
			}
			// 报错须点出"端口"二字：只说"须为 host:port 形式"会让人以为格式没错而反复排查。
			if !strings.Contains(err.Error(), "端口") {
				t.Fatalf("拒绝 %q 的报错应说明是端口问题，实际: %v", addr, err)
			}
		})
	}

	// 反面对照：带真实端口的写法不得被这条新检查误伤。
	for _, addr := range []string{"127.0.0.1:8849", ":8849", "[::1]:8849", "10.0.0.5:1"} {
		cfg := base
		cfg.MetricsEndpoint.ListenAddr = addr
		if err := cfg.validate(); err != nil {
			t.Fatalf("带端口的合法监听地址 %q 不应被拒，却报错: %v", addr, err)
		}
	}
}

// TestEmptyPortAddressWouldBindRandomPort 用真实的 net.Listen 交叉印证上一条用例的前提。
//
// 为什么要把标准库的行为也钉下来：上一条用例的正当性完全建立在「空端口 = 内核分配随机端口」
// 这个事实上。若将来有人觉得那条检查多余（"SplitHostPort 都没报错，凭什么拒？"），本用例
// 直接给出实测反例——不是推测，而是真的绑到了一个非预期端口。固定住事实，讨论才不会退回去。
func TestEmptyPortAddressWouldBindRandomPort(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:", ":"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue // 某些平台/内核可能直接拒绝，那同样说明该写法不可用，无需断言
		}
		bound := ln.Addr().String()
		_ = ln.Close()
		// 绑定成功的地址必然带一个内核选出的非零端口，且与配置里的空端口不同。
		if bound == addr {
			t.Fatalf("net.Listen(%q) 竟原样绑定 %q——若标准库将来改成拒绝空端口，本用例与上一条检查都可简化", addr, bound)
		}
		t.Logf("net.Listen(%q) 实际绑到 %s（证明空端口会落到随机端口，抓取地址不可预测）", addr, bound)
	}
}

// TestMetricsEndpointNonLoopbackWithoutTokenAllowed 非回环且无凭据仍应放行（只告警不阻断）。
//
// 该组合有正当场景（内网可信域部署），且"网段是否可信"取决于防火墙，不是控制面能判定的。
// 校验层只负责让运维明确知道风险，不替他做部署决策。
func TestMetricsEndpointNonLoopbackWithoutTokenAllowed(t *testing.T) {
	cfg := Default()
	cfg.Auth.Password = "p"
	cfg.Auth.Secret = "s"
	cfg.MetricsEndpoint.ListenAddr = "10.0.0.5:8849"
	cfg.MetricsEndpoint.BearerToken = ""
	if err := cfg.validate(); err != nil {
		t.Fatalf("非回环无凭据应放行（仅告警），却报错: %v", err)
	}
}

// TestIsLoopbackListenAddr 覆盖各种监听写法的回环判定。
//
// 漏判方向不对称，必须逐条锁死：把「其实已对外」误判成回环，会让告警静默失效（比不判定更糟）；
// 把回环误判成对外，只是多一条无害的 WARN。故未知写法一律保守视为非回环。
func TestIsLoopbackListenAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8849", true},
		{"localhost:8849", true},
		{"[::1]:8849", true},
		{"LOCALHOST:8849", true},    // 大小写不敏感
		{"127.0.0.1:1", true},       // 端口无关
		{":8849", false},            // 所有网卡（Go 里省略 host 即全部接口）
		{"0.0.0.0:8849", false},     // 所有网卡（显式）
		{"[::]:8849", false},        // 所有网卡（IPv6 通配）
		{"10.0.0.5:8849", false},    // 内网地址
		{"192.168.1.7:8849", false}, // 内网地址
		{"example.com:8849", false}, // 域名
		{"", false},                 // 非法（保守）
		{"garbage", false},          // 非法（保守）
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackListenAddr(tc.addr); got != tc.want {
				t.Fatalf("isLoopbackListenAddr(%q) 应为 %v，实际 %v", tc.addr, tc.want, got)
			}
		})
	}
}

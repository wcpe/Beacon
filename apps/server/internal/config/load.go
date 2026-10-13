package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load 按"默认 → yaml 文件 → 环境变量"的顺序加载配置并校验。
// path 指向可选的 yaml 文件；文件不存在时忽略，仅用默认值与环境变量。
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return Config{}, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
			}
		case os.IsNotExist(err):
			// 文件不存在属正常（容器内常仅靠环境变量），忽略即可
		default:
			return Config{}, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv 用环境变量覆盖配置；变量名与 .env.example 对齐，敏感项走环境注入。
func applyEnv(cfg *Config) {
	if v := os.Getenv("BEACON_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("BEACON_BOOTSTRAP_TOKEN"); v != "" {
		cfg.AgentToken = v
	}
	if v := os.Getenv("BEACON_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
	}
	if v := os.Getenv("BEACON_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	// 归档库（FR-151，见 ADR-0066）：DSN 含凭据为敏感项，仅从 env 注入、不入库 yaml；库名可 env 覆盖
	if v := os.Getenv("BEACON_ARCHIVE_DSN"); v != "" {
		cfg.Archive.DSN = v
	}
	if v := os.Getenv("BEACON_ARCHIVE_DATABASE"); v != "" {
		cfg.Archive.Database = v
	}
	if v := os.Getenv("BEACON_ADMIN_USERNAME"); v != "" {
		cfg.Auth.Username = v
	}
	if v := os.Getenv("BEACON_ADMIN_PASSWORD"); v != "" {
		cfg.Auth.Password = v
	}
	if v := os.Getenv("BEACON_AUTH_SECRET"); v != "" {
		cfg.Auth.Secret = v
	}
	if v := os.Getenv("BEACON_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	// 指标采样（FR-32）：布尔显式解析（区分"未设"与"显式 false"），间隔/保留期整数解析
	if v := os.Getenv("BEACON_METRIC_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Metric.Enabled = b
		}
	}
	if v := os.Getenv("BEACON_METRIC_SAMPLE_INTERVAL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Metric.SampleIntervalSec = n
		}
	}
	if v := os.Getenv("BEACON_METRIC_RETENTION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Metric.RetentionHours = n
		}
	}
	// Prometheus 抓取端点（见 ADR-0090）：监听地址可 env 覆盖；凭据为敏感项，仅从 env 注入、不入库 yaml。
	// 注意这里区分「未设」与「显式置空」：只有环境变量**存在**时才覆盖，故用 LookupEnv 而非 Getenv——
	// 否则运维无法用空值把端点关掉（`BEACON_..._LISTEN_ADDR=""` 会被当未设而保留默认回环地址）。
	if v, ok := os.LookupEnv("BEACON_METRICS_ENDPOINT_LISTEN_ADDR"); ok {
		cfg.MetricsEndpoint.ListenAddr = v
	}
	if v := os.Getenv("BEACON_METRICS_ENDPOINT_BEARER_TOKEN"); v != "" {
		cfg.MetricsEndpoint.BearerToken = v
	}
	// git 单向导出（FR-47）：开关布尔显式解析，仓路径 / 远程地址 / 分支字符串覆盖；
	// 远程凭据为敏感项，仅从 env 注入、不入库 yaml（rule #14）
	if v := os.Getenv("BEACON_GIT_EXPORT_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.GitExport.Enabled = b
		}
	}
	if v := os.Getenv("BEACON_GIT_EXPORT_REPO_PATH"); v != "" {
		cfg.GitExport.RepoPath = v
	}
	if v := os.Getenv("BEACON_GIT_EXPORT_REMOTE_URL"); v != "" {
		cfg.GitExport.RemoteURL = v
	}
	if v := os.Getenv("BEACON_GIT_EXPORT_REMOTE_BRANCH"); v != "" {
		cfg.GitExport.RemoteBranch = v
	}
	if v := os.Getenv("BEACON_GIT_EXPORT_REMOTE_TOKEN"); v != "" {
		cfg.GitExport.RemoteToken = v
	}
}

// validate 校验关键项，缺失即 fail-fast（中文报错）。
// validateMCP 校验启用 MCP 时的公网基址与可信代理网段（FR-212；内网直连见 FR-222）。
// 拆为独立方法，避免 validate 内嵌套过深触发 nestif 门禁。
func (c Config) validateMCP() error {
	if !c.MCP.Enabled {
		return nil
	}
	base, err := url.Parse(c.MCP.PublicBaseURL)
	schemeOK := err == nil && base.Host != "" && base.Path == "" && base.RawQuery == "" && base.Fragment == ""
	if schemeOK && c.MCP.AllowInsecureInternal {
		// 内网直连模式：允许 http（无 TLS 终止）
		schemeOK = base.Scheme == "https" || base.Scheme == "http"
	} else if schemeOK {
		schemeOK = base.Scheme == "https"
	}
	if !schemeOK {
		if c.MCP.AllowInsecureInternal {
			return fmt.Errorf("配置校验失败: 启用 MCP 时 mcp.public-base-url 必须是 host 非空且无路径的 http(s) 基址")
		}
		return fmt.Errorf("配置校验失败: 启用 MCP 时 mcp.public-base-url 必须是无路径的 HTTPS 公网基址（内网明文请置 mcp.allow-insecure-internal=true）")
	}
	// 直连模式（allow-insecure-internal）允许空 CIDR；其余情况必须显式列出可信代理。
	if len(c.MCP.TrustedProxyCIDRs) == 0 && !c.MCP.AllowInsecureInternal {
		return fmt.Errorf("配置校验失败: 启用 MCP 时 mcp.trusted-proxy-cidrs 不能为空")
	}
	return nil
}

// validateMetricsEndpoint 校验抓取端点监听地址（见 ADR-0090）。
//
// 拆为独立方法，避免 validate 内嵌套过深触发 nestif 门禁。
// 空值合法：语义是「不启动该端点」（显式关闭），不是配置错误。
func (c Config) validateMetricsEndpoint() error {
	addr := strings.TrimSpace(c.MetricsEndpoint.ListenAddr)
	if addr == "" {
		return nil // 显式关闭
	}
	// 必须含端口：裸 IP 或裸主机名会被 net.Listen 拒（`listen tcp: address 10.0.0.5: missing port`），
	// 但那是在启动后期才报，此处提前到配置校验期、并给出可读原因。
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("配置校验失败: metrics-endpoint.listen-addr 须为 host:port 形式（如 127.0.0.1:8849、:8849），实际 %q：%w", addr, err)
	}
	// 与主端口冲突检查：两者若相同，后监听者必然 bind 失败，而失败发生在启动后期、
	// 表现为一个难定位的 "address already in use"。且语义上也危险——那等于把抓取端点
	// 又挂回了业务主端口（正是本次改动要消除的形态），故直接拒绝而非静默接受。
	if addr == strings.TrimSpace(c.HTTPAddr) {
		return fmt.Errorf("配置校验失败: metrics-endpoint.listen-addr 不能与 http-addr 相同（均为 %q）——抓取端点须独立监听，与业务主端口重合等于又把它暴露在业务面上", addr)
	}
	// 未设凭据却在非回环地址上监听 = 把组织拓扑与容量画像匿名对外开放。
	// 不禁止这种配置（确有内网可信域部署的正当场景），但必须让运维明确知道。
	if c.MetricsEndpoint.BearerToken == "" && !isLoopbackListenAddr(addr) {
		slog.Warn("抓取端点监听在非回环地址且未配置凭据，任何可达该地址者都能匿名读取运行指标",
			"监听地址", addr, "建议", "配置 BEACON_METRICS_ENDPOINT_BEARER_TOKEN，或用反向代理限制来源网段")
	}
	return nil
}

// isLoopbackListenAddr 判断监听地址是否只绑定回环（含「只写了端口」与「显式回环」两种写法）。
//
// 为什么要覆盖多种写法：`:8849`（所有网卡）、`0.0.0.0:8849`、`[::]:8849` 都不是回环，而
// `127.0.0.1:8849`、`localhost:8849`、`[::1]:8849` 是。漏判会把「其实已对外」误报成安全，
// 比不判断更糟——故此处按 host 部分白名单判定，未知写法一律视为非回环（保守）。
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
func (c Config) validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("配置校验失败: 监听地址 http-addr 不能为空")
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("配置校验失败: 数据库 dsn 不能为空")
	}
	if strings.TrimSpace(c.Auth.Username) == "" {
		return fmt.Errorf("配置校验失败: 管理面操作者用户名 auth.username 不能为空")
	}
	if strings.TrimSpace(c.Auth.Password) == "" {
		return fmt.Errorf("配置校验失败: 管理面操作者口令不能为空（经环境变量或当前目录 .env 文件的 BEACON_ADMIN_PASSWORD 注入）")
	}
	if strings.TrimSpace(c.Auth.Secret) == "" {
		return fmt.Errorf("配置校验失败: 令牌签名密钥不能为空（经环境变量或当前目录 .env 文件的 BEACON_AUTH_SECRET 注入）")
	}
	switch strings.ToUpper(c.Log.Level) {
	case "ERROR", "WARN", "INFO", "DEBUG":
	default:
		return fmt.Errorf("配置校验失败: 未知日志级别 %q（应为 ERROR/WARN/INFO/DEBUG）", c.Log.Level)
	}
	// 健康阈值须满足 degraded < ttl < offline，否则状态机分档失效（FR-28）
	h := c.Health
	if h.DegradedAfterSec >= h.TTLSec || h.TTLSec >= h.OfflineGraceSec {
		return fmt.Errorf("配置校验失败: 健康阈值须满足 degraded-after-sec(%d) < ttl-sec(%d) < offline-grace-sec(%d)",
			h.DegradedAfterSec, h.TTLSec, h.OfflineGraceSec)
	}
	// 启用指标采样时，采样间隔与保留期须为正（否则定时器/清理 cutoff 无意义，FR-32）；关闭则不约束
	if c.Metric.Enabled {
		if c.Metric.SampleIntervalSec <= 0 {
			return fmt.Errorf("配置校验失败: 启用指标采样时 metric.sample-interval-sec 须为正，实际 %d", c.Metric.SampleIntervalSec)
		}
		if c.Metric.RetentionHours <= 0 {
			return fmt.Errorf("配置校验失败: 启用指标采样时 metric.retention-hours 须为正，实际 %d", c.Metric.RetentionHours)
		}
	}
	// 失联孤儿告警自动关闭阈值须为正：0 / 负值会让「已超阈值」判据恒真，外部删除的实例告警当轮即被自动关闭（FR-232 降噪）
	if c.Alert.OrphanTimeoutHours <= 0 {
		return fmt.Errorf("配置校验失败: alert.orphan-timeout-hours 须为正（单位：小时），实际 %d", c.Alert.OrphanTimeoutHours)
	}
	if err := c.validateMCP(); err != nil {
		return err
	}
	if err := c.validateMetricsEndpoint(); err != nil {
		return err
	}
	// 连接等待预算须小于事务寿命预算（见 store 的连接等待防护）：
	// 二者的关系是「先花 call 抢连接，抢到后事务最多活 tx」。
	//
	// **判据必须用生效值而非配置原值**（code review P1-2）：`0` 在 store 侧表示「取内置默认」
	// （resolveBudget），而字段缺省与显式 0 在解析后不可区分——若按原值判 `> 0`，
	// 则 `call=0 + tx=3000` 会通过校验，运行时却生效为 `call=5000 > tx=3000`，
	// 恰好落进本校验要防的「事务刚开起来就超预算」形态。故此处先把 0 规范化成默认值再比较。
	callMs := resolveCallBudgetMs(c.Database.CallTimeoutMs)
	txMs := resolveTxBudgetMs(c.Database.TxTimeoutMs)
	if callMs > 0 && txMs > 0 && callMs >= txMs {
		return fmt.Errorf("配置校验失败: database.call-timeout-ms(生效 %d) 须小于 tx-timeout-ms(生效 %d)（前者管等连接、后者管事务寿命；0 表示取内置默认 %d/%d）",
			callMs, txMs, DefaultDatabaseCallTimeoutMs, DefaultDatabaseTxTimeoutMs)
	}
	// 机器注册通道（FR-222）：开启即把 agent 共享 token 升级为安全边界（持有即受信内部调用方），
	// 故必须显式换为强随机值——留空或仍是出厂默认值一律拒绝启动（fail-fast，避免弱 token 直通注册）。
	if c.MCP.AllowMachineRegister && isWeakAgentToken(c.AgentToken) {
		return fmt.Errorf("配置校验失败: 开启 allow-machine-register 时 agent-token 必须改为强随机值（当前为留空或出厂默认值）")
	}
	return nil
}

// weakAgentTokens 是已知的弱 / 出厂默认 agent 共享 token（内置默认、配置样例默认与 .env.example 占位值）。
// 机器注册通道开启时持有该 token 等价于受信调用方，故这些值必须被拒绝。
// 键一律小写：比对前会把候选值规范化小写（见 isWeakAgentToken），防止大小写变体绕过。
var weakAgentTokens = map[string]struct{}{
	"":                   {}, // 留空：无从比对，等于无门禁
	DefaultAgentToken:    {}, // 内置默认（config.Default()）
	ExampleAgentToken:    {}, // 配置样例默认（config.example.yml 与 agent 样例开箱匹配值）
	EnvExampleAgentToken: {}, // .env.example 占位值（照抄 .env 即带入，公开已知）
}

// isWeakAgentToken 判断 agent 共享 token 是否属于须拒绝的弱值。
// 比对前做「去空白 + 转小写」规范化：空白等价留空，大小写变体（如 CHANGE-ME）也不得绕过；
// 强随机 token 不可能规范化后等于名单中的弱值，故不会误伤。
func isWeakAgentToken(token string) bool {
	_, weak := weakAgentTokens[strings.ToLower(strings.TrimSpace(token))]
	return weak
}

// resolveCallBudgetMs / resolveTxBudgetMs 把配置原值规范化为**生效值**（毫秒），
// 与 store 侧 resolveBudget 的语义一致：>0 用原值、<0 表示关闭（返回 0）、0 取内置默认。
//
// 为什么不复用 store 的 resolveBudget：store 依赖本包（构造 DatabaseConfig），反向 import 会成环。
// 两处口径须同步——store 的 resolveBudget 与这里的规范化是同一语义，改动一方时必须同时改另一方。
func resolveCallBudgetMs(configuredMs int) int {
	switch {
	case configuredMs > 0:
		return configuredMs
	case configuredMs < 0:
		return 0
	default:
		return DefaultDatabaseCallTimeoutMs
	}
}

func resolveTxBudgetMs(configuredMs int) int {
	switch {
	case configuredMs > 0:
		return configuredMs
	case configuredMs < 0:
		return 0
	default:
		return DefaultDatabaseTxTimeoutMs
	}
}

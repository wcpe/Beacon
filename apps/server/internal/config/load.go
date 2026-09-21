package config

import (
	"fmt"
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
	if c.MCP.Enabled {
		base, err := url.Parse(c.MCP.PublicBaseURL)
		if err != nil || base.Scheme != "https" || base.Host == "" || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
			return fmt.Errorf("配置校验失败: 启用 MCP 时 mcp.public-base-url 必须是无路径的 HTTPS 公网基址")
		}
		if len(c.MCP.TrustedProxyCIDRs) == 0 {
			return fmt.Errorf("配置校验失败: 启用 MCP 时 mcp.trusted-proxy-cidrs 不能为空")
		}
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

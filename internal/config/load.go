package config

import (
	"fmt"
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
	if !(h.DegradedAfterSec < h.TTLSec && h.TTLSec < h.OfflineGraceSec) {
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
	return nil
}

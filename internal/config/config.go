package config

// Config 是 Beacon 控制面自身的运行配置（非"配置中心"业务配置）。
// 加载顺序：内置默认 → 可选 yaml 文件 → 环境变量覆盖（见 load.go）。
type Config struct {
	// API 与管理台 UI 的监听地址（二者同端口），如 ":8848"
	HTTPAddr string `yaml:"http-addr"`
	// agent 端共享 token，仅防误连（非安全边界，鉴权属 P2）
	AgentToken string `yaml:"agent-token"`
	// 配置/版本/分配/审计的权威库连接
	Database DatabaseConfig `yaml:"database"`
	// 管理面鉴权（操作者认证 + 令牌，见 ADR-0009）
	Auth AuthConfig `yaml:"auth"`
	// 注册健康相关参数
	Health HealthConfig `yaml:"health"`
	// 健康告警相关参数（站内信 + webhook，FR-28）
	Alert AlertConfig `yaml:"alert"`
	// 负载指标采样相关参数（采样落库 + 保留期清理，FR-32）
	Metric MetricConfig `yaml:"metric"`
	// 长轮询相关参数
	Longpoll LongpollConfig `yaml:"longpoll"`
	// git 单向导出镜像（备份 / 灾备 / 外部可见，FR-47）
	GitExport GitExportConfig `yaml:"git-export"`
	// 日志配置
	Log LogConfig `yaml:"log"`
}

// GitExportConfig 是 git 单向导出镜像配置（FR-47，见 ADR-0030）。
// 发布 / 回滚事务提交后异步 best-effort 把配置 / 文件树源层导出 commit 到本地裸仓、可选推送远程；
// git 仓是单向派生镜像、不作第二真源，失败仅告警不阻断发布。远程凭据走 env、不写入库 yaml。
type GitExportConfig struct {
	// 是否启用导出；false 时完全不导出（默认 false，属可选增强）
	Enabled bool `yaml:"enabled"`
	// 本地 git 仓路径（导出 commit 落此目录；相对路径相对进程工作目录）
	RepoPath string `yaml:"repo-path"`
	// 可选远程推送地址（GitHub/Gitea，空则只本地 commit 不推送）
	RemoteURL string `yaml:"remote-url"`
	// 远程推送分支
	RemoteBranch string `yaml:"remote-branch"`
	// commit 作者名（仅 git 提交身份元数据，非鉴权）
	AuthorName string `yaml:"author-name"`
	// commit 作者邮箱（仅 git 提交身份元数据，非鉴权）
	AuthorEmail string `yaml:"author-email"`
	// 远程推送凭据（token / 密码）：敏感项，仅从 env BEACON_GIT_EXPORT_REMOTE_TOKEN 注入，禁写入库 yaml
	RemoteToken string `yaml:"-"`
}

// AuthConfig 是管理面鉴权配置（单操作者模型，非 RBAC）。
// 口令与签名密钥为敏感项，走环境变量注入，禁写入入库 yaml、禁硬编码。
type AuthConfig struct {
	// 管理台操作者用户名
	Username string `yaml:"username"`
	// 管理台操作者口令（走 env BEACON_ADMIN_PASSWORD）
	Password string `yaml:"password"`
	// 令牌 HMAC 签名密钥（走 env BEACON_AUTH_SECRET）
	Secret string `yaml:"secret"`
	// 登录令牌有效期（秒）
	TokenTTLSec int `yaml:"token-ttl-sec"`
}

// LongpollConfig 是配置长轮询配置。
type LongpollConfig struct {
	// 服务端挂起上限（毫秒）；实际取 min(客户端 timeoutMs, 此值)
	MaxHoldMs int `yaml:"max-hold-ms"`
}

// HealthConfig 是注册/心跳/健康判活配置。
type HealthConfig struct {
	// 下发给 agent 的心跳周期（秒）
	HeartbeatIntervalSec int `yaml:"heartbeat-interval-sec"`
	// 超过多少秒未收到心跳即判亚健康（online→degraded）；须小于 ttl-sec（FR-28）
	DegradedAfterSec int `yaml:"degraded-after-sec"`
	// 超过多少秒未收到心跳即判失联（degraded→lost）
	TTLSec int `yaml:"ttl-sec"`
	// lost 后多久转 offline（秒）
	OfflineGraceSec int `yaml:"offline-grace-sec"`
	// 后台健康扫描周期（秒）
	ScanIntervalSec int `yaml:"scan-interval-sec"`
}

// AlertConfig 是健康告警配置（告警通道可扩展，第一版站内信 + webhook，见 ADR-0019）。
type AlertConfig struct {
	// 站内信保留的最近告警条数（进程内环形缓存，重启清零）
	InboxCapacity int `yaml:"inbox-capacity"`
	// webhook 告警通道配置
	Webhook WebhookConfig `yaml:"webhook"`
}

// WebhookConfig 是 webhook 告警通道配置。
type WebhookConfig struct {
	// 告警 POST 目标 URL；为空则不启用 webhook 通道
	URL string `yaml:"url"`
	// 单次 webhook 请求超时（毫秒）
	TimeoutMs int `yaml:"timeout-ms"`
}

// MetricConfig 是负载指标采样配置（FR-32，见 ADR-0023）。
// 控制面按间隔对在线实例采样落 metric_sample 形成历史趋势，并按保留期滚动清理过期样本。
type MetricConfig struct {
	// 是否启用采样器；false 时不采样、不清理（仅实时聚合端点仍可用）
	Enabled bool `yaml:"enabled"`
	// 采样间隔（秒）：每隔多少秒对在线实例采一次样落库；启用时须为正
	SampleIntervalSec int `yaml:"sample-interval-sec"`
	// 保留期（小时）：早于 now-本值的样本被滚动清理，控制表体量；启用时须为正
	RetentionHours int `yaml:"retention-hours"`
}

// DatabaseConfig 是数据库连接与连接池配置。
type DatabaseConfig struct {
	// 数据库驱动：mysql 或 sqlite；默认 sqlite（本地开发零依赖）
	Driver string `yaml:"driver"`
	// GORM DSN；切 Postgres 时只改 driver 与此串，业务代码零改
	DSN string `yaml:"dsn"`
	// 连接池最大打开连接数
	MaxOpenConns int `yaml:"max-open-conns"`
	// 连接池最大空闲连接数
	MaxIdleConns int `yaml:"max-idle-conns"`
	// 单个连接最大存活秒数
	ConnMaxLifetimeSec int `yaml:"conn-max-lifetime-sec"`
}

// LogConfig 是日志配置。
type LogConfig struct {
	// 日志级别：ERROR / WARN / INFO / DEBUG
	Level string `yaml:"level"`
}

// Default 返回内置默认配置（本地开发可直接使用）。
func Default() Config {
	return Config{
		HTTPAddr:   ":8848",
		AgentToken: "change-me",
		Database: DatabaseConfig{
			Driver:             "sqlite",
			DSN:                "beacon.db",
			MaxOpenConns:       1,
			MaxIdleConns:       1,
			ConnMaxLifetimeSec: 1800,
		},
		Auth: AuthConfig{
			// 用户名给默认值；口令与签名密钥默认空，必须经 env 注入（禁空凭据空跑）
			Username:    "admin",
			TokenTTLSec: 86400,
		},
		Health: HealthConfig{
			HeartbeatIntervalSec: 10,
			DegradedAfterSec:     15,
			TTLSec:               30,
			OfflineGraceSec:      120,
			ScanIntervalSec:      5,
		},
		Alert: AlertConfig{
			InboxCapacity: 200,
			Webhook:       WebhookConfig{URL: "", TimeoutMs: 3000},
		},
		Metric: MetricConfig{
			Enabled:           true,
			SampleIntervalSec: 30,  // 默认 30s 采样，约 50 服规模下单表 + 保留期清理足够
			RetentionHours:    168, // 默认保留 7 天（168h）
		},
		Longpoll: LongpollConfig{MaxHoldMs: 30000},
		GitExport: GitExportConfig{
			// 默认关闭：属可选增强，开启需运维显式配置仓路径 / 远程
			Enabled:      false,
			RepoPath:     "beacon-config-export",
			RemoteURL:    "",
			RemoteBranch: "main",
			AuthorName:   "beacon",
			AuthorEmail:  "beacon@local",
		},
		Log: LogConfig{Level: "INFO"},
	}
}

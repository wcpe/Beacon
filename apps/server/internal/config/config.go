package config

// Config 是 Beacon 控制面自身的运行配置（非"配置中心"业务配置）。
// 加载顺序：内置默认 → 可选 yaml 文件 → 环境变量覆盖（见 load.go）。
type Config struct {
	// API 与管理台 UI 的监听地址（二者同端口），如 ":8848"
	HTTPAddr string `yaml:"http-addr"`
	// agent 端**共享 token**（易与 agent 本地键 beacon.bootstrap-token 混淆，见 config.example.yml 的说明）：
	// 它是 **v1 数据面**端点（现名 /beacon/v1/agent/data-plane/attach）的凭据，也是机器注册（FR-222）
	// 的唯一信任源（匹配即「受信内部调用方」，开启 mcp.allow-machine-register 后可直落 active）。
	// 与 v2 身份注册端点使用的 **namespace token** 是两套不同凭据：后者按 namespace 表哈希校验并与
	// namespace 绑定；用本项去打 v2 必然 401（用错通道是常见故障，见 docs/OPERATIONS.md §9）。
	// 默认仅防误连（非安全边界）；开启 mcp.allow-machine-register 后升级为安全边界，启动校验强制强随机值。
	AgentToken string `yaml:"agent-token"`
	// 配置/版本/分配/审计的权威库连接
	Database DatabaseConfig `yaml:"database"`
	// 热冷归档库连接（FR-151，见 ADR-0066）：第二个独立 DB 连接，承载到期数据的冷库
	Archive ArchiveConfig `yaml:"archive"`
	// 管理面鉴权（操作者认证 + 令牌，见 ADR-0009）
	Auth AuthConfig `yaml:"auth"`
	// MCP 公网协议入口；TLS 由受信反向代理终止，未显式启用时入口失败关闭。
	MCP MCPConfig `yaml:"mcp"`
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
	// 控制面在线更新相关参数（FR-98 起，含出站代理）
	Update UpdateConfig `yaml:"update"`
	// 日志配置
	Log LogConfig `yaml:"log"`
}

// MCPConfig 是 MCP 入口的固定部署信任边界。
type MCPConfig struct {
	Enabled bool `yaml:"enabled"`
	// PublicBaseURL 入口基址。默认要求无路径的 HTTPS 公网基址；
	// 置 allow-insecure-internal=true 时放宽为 http，供内网直连（无反向代理）部署。
	PublicBaseURL string `yaml:"public-base-url"`
	// TrustedProxyCIDRs 可信反向代理网段（CIDR）。经反代部署时必填且非空；
	// allow-insecure-internal=true 且留空时视为直连模式，此时跳过 X-Forwarded-* 校验。
	TrustedProxyCIDRs []string `yaml:"trusted-proxy-cidrs"`
	// AllowInsecureInternal 允许内网明文 HTTP 直连 MCP 入口（无 TLS 终止、无反向代理）。
	// 仅供内网/回环部署；公网环境必须保持 false。
	AllowInsecureInternal bool `yaml:"allow-insecure-internal"`
	// AllowedHosts 直连模式下允许的 Host 头白名单（host 或 host:port），用于防 DNS rebinding。
	// 留空时仅放行与 public-base-url 的 host 完全一致的 Host；因此内网直连部署若要用
	// 127.0.0.1 / localhost 等本机地址访问 MCP，必须把它们显式列入本白名单。
	AllowedHosts []string `yaml:"allowed-hosts"`
	// AllowApprovalDecide 允许 automation 客户端执行审批决定（默认 false）。
	// 默认关闭以保持"审批决定权归人类"的分权设计；仅内网单操作者部署可显式开启闭环自动化。
	AllowApprovalDecide bool `yaml:"allow-approval-decide"`
	// AllowMachineRegister 允许受信内部调用方（命中 X-Beacon-Token 共享 token 的请求）机器化注册 agent：
	// 注册直接落 active 并绑定 serverId，跳过人工审批（FR-222，见 specs/internal-trust-channel.md）。
	// 默认 false —— 关闭时行为与既有分权设计完全一致（一律落 pending 待人工确认）。
	// 开启即把共享 token 升级为安全边界，故启动校验强制要求 agent-token 必须为强随机值（禁默认值）；
	// 仅内网单操作者部署可开启，公网部署必须保持 false。
	AllowMachineRegister bool `yaml:"allow-machine-register"`

	// ProductionMode 生产模式（FR-237）：开启后 MCP 面隐藏 critical 风险等级的工具，
	// 即不可逆（墓碑化删除）、影响控制面自身（自更新 / 系统设置）或可造成权限提升
	// （凭据签发轮换、机器自批审批）的那一档；low / high 不受影响。
	// 生产部署建议开启；默认 false，行为与既有完全一致。
	ProductionMode bool `yaml:"production-mode"`
}

// agent 共享 token 的已知弱默认值：机器注册通道（FR-222）开启时启动校验一律拒绝它们。
// 这些都是仓库里公开已知的弱口令，仅防误连；升级为安全边界前必须显式换为强随机值。
const (
	// DefaultAgentToken 是内置默认值（config.Default()）。
	DefaultAgentToken = "change-me"
	// ExampleAgentToken 是配置样例（config.example.yml）与 agent 样例开箱匹配的默认值。
	ExampleAgentToken = "beacon-bootstrap-token"
	// EnvExampleAgentToken 是 .env.example 的占位值——照抄 .env 时会原样带入，
	// 同属公开已知的弱值，须与上述默认值一并拒绝。
	EnvExampleAgentToken = "change-me-bootstrap-token"
)

// UpdateConfig 是控制面在线更新配置（FR-98 起，见 ADR-0047）。
// 全部字段均为热改项（首启种子 + DB store 真源），config.yml 仅作出厂默认。
type UpdateConfig struct {
	// 更新出站代理地址（http://host:port 或 https://...，可含 user:pass）；留空=直连。
	// 仅作用于更新检查 / 下载出站，不影响 webhook（FR-98）。热改项首启种子，运行真源在设置 store。
	ProxyURL string `yaml:"proxy-url"`
	// 更新渠道：stable（正式版）/ rc（预发布版）。决定查 Release 取哪条线（FR-101，被 FR-99 消费）。
	Channel string `yaml:"channel"`
	// 是否启用自动检查更新（FR-101）；false 时不后台轮询、仅手动检查。
	AutoCheckEnabled bool `yaml:"auto-check-enabled"`
	// 自动检查更新周期（小时）：每隔多少小时查一次有无新版本（FR-101，下界 1 上界 168）。
	CheckIntervalHours int `yaml:"check-interval-hours"`
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
	// 失联孤儿告警自动关闭阈值（小时）：实例既不在运行时注册表、也不在 server 表活动目录（生命周期非 active），
	// 且其未处理告警的最近触发已超此时长，才由后台清理器自动消解。热改项首启种子（真源在设置 store，FR-61）
	OrphanTimeoutHours int `yaml:"orphan-timeout-hours"`
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
	// 单条语句 / 等待连接的预算（毫秒）。池被占满时以此为上限快速失败，而不是无声永久挂起
	// （见 store 的连接等待防护）。<=0 表示显式关闭该预算（退回无上限等待，不推荐）。
	// 须小于 TxTimeoutMs，否则事务还没开起来就该被掐断。
	CallTimeoutMs int `yaml:"call-timeout-ms"`
	// 单个事务的寿命预算（毫秒，从事务拿到连接算起）。<=0 表示显式关闭该预算（不推荐）。
	// 必须显著大于最长正常事务——本仓最长是文件批量导入，实测约 8.5s，
	// 若与 CallTimeoutMs 取同值会误杀正常导入。
	TxTimeoutMs int `yaml:"tx-timeout-ms"`
}

// 连接等待与事务寿命的默认预算（毫秒）。定义在 config 而非 store，是因为 store 依赖 config，
// 反向引用会成环；两处需要同一个数时以这里为唯一真源。
const (
	// DefaultDatabaseCallTimeoutMs 是单条语句 / 取连接的默认预算。
	// 取值依据：业务事务实测中位 1.482ms / p95 3.817ms / 最大 11.301ms，5s 有两个数量级余量，
	// 真被耗尽时必是池枯竭而非正常抖动。
	DefaultDatabaseCallTimeoutMs = 5000
	// DefaultDatabaseTxTimeoutMs 是事务寿命的默认预算。
	// 取值依据：最长事务（导入 2000 文件）实测 8.5s，60s 留足 7 倍余量；同时仍是有限值，
	// 保证事务一旦卡死也会在 60s 内释放连接，而不是永久占用。
	DefaultDatabaseTxTimeoutMs = 60000
)

// ArchiveConfig 是热冷归档库连接配置（FR-151，见 ADR-0066）。
// 归档库是控制面的第二个独立 DB 连接（非跨库 SQL）：sqlite=第二个文件、mysql=同实例第二个 database。
// 属启动配置——含凭据的 DSN 走 env 覆盖、不入库 yaml 明文，改后须重启（仅保留期 / 调度 / 批量参数走设置 store 热更）。
type ArchiveConfig struct {
	// 独立归档库 DSN；留空 = 与主库同实例模式（复用主库连接参数、仅替换库名为 database）。
	// 非空 = 独立库模式，归档写入与冷查询全部路由该 DSN、database 忽略。含凭据走 env BEACON_ARCHIVE_DSN 注入。
	DSN string `yaml:"dsn"`
	// 同实例模式下的归档库名（mysql=同实例第二个 database、sqlite=同目录第二个 .db 文件名前缀）；默认 beacon_archive。
	Database string `yaml:"database"`
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
			MaxOpenConns:       4,
			MaxIdleConns:       2,
			ConnMaxLifetimeSec: 1800,
			CallTimeoutMs:      DefaultDatabaseCallTimeoutMs,
			TxTimeoutMs:        DefaultDatabaseTxTimeoutMs,
		},
		// 归档库默认同实例模式（dsn 留空）、库名 beacon_archive（FR-151，见 ADR-0066）。
		Archive: ArchiveConfig{
			DSN:      "",
			Database: "beacon_archive",
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
			// 默认 24 小时：给「实例被外部删除后又被重新纳管」留出一个完整观察窗（FR-232 降噪）。
			OrphanTimeoutHours: 24,
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
		// 更新：默认空代理=直连（FR-98）；默认 stable 渠道、开自动检查、6 小时一查（FR-101）
		Update: UpdateConfig{ProxyURL: "", Channel: "stable", AutoCheckEnabled: true, CheckIntervalHours: 6},
		Log:    LogConfig{Level: "INFO"},
	}
}

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
	// 长轮询相关参数
	Longpoll LongpollConfig `yaml:"longpoll"`
	// 日志配置
	Log LogConfig `yaml:"log"`
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
	// 超过多少秒未收到心跳即判失联（online→lost）
	TTLSec int `yaml:"ttl-sec"`
	// lost 后多久转 offline（秒）
	OfflineGraceSec int `yaml:"offline-grace-sec"`
	// 后台健康扫描周期（秒）
	ScanIntervalSec int `yaml:"scan-interval-sec"`
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
			TTLSec:               30,
			OfflineGraceSec:      120,
			ScanIntervalSec:      5,
		},
		Longpoll: LongpollConfig{MaxHoldMs: 30000},
		Log:      LogConfig{Level: "INFO"},
	}
}

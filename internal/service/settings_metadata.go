package service

import (
	"strconv"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/model"
)

// 热改设置 key 常量（FR-61，见 ADR-0038）：白名单内的运维旋钮，真源在 DB store。
// 启动 / 安全项（http-addr / database.* / auth.* / agent-token / git-export.*）绝不进 store、不出现在设置 API。
const (
	SettingHealthDegradedAfterSec   = "health.degraded-after-sec"
	SettingHealthTTLSec             = "health.ttl-sec"
	SettingHealthOfflineGraceSec    = "health.offline-grace-sec"
	SettingHealthScanIntervalSec    = "health.scan-interval-sec"
	SettingMetricEnabled            = "metric.enabled"
	SettingMetricSampleIntervalSec  = "metric.sample-interval-sec"
	SettingMetricRetentionHours     = "metric.retention-hours"
	SettingLongpollMaxHoldMs        = "longpoll.max-hold-ms"
	SettingAlertWebhookURL          = "alert.webhook-url"
	SettingAlertWebhookTimeoutMs    = "alert.webhook-timeout-ms"
	SettingLogLevel                 = "log.level"
	SettingReverseFetchMaxFileBytes = "reverse-fetch.max-file-bytes"
)

// logLevels 是 log.level 的合法枚举集（与 internal/pkg/log 同口径）。
var logLevels = map[string]struct{}{
	"ERROR": {}, "WARN": {}, "INFO": {}, "DEBUG": {},
}

// settingMeta 是单个热改 key 的白名单元数据（FR-61）：类型 / 默认值 / 校验 / 首启种子取值 / 中文说明。
// 校验范围用闭区间 [Min, Max]（仅 int 类型用）；枚举校验用 enumOK（仅 string 类型用，nil 表示不限）。
type settingMeta struct {
	// 值类型：int / bool / string
	valueType string
	// 面向运维的中文说明（供前端 FR-62 展示）
	desc string
	// int 类型的合理下界（闭区间，含）；非 int 忽略
	min int
	// int 类型的合理上界（闭区间，含）；非 int 忽略
	max int
	// string 类型的枚举校验（nil 表示不限，如 URL）；非 string 忽略
	enumOK func(string) bool
	// 从 config.yml 取该 key 的默认值（首启 seed 用），返回字符串化值
	defaultFromConfig func(cfg config.Config) string
}

// settingsWhitelist 是热改项白名单元数据表（FR-61，见 ADR-0038 决策 2）。
// 写非白名单 key 一律拒；秒 / 毫秒 / 字节类按正整数合理上下界校验，log.level 按枚举校验，metric.enabled 按 bool。
var settingsWhitelist = map[string]settingMeta{
	SettingHealthDegradedAfterSec: {
		valueType: model.SettingValueTypeInt, desc: "超过多少秒未收到心跳即判亚健康（online→degraded），须小于 ttl-sec",
		min: 1, max: 86400,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Health.DegradedAfterSec) },
	},
	SettingHealthTTLSec: {
		valueType: model.SettingValueTypeInt, desc: "超过多少秒未收到心跳即判失联（degraded→lost）",
		min: 1, max: 86400,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Health.TTLSec) },
	},
	SettingHealthOfflineGraceSec: {
		valueType: model.SettingValueTypeInt, desc: "lost 后多久转 offline（秒）",
		min: 1, max: 604800,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Health.OfflineGraceSec) },
	},
	SettingHealthScanIntervalSec: {
		valueType: model.SettingValueTypeInt, desc: "后台健康扫描周期（秒）",
		min: 1, max: 3600,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Health.ScanIntervalSec) },
	},
	SettingMetricEnabled: {
		valueType: model.SettingValueTypeBool, desc: "是否启用负载指标采样器；false 时不采样、不清理（实时聚合端点仍可用）",
		defaultFromConfig: func(c config.Config) string { return strconv.FormatBool(c.Metric.Enabled) },
	},
	SettingMetricSampleIntervalSec: {
		valueType: model.SettingValueTypeInt, desc: "采样间隔（秒）：每隔多少秒对在线实例采一次样落库",
		min: 1, max: 86400,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Metric.SampleIntervalSec) },
	},
	SettingMetricRetentionHours: {
		valueType: model.SettingValueTypeInt, desc: "保留期（小时）：早于 now 减本值的样本被滚动清理，控制表体量",
		min: 1, max: 87600,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Metric.RetentionHours) },
	},
	SettingLongpollMaxHoldMs: {
		valueType: model.SettingValueTypeInt, desc: "服务端长轮询挂起上限（毫秒）；实际取 min(客户端 timeoutMs, 此值)",
		min: 1000, max: 600000,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Longpoll.MaxHoldMs) },
	},
	SettingAlertWebhookURL: {
		valueType: model.SettingValueTypeString, desc: "告警 POST 目标 URL；留空则不启用 webhook 通道（仅站内信）",
		enumOK:            nil, // URL 不做枚举约束，允许空串（表示不启用）
		defaultFromConfig: func(c config.Config) string { return c.Alert.Webhook.URL },
	},
	SettingAlertWebhookTimeoutMs: {
		valueType: model.SettingValueTypeInt, desc: "单次 webhook 请求超时（毫秒）",
		min: 100, max: 60000,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Alert.Webhook.TimeoutMs) },
	},
	SettingLogLevel: {
		valueType: model.SettingValueTypeString, desc: "日志级别：ERROR / WARN / INFO / DEBUG",
		enumOK: func(v string) bool {
			_, ok := logLevels[v]
			return ok
		},
		defaultFromConfig: func(c config.Config) string { return c.Log.Level },
	},
	SettingReverseFetchMaxFileBytes: {
		valueType: model.SettingValueTypeInt, desc: "反向抓取单文件内容上限（字节）；超此上限的文件须显式确认才纳入选定集",
		min: 1024, max: 1073741824, // 1KB ~ 1GB
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(MaxFileContentBytes) },
	},
}

// settingMetaFor 取某 key 的白名单元数据；不在白名单返回 (zero, false)。
func settingMetaFor(key string) (settingMeta, bool) {
	m, ok := settingsWhitelist[key]
	return m, ok
}

package service

import (
	"strconv"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/httpx"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 热改设置 key 常量（FR-61，见 ADR-0038）：白名单内的运维旋钮，真源在 DB store。
// 启动 / 安全项（http-addr / database.* / auth.* / agent-token / git-export.*）绝不进 store、不出现在设置 API。
const (
	SettingHealthDegradedAfterSec  = "health.degraded-after-sec"
	SettingHealthTTLSec            = "health.ttl-sec"
	SettingHealthOfflineGraceSec   = "health.offline-grace-sec"
	SettingHealthScanIntervalSec   = "health.scan-interval-sec"
	SettingMetricEnabled           = "metric.enabled"
	SettingMetricSampleIntervalSec = "metric.sample-interval-sec"
	SettingMetricRetentionHours    = "metric.retention-hours"
	SettingLongpollMaxHoldMs       = "longpoll.max-hold-ms"
	SettingAlertWebhookURL         = "alert.webhook-url"
	SettingAlertWebhookTimeoutMs   = "alert.webhook-timeout-ms"
	// 失联孤儿告警自动关闭阈值（小时）：实例既不在运行时注册表、也不在 server 表活动目录，
	// 且其未处理告警最近触发已超此时长，才被后台清理器自动消解（真机降噪，见 docs/specs/alert-dedup-and-auto-resolve.md）。
	SettingAlertOrphanTimeoutHours = "alert.orphan-timeout-hours"
	SettingLogLevel                = "log.level"
	// SettingLockDBGuardEnabled 「持锁期间 DB 访问」运行时守卫开关（P0 死锁防回归，2026-10-10）：
	// 开启时每次 DB 访问都判定「发起者是否正持有受观测锁」，命中即记 ERROR 日志（不 panic）。
	// 默认关：它是**诊断开关**，不是常规防护——常规防护由 store 层的连接等待预算承担
	// （那里把「无限挂起」变成「快速失败」），本开关用来在事故排查或回归验证时定位到具体调用点。
	// 依据 .claude/rules/testing-and-quality.md §3「DB IO 一律在锁外」。
	SettingLockDBGuardEnabled       = "debug.lock-db-guard-enabled"
	SettingReverseFetchMaxFileBytes = "reverse-fetch.max-file-bytes"
	SettingUpdateProxyURL           = "update.proxy-url"
	SettingUpdateChannel            = "update.channel"
	SettingUpdateAutoCheckEnabled   = "update.auto-check-enabled"
	SettingUpdateCheckIntervalHours = "update.check-interval-hours"
	SettingUndoWindowHours          = "undo.window-hours"
	// 并发身份冲突检测窗口（FR-177，spec §4.5）：窗口内同 identityId bootId 往复活跃即判并发双实例。
	SettingIdentityConflictWindowSec = "identity.conflict-window-sec"
	// 交付编排审批职责分离（FR-162/168，spec v2-delivery-orchestration.md §4.8.1）：
	// 开启时变更单审批人不得是创建人；单管理员小规模部署可关闭（关闭动作走 settings.update 审计）。
	SettingDeliveryApproverSeparationEnabled = "delivery.approver-separation-enabled"
	// 交付数据面资源约束键（FR-165，见 ADR-0069 决策 4）：blob 保留期 / 磁盘容量 / 上传下载并发 / 清理周期，
	// 均无 config.yml 对应项、纯设置 store 项，走热更（消费点每轮 / 每请求重读）。
	SettingDeliveryBlobRetentionDays      = "delivery.blob-retention-days"
	SettingDeliveryBlobCapacityBytes      = "delivery.blob-capacity-bytes"
	SettingDeliveryUploadConcurrency      = "delivery.upload-concurrency"
	SettingDeliveryDownloadConcurrency    = "delivery.download-concurrency"
	SettingDeliveryCleanupIntervalMinutes = "delivery.cleanup-interval-minutes"
	// SettingDeliveryMinAgentVersion 交付能力最低 agent 版本（FR-264，落实 ADR-0069 L58）：
	// 目标 / 模板源 agent 自报版本低于此值即被拒绝下发交付命令；留空 = 不校验（运维逃生口）。
	SettingDeliveryMinAgentVersion = "delivery.min-agent-version"
	// 热冷归档策略键（FR-151，见 ADR-0066）：各域热库保留天数（≥7 守卫）+ 调度 / 批量 / 校验 / 冷查询参数。
	// 注意 `archive.retention-days.alert-event` 只对**已处理**（status=resolved）告警生效：未处理告警是运维待办、
	// 永不搬运（域注册表的 extraWhere 约束），调小该保留期不会让待办从热库消失。
	SettingArchiveRetentionMetricSample   = "archive.retention-days.metric-sample"
	SettingArchiveRetentionHealthSnapshot = "archive.retention-days.health-snapshot"
	SettingArchiveRetentionSchedDecision  = "archive.retention-days.sched-decision"
	SettingArchiveRetentionConnDetail     = "archive.retention-days.conn-detail"
	SettingArchiveRetentionMsgTrace       = "archive.retention-days.msg-trace"
	SettingArchiveRetentionMsgPayload     = "archive.retention-days.msg-payload"
	SettingArchiveRetentionAudit          = "archive.retention-days.audit"
	SettingArchiveRetentionMCPInvocation  = "archive.retention-days.mcp-invocation"
	SettingArchiveRetentionAlertEvent     = "archive.retention-days.alert-event"
	SettingArchiveAutoEnabled             = "archive.auto-enabled"
	SettingArchiveScheduleHourUTC         = "archive.schedule-hour-utc"
	SettingArchiveBatchRows               = "archive.batch-rows"
	SettingArchiveBatchIntervalMs         = "archive.batch-interval-ms"
	SettingArchiveVerifySampleSize        = "archive.verify-sample-size"
	SettingArchiveColdQueryMaxDays        = "archive.cold-query-max-days"
)

// 热冷归档策略键默认值（FR-151，spec §3.3；各域保留期默认以属主规格量级为准）。
// 这些键无 config.yml 对应项、纯设置 store 项，默认值由此常量提供。
// alert_event 与 audit / mcp-invocation 同档（180 天）：告警事件是运维处置留痕，追溯价值高。
const (
	archiveDefaultRetentionMetricSample   = 14
	archiveDefaultRetentionHealthSnapshot = 30
	archiveDefaultRetentionSchedDecision  = 60
	archiveDefaultRetentionConnDetail     = 60
	archiveDefaultRetentionMsgTrace       = 60
	archiveDefaultRetentionMsgPayload     = 30
	archiveDefaultRetentionAudit          = 180
	// MCP 工具调用是低频机器操作流水，取证窗口应与审计一致（FR-240，spec §3.8）。
	archiveDefaultRetentionMCPInvocation = 180
	archiveDefaultRetentionAlertEvent    = 180
	archiveDefaultAutoEnabled            = true
	archiveDefaultScheduleHourUTC        = 4
	archiveDefaultBatchRows              = 1000
	archiveDefaultBatchIntervalMs        = 200
	archiveDefaultVerifySampleSize       = 100
	archiveDefaultColdQueryMaxDays       = 31
	// 保留期下限守卫：任何 retention-days.* 不得小于此值，防误配当天删光（spec §3.3）。
	archiveMinRetentionDays = 7
)

// 并发身份冲突检测窗口默认值（FR-177，spec §4.5）：默认 10 分钟；无 config.yml 对应项、纯设置 store 项。
const identityDefaultConflictWindowSec = 600

// 失联孤儿告警自动关闭阈值默认值（小时）：无 store 值 / 值非法时的防御性回退口径。
// 出厂默认与首启种子口径见 config.Default()（config.yml 的 alert.orphan-timeout-hours）。
const alertOrphanTimeoutDefaultHours = 24

// 交付数据面资源约束默认值（FR-165，spec §8 #4，见 ADR-0069）：初始值按 ADR 拍板，需按真机带宽 / 磁盘实测校准。
// 容量按字节计（20 GiB），要求 64 位 int（控制面仅构建 amd64/arm64 目标）。
const (
	deliveryDefaultBlobRetentionDays      = 7
	deliveryDefaultBlobCapacityBytes      = 21474836480 // 20 GiB
	deliveryDefaultUploadConcurrency      = 4
	deliveryDefaultDownloadConcurrency    = 64
	deliveryDefaultCleanupIntervalMinutes = 60
	// deliveryDefaultMinAgentVersion 是交付能力守卫的默认最低 agent 版本（FR-264）。
	//
	// **默认为空串 = 守卫默认关闭（显式配置才启用）**：agent 上报的版本串尚未经真机核对其形态
	// （实际来自 TabooLib `pluginVersion`，与 Gradle 坐标不同源），而守卫是 fail-closed 的
	// （未上报版本一律按「不支持」处理）。若拍一个猜测值作默认，真机上一旦上报串不符
	// （例如 `0.1.0` 或空串），**全部目标都会被拒、首日无任何已批准单能启动**——
	// 这是上线即全量拒服的事故级风险，故以「不校验」上线，真机核对后再由运维设为确定值。
	deliveryDefaultMinAgentVersion = ""
	// 容量上限的可配上界（1 TiB）与下界（1 MiB）：防误配 0 / 负值当场拒绝所有上传。
	deliveryBlobCapacityMinBytes = 1048576
	deliveryBlobCapacityMaxBytes = 1099511627776
)

// updateChannels 是 update.channel 的合法枚举集：标准在线更新只消费 stable GA（FR-185）。
var updateChannels = map[string]struct{}{
	"stable": {},
}

// proxyURLValid 校验 update.proxy-url：空串=直连合法；非空须为 http/https 且 host:port 合法（FR-98，见 ADR-0047）。
// 复用 httpx.ParseProxyURL 与出站工厂同口径，确保「能存进 store 的代理一定能构造客户端」。
func proxyURLValid(v string) bool {
	if v == "" {
		return true
	}
	_, err := httpx.ParseProxyURL(v)
	return err == nil
}

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
	SettingAlertOrphanTimeoutHours: {
		valueType: model.SettingValueTypeInt, desc: "失联孤儿告警自动关闭阈值（小时）：实例不在运行时注册表、也不在 server 表活动目录，且未处理告警最近触发已超此时长才自动消解",
		min: 1, max: 8760, // 1 小时 ~ 1 年
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Alert.OrphanTimeoutHours) },
	},
	SettingLogLevel: {
		valueType: model.SettingValueTypeString, desc: "日志级别：ERROR / WARN / INFO / DEBUG",
		enumOK: func(v string) bool {
			_, ok := logLevels[v]
			return ok
		},
		defaultFromConfig: func(c config.Config) string { return c.Log.Level },
	},
	SettingLockDBGuardEnabled: {
		valueType:         model.SettingValueTypeBool,
		desc:              "「持锁期间 DB 访问」运行时守卫开关：开启时命中即记 ERROR 日志（不 panic），用于事故排查与回归验证；默认关",
		defaultFromConfig: func(config.Config) string { return strconv.FormatBool(false) },
	},
	SettingReverseFetchMaxFileBytes: {
		valueType: model.SettingValueTypeInt, desc: "反向抓取单文件内容上限（字节）；超此上限的文件须显式确认才纳入选定集",
		min: 1024, max: 1073741824, // 1KB ~ 1GB
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(MaxFileContentBytes) },
	},
	SettingUpdateProxyURL: {
		valueType:         model.SettingValueTypeString,
		desc:              "更新出站代理地址（http://host:port 或 https://...，可含 user:pass）；留空=直连。仅作用于控制面更新检查/下载出站，不影响 webhook",
		enumOK:            proxyURLValid, // 空串=直连合法；非空校验 http/https + host:port（FR-98，见 ADR-0047）
		defaultFromConfig: func(c config.Config) string { return c.Update.ProxyURL },
	},
	SettingUpdateChannel: {
		valueType: model.SettingValueTypeString, desc: "更新渠道：stable（仅合法 GA 正式版）",
		enumOK: func(v string) bool {
			_, ok := updateChannels[v]
			return ok
		},
		defaultFromConfig: func(c config.Config) string { return c.Update.Channel },
	},
	SettingUpdateAutoCheckEnabled: {
		valueType: model.SettingValueTypeBool, desc: "是否启用自动检查更新；false 时不后台轮询、仅手动检查",
		defaultFromConfig: func(c config.Config) string { return strconv.FormatBool(c.Update.AutoCheckEnabled) },
	},
	SettingUpdateCheckIntervalHours: {
		valueType: model.SettingValueTypeInt, desc: "自动检查更新周期（小时）：每隔多少小时查一次有无新版本",
		min: 1, max: 168,
		defaultFromConfig: func(c config.Config) string { return strconv.Itoa(c.Update.CheckIntervalHours) },
	},
	SettingUndoWindowHours: {
		valueType: model.SettingValueTypeInt, desc: "配置操作可撤回时间窗（小时）：下发 / 发布 / 反向抓取超此时长不可撤回（FR-116）",
		min: 1, max: 8760, // 1 小时 ~ 1 年
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(DefaultUndoWindowHours) },
	},
	SettingIdentityConflictWindowSec: {
		valueType: model.SettingValueTypeInt, desc: "并发身份冲突检测窗口（秒）：窗口内同一 identityId 出现 bootId 往复活跃即判并发双实例并冻结待处置（FR-177）",
		min: 30, max: 86400, // 30 秒 ~ 1 天
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(identityDefaultConflictWindowSec) },
	},
	SettingDeliveryApproverSeparationEnabled: {
		valueType: model.SettingValueTypeBool, desc: "变更单审批职责分离：开启时审批人不得是创建人；单管理员部署可关闭（关闭动作入审计）",
		defaultFromConfig: func(config.Config) string { return strconv.FormatBool(true) },
	},
	// 交付数据面资源约束（FR-165，见 ADR-0069 决策 4）：清理器 / 流式端点每轮 / 每请求热读。
	SettingDeliveryBlobRetentionDays: {
		valueType: model.SettingValueTypeInt, desc: "交付中转 blob 保留天数：引用它的变更单全部终结且超此天数未被引用才清理",
		min: 1, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(deliveryDefaultBlobRetentionDays) },
	},
	SettingDeliveryBlobCapacityBytes: {
		valueType: model.SettingValueTypeInt, desc: "交付中转存储容量上限（字节）：已存量 + 新上传声明大小超限即拒绝新上传",
		min: deliveryBlobCapacityMinBytes, max: deliveryBlobCapacityMaxBytes,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(deliveryDefaultBlobCapacityBytes) },
	},
	SettingDeliveryUploadConcurrency: {
		valueType: model.SettingValueTypeInt, desc: "交付流式上传全局并发上限（同时进行的 PUT 流数），超限新上传返回 429",
		min: 1, max: 64,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(deliveryDefaultUploadConcurrency) },
	},
	SettingDeliveryDownloadConcurrency: {
		valueType: model.SettingValueTypeInt, desc: "交付流式下载全局并发上限（同时进行的 GET 流数），超限新下载返回 429",
		min: 1, max: 512,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(deliveryDefaultDownloadConcurrency) },
	},
	SettingDeliveryCleanupIntervalMinutes: {
		valueType: model.SettingValueTypeInt, desc: "交付中转 blob 后台清理周期（分钟）",
		min: 5, max: 10080,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(deliveryDefaultCleanupIntervalMinutes) },
	},
	SettingDeliveryMinAgentVersion: {
		valueType: model.SettingValueTypeString,
		desc:      "交付能力最低 agent 版本：agent 自报版本低于此值或不提供版本时拒绝下发交付命令；留空则不校验",
		// 版本串形态自由（可能带预发布后缀、也可能留空关闭守卫），故不做枚举约束。
		enumOK:            nil,
		defaultFromConfig: func(config.Config) string { return deliveryDefaultMinAgentVersion },
	},
	// 热冷归档保留期（FR-151，见 ADR-0066）：各域热库保留天数，下限 7（防误配当天删光）、上限 3650（约 10 年）。
	SettingArchiveRetentionMetricSample: {
		valueType: model.SettingValueTypeInt, desc: "指标批（metric_sample）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionMetricSample) },
	},
	SettingArchiveRetentionHealthSnapshot: {
		valueType: model.SettingValueTypeInt, desc: "健康快照（health_snapshot）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionHealthSnapshot) },
	},
	SettingArchiveRetentionSchedDecision: {
		valueType: model.SettingValueTypeInt, desc: "调度决策（sched_decision）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionSchedDecision) },
	},
	SettingArchiveRetentionConnDetail: {
		valueType: model.SettingValueTypeInt, desc: "连接明细（conn_detail）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionConnDetail) },
	},
	SettingArchiveRetentionMsgTrace: {
		valueType: model.SettingValueTypeInt, desc: "消息元数据（msg_trace）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionMsgTrace) },
	},
	SettingArchiveRetentionMsgPayload: {
		valueType: model.SettingValueTypeInt, desc: "消息 payload（msg_payload）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionMsgPayload) },
	},
	SettingArchiveRetentionAudit: {
		valueType: model.SettingValueTypeInt, desc: "审计记录（audit）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionAudit) },
	},
	SettingArchiveRetentionMCPInvocation: {
		valueType: model.SettingValueTypeInt, desc: "MCP 工具调用流水（mcp_invocation）热库保留天数；到期后归档并从热库删除",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionMCPInvocation) },
	},
	SettingArchiveRetentionAlertEvent: {
		valueType: model.SettingValueTypeInt, desc: "已处理告警事件（alert_event，status=resolved）热库保留天数；到期后归档并从热库删除（未处理告警是待办，始终留在热库）",
		min: archiveMinRetentionDays, max: 3650,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultRetentionAlertEvent) },
	},
	SettingArchiveAutoEnabled: {
		valueType: model.SettingValueTypeBool, desc: "是否每日自动执行归档任务；false 时仅手动触发",
		defaultFromConfig: func(config.Config) string { return strconv.FormatBool(archiveDefaultAutoEnabled) },
	},
	SettingArchiveScheduleHourUTC: {
		valueType: model.SettingValueTypeInt, desc: "每日自动执行归档任务的 UTC 整点（0~23）",
		min: 0, max: 23,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultScheduleHourUTC) },
	},
	SettingArchiveBatchRows: {
		valueType: model.SettingValueTypeInt, desc: "归档单批搬运 / 删除行数（越大越快、对主库压力越大）",
		min: 1, max: 100000,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultBatchRows) },
	},
	SettingArchiveBatchIntervalMs: {
		valueType: model.SettingValueTypeInt, desc: "归档批间歇（毫秒）：每批之间 sleep 限流、保护主库",
		min: 0, max: 60000,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultBatchIntervalMs) },
	},
	SettingArchiveVerifySampleSize: {
		valueType: model.SettingValueTypeInt, desc: "归档删除前抽样校验条数上限（sha256 抽样）",
		min: 1, max: 10000,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultVerifySampleSize) },
	},
	SettingArchiveColdQueryMaxDays: {
		valueType: model.SettingValueTypeInt, desc: "冷查询（includeArchived）单次最大时间跨度（天）",
		min: 1, max: 366,
		defaultFromConfig: func(config.Config) string { return strconv.Itoa(archiveDefaultColdQueryMaxDays) },
	},
}

// secretSettingKeys 标记「值含凭据、对外须脱敏」的设置 key（FR-98，见 ADR-0047）。
// 这些 key 的 value 落库存原值供运行，但审计 detail / 日志 / 前端回显一律走 httpx.RedactURLCredentials 脱敏。
var secretSettingKeys = map[string]struct{}{
	SettingUpdateProxyURL: {},
}

// dangerousSettingKeys 是运行设置元数据的一部分；危险性只从此处读取，handler 不得自行猜测。
var dangerousSettingKeys = map[string]struct{}{
	SettingHealthDegradedAfterSec: {}, SettingHealthTTLSec: {}, SettingHealthOfflineGraceSec: {}, SettingHealthScanIntervalSec: {},
	SettingMetricSampleIntervalSec: {}, SettingMetricRetentionHours: {}, SettingLongpollMaxHoldMs: {},
	SettingAlertWebhookURL: {}, SettingAlertWebhookTimeoutMs: {}, SettingUpdateProxyURL: {}, SettingUpdateChannel: {},
	// 失联孤儿告警自动关闭阈值：调小会让「外部删除的实例告警」更快被自动消解（影响运维可见范围），
	// 故与保留期类阈值同口径纳入高影响设置、改动走审批 + 审计。
	SettingAlertOrphanTimeoutHours: {},
	SettingUpdateAutoCheckEnabled:  {}, SettingUpdateCheckIntervalHours: {},
	SettingArchiveRetentionMetricSample: {}, SettingArchiveRetentionHealthSnapshot: {}, SettingArchiveRetentionSchedDecision: {},
	SettingArchiveRetentionConnDetail: {}, SettingArchiveRetentionMsgTrace: {}, SettingArchiveRetentionMsgPayload: {},
	SettingArchiveRetentionAudit: {}, SettingArchiveRetentionMCPInvocation: {}, SettingArchiveRetentionAlertEvent: {},
	SettingArchiveAutoEnabled: {}, SettingArchiveScheduleHourUTC: {},
}

// SettingDangerous 返回设置是否已登记为高影响。
func SettingDangerous(key string) bool {
	_, ok := dangerousSettingKeys[key]
	return ok
}

// isSecretSettingKey 判断某 key 是否为含凭据项（对外须脱敏）。
func isSecretSettingKey(key string) bool {
	_, ok := secretSettingKeys[key]
	return ok
}

// settingMetaFor 取某 key 的白名单元数据；不在白名单返回 (zero, false)。
func settingMetaFor(key string) (settingMeta, bool) {
	m, ok := settingsWhitelist[key]
	return m, ok
}

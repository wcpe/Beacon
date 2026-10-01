package model

import "time"

// MCP 工具调用流水的结果档（FR-240，spec §3.4 冻结）。
const (
	// MCPInvocationResultOK 调用成功。
	MCPInvocationResultOK = "ok"
	// MCPInvocationResultFail 调用失败（协议错误 / handler 错误 / 多轮交互未完成）。
	MCPInvocationResultFail = "fail"
	// MCPInvocationResultRejected 调用被拒（未知工具 / 业务拒绝 / 生产模式拒执）。
	MCPInvocationResultRejected = "rejected"
)

// MCP 工具调用流水的风险等级档（FR-240，spec §3.2 冻结）。
// low / high / critical 取自 mcpToolCatalog；unknown 表示工具未登记入目录（撞目录的调用）。
const (
	MCPInvocationRiskLow      = "low"
	MCPInvocationRiskHigh     = "high"
	MCPInvocationRiskCritical = "critical"
	MCPInvocationRiskUnknown  = "unknown"
)

// MCP 工具调用流水的拒绝 / 失败原因码（FR-240，spec §3.4 冻结；成功时为空串）。
const (
	// MCPInvocationReasonUnknownTool 工具名不在 mcpToolCatalog（与 SDK 的 unknown tool 判定同源）。
	MCPInvocationReasonUnknownTool = "unknown_tool"
	// MCPInvocationReasonProductionMode 生产模式执行面拒执（FR-242）。
	MCPInvocationReasonProductionMode = "production_mode"
	// MCPInvocationReasonHandlerRejected handler 主动的业务拒绝（err 为 nil、IsError 为真）。
	MCPInvocationReasonHandlerRejected = "handler_rejected"
	// MCPInvocationReasonHandlerError 错误路径（err 非 nil 或 SDK 包装错误）。
	MCPInvocationReasonHandlerError = "handler_error"
	// MCPInvocationReasonInputRequired 多轮交互本轮未完成。
	MCPInvocationReasonInputRequired = "input_required"
	// MCPInvocationReasonInternalError middleware 自身 panic 恢复路径。
	MCPInvocationReasonInternalError = "internal_error"
)

// mcpInvocationResultSet / riskLevelSet / reasonSet 是三个枚举列的合法取值集合（应用层校验真源）。
var (
	mcpInvocationResultSet = map[string]struct{}{
		MCPInvocationResultOK: {}, MCPInvocationResultFail: {}, MCPInvocationResultRejected: {},
	}
	mcpInvocationRiskLevelSet = map[string]struct{}{
		MCPInvocationRiskLow: {}, MCPInvocationRiskHigh: {}, MCPInvocationRiskCritical: {}, MCPInvocationRiskUnknown: {},
	}
	mcpInvocationReasonSet = map[string]struct{}{
		"": {}, MCPInvocationReasonUnknownTool: {}, MCPInvocationReasonProductionMode: {},
		MCPInvocationReasonHandlerRejected: {}, MCPInvocationReasonHandlerError: {},
		MCPInvocationReasonInputRequired: {}, MCPInvocationReasonInternalError: {},
	}
)

// IsValidMCPInvocationResult 校验结果列取值。
func IsValidMCPInvocationResult(v string) bool {
	_, ok := mcpInvocationResultSet[v]
	return ok
}

// IsValidMCPInvocationRiskLevel 校验风险等级列取值。
func IsValidMCPInvocationRiskLevel(v string) bool {
	_, ok := mcpInvocationRiskLevelSet[v]
	return ok
}

// IsValidMCPInvocationReason 校验原因码列取值（空串表示成功）。
func IsValidMCPInvocationReason(v string) bool {
	_, ok := mcpInvocationReasonSet[v]
	return ok
}

// MCPInvocation 是 MCP 工具调用日表 mcp_invocation_YYYYMMDD 的行模型（FR-240）。
// 全部基础类型，禁 JSON / ENUM 列与方言专有 SQL（守 DB 可移植，与 ConnDetail 同口径）；
// 枚举 result / reason / risk_level 落 VARCHAR + 应用层校验。
// 索引用 composite 空名式，让 GORM 按当日表名生成索引名（sqlite 索引名全库唯一，同字面名跨日冲突）。
//
// 本模型不进全局 AutoMigrate——日表按 UTC 日期后缀由 store.EnsureDailyTable 按需建。
type MCPInvocation struct {
	// 控制面生成的 UUIDv7 文本（36 字符）；内嵌毫秒 = 完成时刻，日表按它路由
	InvocationID string `gorm:"column:invocation_id;size:36;primaryKey"`
	// 认证主体 ID（MCPOAuthClient.ClientID），权威取自已认证主体、绝不取请求自报值
	ClientID string `gorm:"column:client_id;size:96;not null;index:,composite:client_created,priority:1"`
	// 主体档案：observer / automation
	Profile string `gorm:"column:profile;size:32;not null"`
	// 工具名原文（≤128，超出截断）；未知工具也记原样，故不做外键 / 白名单校验
	ToolName string `gorm:"column:tool_name;size:128;not null;index:,composite:tool_created,priority:1"`
	// 风险等级：low / high / critical / unknown（未登记目录记 unknown）
	RiskLevel string `gorm:"column:risk_level;size:16;not null;index:,composite:risk_created,priority:1"`
	// 结果：ok / fail / rejected
	Result string `gorm:"column:result;size:16;not null;index:,composite:result_created,priority:1"`
	// 原因码（受控枚举，成功为空串）
	Reason string `gorm:"column:reason;size:32;not null;default:''"`
	// 目标标识摘要 k1=v1;k2=v2（≤3 项，≤255 字符）
	TargetDigest string `gorm:"column:target_digest;size:255;not null;default:''"`
	// 顶层参数键名清单（≤24 项，≤512 字符）
	ArgKeys string `gorm:"column:arg_keys;size:512;not null;default:''"`
	// 参数原文字节数（不是字符数），缺省 0
	ArgBytes int `gorm:"column:arg_bytes;not null;default:0"`
	// middleware 入口 → handler 返回的墙钟毫秒（不含响应序列化 / 网络）
	DurationMs int `gorm:"column:duration_ms;not null;default:0"`
	// 16 位十六进制追踪号，与响应头 X-Trace-Id 同值
	TraceID string `gorm:"column:trace_id;size:32;not null;default:''"`
	// 客户端地址（IPv4 / IPv6 文本，可为空）
	ClientIP string `gorm:"column:client_ip;size:45;not null;default:''"`
	// 失败 / 拒绝摘要（≤255，已脱敏）；成功为空串
	ErrorSummary string `gorm:"column:error_summary;size:255;not null;default:''"`
	// 调用完成时刻（UTC）；同时充当四组复合索引的第 2 位
	CreatedAt time.Time `gorm:"column:created_at;not null;index:,composite:client_created,priority:2;index:,composite:tool_created,priority:2;index:,composite:risk_created,priority:2;index:,composite:result_created,priority:2"`
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
func (MCPInvocation) TableName() string { return "mcp_invocation" }

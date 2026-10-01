package server

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// MCPInvocationRecorder 是工具调用流水的唯一写入出口（FR-240，spec §3.1）。
//
// server 包只持这一窄接口，实现由 service 层提供（非阻塞投递到异步日表写入通道）；
// 为 nil 时 middleware 不挂载——既有单测与未装配路径零依赖、零行为变化。
type MCPInvocationRecorder interface {
	Record(model.MCPInvocation)
}

// 三档脱敏与存储形状的冻结上界（spec §3.2/§3.3）。
const (
	// mcpInvocationTextMax 目标标识单值截断长度（超长加省略号）。
	mcpInvocationTextMax = 96
	// mcpInvocationDigestMax target_digest 整体列宽。
	mcpInvocationDigestMax = 255
	// mcpInvocationDigestFields A 档最多记几个键（按声明顺序取前 N 个「存在且为标量」的键）。
	mcpInvocationDigestFields = 3
	// mcpInvocationArgKeysMax arg_keys 列宽。
	mcpInvocationArgKeysMax = 512
	// mcpInvocationArgKeysEntries arg_keys 最多记几个键。
	mcpInvocationArgKeysEntries = 24
	// mcpInvocationSummaryMax error_summary / tool_name 列宽。
	mcpInvocationSummaryMax = 255
	// mcpInvocationToolMax tool_name 列宽（与模型 size:128 对齐）。
	mcpInvocationToolMax = 128
)

// mcpInvocationRejectedPrefix 是控制面统一拒绝文案的前缀（mcpToolError / mcpToolErrorWithReason）。
// 入库时去掉它、只留 <原因>，便于按原因定位（spec §3.4）。
const mcpInvocationRejectedPrefix = "请求被拒绝或目标不可用"

// mcpUnknownToolPhrase 是 SDK 未发现工具的错误消息片段（mcp/server.go：`unknown tool %q`，带引号的工具名）。
// 用「包含」而不是「前缀」匹配：该错误经 jsonrpc 层透传后会被包上 `calling "tools/call": ` 前缀，
// 直接判前缀会永不命中（本 FR 实现期实测，非推测）。
const mcpUnknownToolPhrase = `unknown tool "`

// mcpTargetDigestKeys 是 A 档目标标识白名单，**声明顺序即取值顺序**（保证输出可复现、可断言）。
// 这是唯一允许存值的一档；键全部是标识类（id / 名字 / path），非凭据非正文。
var mcpTargetDigestKeys = []string{
	"namespace", "namespaceCode", "namespaceId", "fromNamespaceId", "toNamespaceId",
	"serverId", "serverRowId", "id", "targetId", "parentId", "targetKind", "targetType",
	"identityId", "orderId", "requestId", "grantId", "messageId", "zone", "group",
	"path", "targetRoot", "version", "scopeLevel", "scopeTarget", "batchNo", "keepBootId",
}

// mcpContentKeySubstrings 是 B 档内容类键的判定子串（键名小写后包含任一即命中，spec §3.3）。
// 命中者只在 arg_keys 中记「键名:字节数」，**值绝不入库**。
var mcpContentKeySubstrings = []string{
	"content", "payload", "body", "text", "data", "file", "secret", "token", "password", "passwd", "pwd",
	"credential", "api_key", "apikey", "comment", "note", "reason", "command", "key", "name", "value",
}

// mcpAttachInvocationAudit 在 server 构造期挂载工具调用流水 middleware（FR-240，spec §3.1）。
//
// rec 为 nil 时 no-op：既有单测与未装配路径不需要任何改动。
// middleware 只读 ctx 与请求参数、只写队列——不改请求、不改结果、不改 handler 语义；
// method != "tools/call" 直接 next，零额外开销。
func mcpAttachInvocationAudit(server *mcp.Server, rec MCPInvocationRecorder) {
	if server == nil || rec == nil {
		return
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			return mcpAuditToolCall(ctx, req, rec, next)
		}
	})
}

// mcpAuditToolCall 是 tools/call 的流水 middleware 主体：计时 → 交给下游 → 分类留痕。
//
// 分类与脱敏判据全部照 spec §3.4 / §3.3 冻结规则；本函数不得改动返回值 / 不启动 goroutine / 不加锁。
func mcpAuditToolCall(ctx context.Context, req mcp.Request, rec MCPInvocationRecorder, next mcp.MethodHandler) (mcp.Result, error) {
	call, ok := req.(*mcp.CallToolRequest)
	if !ok || call == nil || call.Params == nil {
		// 非 tools/call 的参数形态：不做任何推断，原样放行。
		return next(ctx, "tools/call", req)
	}
	toolName := call.Params.Name
	args := call.Params.Arguments

	started := time.Now()
	// panic 恢复：先记一行 internal_error，再让 panic 继续上抛给 HTTP 层 recoverMiddleware（既有行为不变）。
	// 只记固定文案 panic——**不记 panic 值文本**，防其携带业务数据。
	defer func() {
		if r := recover(); r != nil {
			row := mcpBuildInvocation(ctx, toolName, args, nil, nil, model.MCPInvocationReasonInternalError, time.Since(started))
			row.ErrorSummary = "panic"
			rec.Record(row)
			panic(r)
		}
	}()

	res, err := next(ctx, "tools/call", req)
	rec.Record(mcpClassifyInvocation(ctx, toolName, args, res, err, time.Since(started)))
	return res, err
}

// mcpInvocationRiskLevel 取工具的风险等级：未登记目录记 unknown（不是空串，便于筛「撞目录的调用」）。
func mcpInvocationRiskLevel(name string) string {
	if spec, ok := mcpToolSpecByName(name); ok {
		return spec.RiskLevel
	}
	return model.MCPInvocationRiskUnknown
}

// mcpClassifyInvocation 按 spec §3.4 的判据表把一次调用分类为 (result, reason, errorSummary)。
//
// 判据顺序自上而下取首个命中：
//  1. 工具名不在 mcpToolCatalog 或 SDK 以 unknown tool 上抛 → rejected / unknown_tool（**优先于** handler_error）；
//  2. err == nil 且 res.IsError 且 res.GetError() == nil（handler 主动拒绝）→ rejected / production_mode|handler_rejected；
//  3. err != nil 或 res.IsError && res.GetError() != nil → fail / handler_error；
//  4. res.NeedsInput()（多轮交互本轮未完成）→ fail / input_required；
//  5. 其余 → ok / ""。
func mcpClassifyInvocation(ctx context.Context, toolName string, args json.RawMessage, res mcp.Result, err error, elapsed time.Duration) model.MCPInvocation {
	callResult, _ := res.(*mcp.CallToolResult)
	_, registered := mcpToolSpecByName(toolName)

	switch {
	case !registered || mcpIsUnknownToolError(err):
		return mcpBuildInvocation(ctx, toolName, args, res, err, model.MCPInvocationReasonUnknownTool, elapsed)
	case err == nil && callResult != nil && callResult.IsError && callResult.GetError() == nil:
		reason := model.MCPInvocationReasonHandlerRejected
		if mcpIsProductionModeRejection(toolName, callResult) {
			reason = model.MCPInvocationReasonProductionMode
		}
		return mcpBuildInvocation(ctx, toolName, args, res, err, reason, elapsed)
	case err != nil || (callResult != nil && callResult.IsError && callResult.GetError() != nil):
		return mcpBuildInvocation(ctx, toolName, args, res, err, model.MCPInvocationReasonHandlerError, elapsed)
	case callResult != nil && callResult.NeedsInput():
		// 防御分支：现无工具使用 MRTR（多轮交互）。
		return mcpBuildInvocation(ctx, toolName, args, res, err, model.MCPInvocationReasonInputRequired, elapsed)
	default:
		return mcpBuildInvocation(ctx, toolName, args, res, err, "", elapsed)
	}
}

// mcpIsUnknownToolError 判定 SDK 是否以 `unknown tool "<name>"` 上抛。
//
// 这是 §3.4 判据表的补充而非主判据（主判据是「工具名不在 mcpToolCatalog」）：
// 目录内**但本次未注册**的工具（如审批决定工具在开关关闭时被 mcpToolDiscoverable 隐去）
// 调用同样落到 SDK 的 unknown tool，只有靠消息片段才认得出，否则会错记成 handler_error。
func mcpIsUnknownToolError(err error) bool {
	return err != nil && strings.Contains(err.Error(), mcpUnknownToolPhrase)
}

// mcpIsProductionModeRejection 判定一次「handler 主动拒绝」是否来自 FR-242 的生产模式执行面拒执。
//
// 主判据（冻结）：拒绝文案以 mcpProductionModeRejectedReason 为前缀。
// 一致性兜底：riskLevel=critical 且生产模式开关打开——与主判据应恒等，测试双向断言（spec §3.6）。
func mcpIsProductionModeRejection(toolName string, res *mcp.CallToolResult) bool {
	if strings.HasPrefix(mcpRejectionSummary(res), mcpProductionModeRejectedReason) {
		return true
	}
	return auth.MCPProductionModeEnabled() && mcpInvocationRiskLevel(toolName) == MCPRiskCritical
}

// mcpBuildInvocation 组装一行流水：脱敏摘要 + 主体 / traceId / 地址 + 完成时刻与 UUIDv7 主键。
//
// CreatedAt 与 InvocationID 内嵌毫秒同源——取同一时刻、取整到毫秒（spec §3.2）。
func mcpBuildInvocation(ctx context.Context, toolName string, args json.RawMessage, res mcp.Result, err error, reason string, elapsed time.Duration) model.MCPInvocation {
	callResult, _ := res.(*mcp.CallToolResult)
	digest, argKeys := mcpSummarizeArguments(args)

	principal, _ := auth.FromContext(ctx)
	completedAt := time.Now().UTC()
	ms := completedAt.UnixMilli()

	row := model.MCPInvocation{
		InvocationID: store.NewUUIDv7(ms),
		ClientID:     principal.ID,
		Profile:      principal.Role,
		ToolName:     mcpClampRunes(toolName, mcpInvocationToolMax),
		RiskLevel:    mcpInvocationRiskLevel(toolName),
		Result:       mcpResultOfReason(reason),
		Reason:       reason,
		TargetDigest: digest,
		ArgKeys:      argKeys,
		ArgBytes:     len(args),
		DurationMs:   int(elapsed / time.Millisecond),
		TraceID:      render.TraceID(ctx),
		ClientIP:     auth.ClientIPFromContext(ctx),
		CreatedAt:    time.UnixMilli(ms).UTC(),
	}
	row.ErrorSummary = mcpErrorSummary(callResult, err, row.Result)
	return row
}

// mcpResultOfReason 由原因码反推 result 列（spec §3.4 判据表逐行对齐）：成功为空串即 ok，
// 三种拒绝原因对应 rejected，其余失败原因对应 fail。
func mcpResultOfReason(reason string) string {
	switch reason {
	case "":
		return model.MCPInvocationResultOK
	case model.MCPInvocationReasonUnknownTool,
		model.MCPInvocationReasonProductionMode,
		model.MCPInvocationReasonHandlerRejected:
		return model.MCPInvocationResultRejected
	default:
		return model.MCPInvocationResultFail
	}
}

// mcpErrorSummary 按 spec §3.4 提取失败 / 拒绝摘要（结果正文绝不入库）。
//
// 优先级 ① res.GetError() 的消息文本（错误路径）；② res.IsError 时**首条**文本块的文本（拒绝路径）；
// 两者皆无 → ""。result=ok 时绝不读 Content（那是业务正文）。
// 拒绝文案入库时去掉统一前缀「请求被拒绝或目标不可用：」，只留 <原因>（无原因则存前缀本身）。
// 所有文本先经 redact.Desensitize **再**截断——顺序不可颠倒，否则被截断的凭据片段可能漏码。
func mcpErrorSummary(res *mcp.CallToolResult, err error, result string) string {
	if result == model.MCPInvocationResultOK {
		return ""
	}
	if res == nil {
		return mcpClampRunes(redact.DesensitizeErr(err), mcpInvocationSummaryMax)
	}
	if e := res.GetError(); e != nil {
		return mcpClampRunes(redact.Desensitize(e.Error()), mcpInvocationSummaryMax)
	}
	if !res.IsError {
		return mcpClampRunes(redact.DesensitizeErr(err), mcpInvocationSummaryMax)
	}
	return mcpClampRunes(redact.Desensitize(mcpRejectionSummary(res)), mcpInvocationSummaryMax)
}

// mcpRejectionSummary 取拒绝结果的首条文本块原文并去掉统一前缀（只读第一条，绝不读 StructuredContent）。
func mcpRejectionSummary(res *mcp.CallToolResult) string {
	text := mcpFirstTextContent(res)
	if text == "" {
		return ""
	}
	if !strings.HasPrefix(text, mcpInvocationRejectedPrefix) {
		return text
	}
	rest := strings.TrimPrefix(text, mcpInvocationRejectedPrefix)
	rest = strings.TrimPrefix(rest, "：")
	if rest == "" {
		// 无原因：存前缀本身（与 spec §3.4 一致，便于按「无原因拒绝」定位）。
		return text
	}
	return rest
}

// mcpFirstTextContent 只取 Content 里第一条文本块（非文本内容块跳过，绝不读第二条及以后）。
func mcpFirstTextContent(res *mcp.CallToolResult) string {
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

// mcpSummarizeArguments 把参数原文压成 (target_digest, arg_keys) 两列摘要（spec §3.3 三档规则）。
//
// 只解析顶层对象；不是 JSON 对象 / 解析失败 → 两个空串（arg_bytes 由调用方照记原文长度），
// **绝不因解析失败而回落存原文**。
func mcpSummarizeArguments(args json.RawMessage) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
		return "", ""
	}
	return mcpTargetDigest(obj), mcpArgumentKeys(obj)
}

// mcpTargetDigest 取 A 档目标标识：按**声明顺序**（不是参数出现顺序）取前 3 个「存在且为标量」的键，
// 拼成 键=值，用 ; 连接，再整体截断到 255。数组 / 对象 / null 一律跳过（不取值、不占名额）。
func mcpTargetDigest(obj map[string]json.RawMessage) string {
	parts := make([]string, 0, mcpInvocationDigestFields)
	for _, key := range mcpTargetDigestKeys {
		if len(parts) >= mcpInvocationDigestFields {
			break
		}
		raw, ok := obj[key]
		if !ok {
			continue
		}
		value, ok := mcpScalarValue(raw)
		if !ok {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	if len(parts) == 0 {
		return ""
	}
	// 走 redact 兜底防 ?token= 之类混入（ADR-0057：path 属运维定位上下文、本身不打码）。
	return mcpClampRunes(redact.Desensitize(strings.Join(parts, ";")), mcpInvocationDigestMax)
}

// mcpScalarValue 把一个 JSON 值压成简短标量文本：字符串去首尾空白后截断 96 字符（超长加省略号）；
// 数字 / 布尔取 JSON 紧凑形式；数组 / 对象 / null 返回 ok=false。
func mcpScalarValue(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return "", false
		}
		return mcpClampRunes(s, mcpInvocationTextMax), true
	case '{', '[':
		return "", false
	default:
		var compact any
		if err := json.Unmarshal(raw, &compact); err != nil {
			return "", false
		}
		if compact == nil {
			return "", false
		}
		if b, err := json.Marshal(compact); err == nil {
			return mcpClampRunes(string(b), mcpInvocationTextMax), true
		}
		return mcpClampRunes(trimmed, mcpInvocationTextMax), true
	}
}

// mcpArgumentKeys 生成 arg_keys：全部顶层键按键名升序（Go 默认字符串序）拼接，`,` 分隔；
// 最多 24 项；合计超 512 字符则从尾部截断并追加省略号（保留前缀，保证头部项稳定可断言）。
//
// 判定顺序 A → B → C（先命中先归属）：
//   - A 档（目标标识白名单）：记裸键名（其值已在 target_digest，不重复）；
//   - B 档（内容类键）：记 键名:字节数（字节数 = 该值 JSON 紧凑序列化后的长度），**值绝不入库**；
//   - C 档（其余键）：只记裸键名。
func mcpArgumentKeys(obj map[string]json.RawMessage) string {
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > mcpInvocationArgKeysEntries {
		names = names[:mcpInvocationArgKeysEntries]
	}
	items := make([]string, 0, len(names))
	for _, name := range names {
		if mcpIsTargetDigestKey(name) {
			items = append(items, name)
			continue
		}
		if mcpIsContentKey(name) {
			items = append(items, name+":"+strconv.Itoa(mcpCompactValueLen(obj[name])))
			continue
		}
		items = append(items, name)
	}
	joined := strings.Join(items, ",")
	if len(joined) <= mcpInvocationArgKeysMax {
		return joined
	}
	return mcpTruncateBytesWithEllipsis(joined, mcpInvocationArgKeysMax)
}

// mcpIsTargetDigestKey 判定键是否属 A 档白名单（顺序无关的成员判定）。
func mcpIsTargetDigestKey(name string) bool {
	for _, key := range mcpTargetDigestKeys {
		if key == name {
			return true
		}
	}
	return false
}

// mcpIsContentKey 判定键是否属 B 档内容类（键名小写后包含任一子串即命中）。
func mcpIsContentKey(name string) bool {
	lower := strings.ToLower(name)
	for _, sub := range mcpContentKeySubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// mcpCompactValueLen 取一个值 JSON 紧凑序列化后的长度（数字 / 布尔原样，字符串去掉引号外包裹后的原文长度）。
func mcpCompactValueLen(raw json.RawMessage) int {
	var compact any
	if err := json.Unmarshal(raw, &compact); err != nil {
		// 无法解析（理论上不会）：退化为原文长度，仍只记长度、不记值。
		return len(raw)
	}
	if s, ok := compact.(string); ok {
		return len(s)
	}
	if b, err := json.Marshal(compact); err == nil {
		return len(b)
	}
	return len(raw)
}

// mcpClampRunes 按字符（rune）数截断并加省略号：绝不切碎多字节字符。
func mcpClampRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-1]) + "…"
}

// mcpTruncateBytesWithEllipsis 按字节上限从尾部截断并追加省略号（保留前缀），且不切碎多字节字符。
func mcpTruncateBytesWithEllipsis(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

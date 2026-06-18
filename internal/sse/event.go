// Package sse 提供 server→agent 单向 SSE 推送的事件编码（纯函数，无 IO）。
// 流只发"变更通知"，不搬数据：事件载荷是轻量 JSON（通道名 + 新 md5），
// agent 收到后用现有 HTTP 端点取内容并应用（见 ADR-0015）。
package sse

import (
	"encoding/json"
	"strings"
)

// 事件类型常量：当前三条通道的变更通知 + 预留的命令/拓扑事件（后续 FR 复用本流，按需启用）。
const (
	// EventConfigChanged 配置（通道A）有效配置变更通知。
	EventConfigChanged = "config-changed"
	// EventFileChanged 文件树（通道B）有效清单变更通知。
	EventFileChanged = "file-changed"
	// EventOverrideChanged 三方覆盖集（FR-15）适用集合变更通知。
	EventOverrideChanged = "override-changed"
	// EventReady 首轮对账完成标记：agent 收到即知"落下的增量已补发完、转入直播"。
	EventReady = "ready"
	// EventPing 保活心跳：编码为 SSE 注释行，agent 收到不触发任何取数据，仅维持连接/穿透反代空闲超时。
	EventPing = "ping"
)

// Event 是一条 SSE 推送事件：type 决定 agent 走哪条取数据-应用逻辑，data 为对应通道的新 md5（通知式，不含内容）。
type Event struct {
	// 事件类型，取上面的常量之一
	Type string
	// 该通道的新 md5（ready 事件为空）
	MD5 string
}

// changedPayload 是 *-changed 事件的 JSON 载荷（仅新 md5，agent 据此决定是否重取）。
type changedPayload struct {
	MD5 string `json:"md5"`
}

// Encode 把一条事件编码为合法 SSE 报文（event: 行 + data: 行 + 空行结尾）。
// 纯函数：相同入参得相同输出，便于单测穷举各事件类型。
func Encode(e Event) string {
	// 保活心跳编码为 SSE 注释行（冒号开头）：维持连接但不构成事件，agent 解析时跳过。
	if e.Type == EventPing {
		return ": ping\n\n"
	}
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(e.Type)
	b.WriteByte('\n')
	b.WriteString("data: ")
	b.WriteString(encodeData(e))
	// 单条事件以一个空行结束（SSE 帧分隔）。
	b.WriteString("\n\n")
	return b.String()
}

// encodeData 生成 data 行的 JSON 文本：ready 为最小占位对象，其余携带新 md5。
func encodeData(e Event) string {
	if e.Type == EventReady {
		return "{}"
	}
	// 单层对象不会编码失败，忽略 err 安全。
	raw, _ := json.Marshal(changedPayload{MD5: e.MD5})
	return string(raw)
}

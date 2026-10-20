package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 审计 detail 必须永远是合法 JSON —— 回归 issue #144。
//
// 缺陷形态：detail 用 `fmt.Sprintf` 手写 JSON、字符串值用 Go 的 `%q` 转义。`%q` 与 JSON 转义**不等价**：
// 对 ASCII 控制字符它输出 `\x01`，而 JSON 规范不允许 `\x` 转义，detail 于是变成非法 JSON；
// audit_log.detail 是 text 列、写入前无校验，非法内容会静默落库，之后依赖 detail 做 JSON 解析的
// 审计检索 / 导出 / 前端展示会报错或丢字段。
//
// 这条用例刻意让外部可控输入带上控制字符与双引号：
//   - 若有人把实现改回 `%q` 拼接，detail 会出现 `\x01`，json.Valid 立刻变 false；
//   - 若改回 `"%s"` 直接塞引号内（zone_service 旧写法），双引号会提前闭合字符串，同样非法。
func TestAuditDetailAlwaysValidJSON(t *testing.T) {
	// 同时覆盖：控制字符（%q 会写成 \x01）、双引号（"%s" 会破坏结构）、反斜杠（转义配错会破坏结构）。
	const hostilePath = "plugins/\x01bad\x1b\"quoted\"\\tail.yml"

	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)

	if _, err := svc.RequestBrowse(context.Background(), BrowseParams{
		Namespace: "prod", ServerID: "lobby-1",
		Op: model.BrowseOpFile, Path: hostilePath,
		Operator: "alice", ClientIP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("带控制字符的浏览 path 不应被拒（本次只校验审计 detail 的 JSON 合法性）: %v", err)
	}

	var audit model.AuditLog
	if err := db.Where("action = ?", model.ActionFileBrowse).First(&audit).Error; err != nil {
		t.Fatalf("应写入浏览命令审计: %v", err)
	}
	if !json.Valid([]byte(audit.Detail)) {
		t.Fatalf("审计 detail 必须是合法 JSON，实际 %q", audit.Detail)
	}
	// 合法还不够：语义不能丢，path 必须原样可解回（控制字符应被 JSON 合规转义而不是被丢弃）。
	var detail map[string]any
	if err := json.Unmarshal([]byte(audit.Detail), &detail); err != nil {
		t.Fatalf("审计 detail 应可解析: %v", err)
	}
	if got, _ := detail["path"].(string); got != hostilePath {
		t.Fatalf("detail.path 必须与入参一致：want=%q got=%q", hostilePath, got)
	}
	if got, _ := detail["op"].(string); got != model.BrowseOpFile {
		t.Fatalf("detail.op 必须与入参一致：want=%q got=%q", model.BrowseOpFile, got)
	}
	if _, ok := detail["commandId"]; !ok {
		t.Fatalf("detail 应保留 commandId 字段，实际 %q", audit.Detail)
	}
}

// TestAuditDetailValidJSONForServerID 覆盖另一条外部可控入口（serverId）的同类回归。
//
// serverId 来自 URL 路径参数，同样可能带控制字符；这里直接构造命令并核对写出的审计 detail。
func TestAuditDetailValidJSONForTailLogsCommand(t *testing.T) {
	const hostileServerID = "lobby-\x01\"1\""

	db := newCommandSvcTestDB(t)
	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))

	if _, err := logs.RequestTailLogs("prod", hostileServerID, "alice", "127.0.0.1"); err != nil {
		t.Fatalf("取日志命令创建失败: %v", err)
	}
	var audit model.AuditLog
	if err := db.Where("action = ?", model.ActionInstanceTailLogs).First(&audit).Error; err != nil {
		t.Fatalf("应写入取日志命令审计: %v", err)
	}
	if !json.Valid([]byte(audit.Detail)) {
		t.Fatalf("审计 detail 必须是合法 JSON，实际 %q", audit.Detail)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(audit.Detail), &detail); err != nil {
		t.Fatalf("审计 detail 应可解析: %v", err)
	}
	if got, _ := detail["serverId"].(string); got != hostileServerID {
		t.Fatalf("detail.serverId 必须与入参一致：want=%q got=%q", hostileServerID, got)
	}
}

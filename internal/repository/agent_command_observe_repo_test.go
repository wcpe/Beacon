package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
)

// mkCmd 建一条指定环境 / 实例 / 类型 / 状态的命令（瞬态字段填值以验投影排除）。
func mkCmd(ns, serverID, cmdType, status string) *model.AgentCommand {
	return &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: cmdType, Payload: `{"scope":"group"}`,
		Status:       status,
		ResultDetail: "ok-3-files",
		// 瞬态敏感字段填值：用于断言观测投影绝不带出
		ImprintContent: "SECRET-IMPRINT",
		LogContent:     "SECRET-LOG",
		Operator:       "admin",
	}
}

// TestCommandListFilterAndProject 列表按过滤分页倒序，且投影绝不带出 imprint/log/payload（FR-104 不返敏感瞬态）。
func TestCommandListFilterAndProject(t *testing.T) {
	repo := NewAgentCommandRepository(newCommandTestDB(t))
	_ = repo.Create(mkCmd("prod", "lobby-1", model.CommandTypeIngestPlugins, model.CommandStatusDone))
	_ = repo.Create(mkCmd("prod", "lobby-1", model.CommandTypeTailLogs, model.CommandStatusPending))
	_ = repo.Create(mkCmd("prod", "lobby-2", model.CommandTypeResyncConfig, model.CommandStatusFailed))
	_ = repo.Create(mkCmd("stg", "lobby-9", model.CommandTypeIngestPlugins, model.CommandStatusDone))

	// 按环境过滤：prod 三条
	items, total, err := repo.List(CommandFilter{Namespace: "prod", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("prod 应 3 条，实际 total=%d len=%d", total, len(items))
	}
	// 投影绝不带出敏感 / 瞬态字段（CommandMeta 无这些字段，此处验结果摘要保留、其余元数据完整）
	if items[0].ResultDetail != "ok-3-files" || items[0].Operator != "admin" {
		t.Fatalf("元数据应完整: %+v", items[0])
	}

	// 倒序：创建时间倒序（id 越大越靠前）
	if !(items[0].ID > items[1].ID && items[1].ID > items[2].ID) {
		t.Fatalf("应按 createdAt/id 倒序: %+v", items)
	}

	// 按实例 + 类型 + 状态联合过滤
	got, total2, _ := repo.List(CommandFilter{Namespace: "prod", ServerID: "lobby-1", Type: model.CommandTypeTailLogs, Status: model.CommandStatusPending, Page: 1, Size: 20})
	if total2 != 1 || len(got) != 1 || got[0].Type != model.CommandTypeTailLogs {
		t.Fatalf("联合过滤应命中 1 条 tail-logs，实际 total=%d %+v", total2, got)
	}

	// 分页：每页 2，第二页 1 条
	p1, _, _ := repo.List(CommandFilter{Namespace: "prod", Page: 1, Size: 2})
	p2, _, _ := repo.List(CommandFilter{Namespace: "prod", Page: 2, Size: 2})
	if len(p1) != 2 || len(p2) != 1 {
		t.Fatalf("分页应 2+1，实际 %d+%d", len(p1), len(p2))
	}
}

// TestCommandScanForAnalytics 窗口内取聚合投影行，按时间升序，复用 namespace/from/to 过滤。
func TestCommandScanForAnalytics(t *testing.T) {
	db := newCommandTestDB(t)
	repo := NewAgentCommandRepository(db)
	_ = repo.Create(mkCmd("prod", "a", model.CommandTypeIngestPlugins, model.CommandStatusDone))
	_ = repo.Create(mkCmd("prod", "b", model.CommandTypeTailLogs, model.CommandStatusFailed))
	_ = repo.Create(mkCmd("stg", "c", model.CommandTypeIngestPlugins, model.CommandStatusDone))

	// 把 prod 两条改到窗口内不同日，stg 一条改到窗口外
	now := time.Now().UTC()
	_ = db.Model(&model.AgentCommand{}).Where("server_id = ?", "a").Update("created_at", now.Add(-48*time.Hour))
	_ = db.Model(&model.AgentCommand{}).Where("server_id = ?", "b").Update("created_at", now.Add(-1*time.Hour))
	_ = db.Model(&model.AgentCommand{}).Where("server_id = ?", "c").Update("created_at", now.Add(-1*time.Hour))

	rows, err := repo.ScanForAnalytics(CommandFilter{Namespace: "prod", From: now.Add(-72 * time.Hour), To: now})
	if err != nil {
		t.Fatalf("ScanForAnalytics 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("prod 窗口内应 2 行（stg 不串），实际 %d", len(rows))
	}
	// 升序：第一行 created_at 更早
	if !rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatalf("应按 created_at 升序: %+v", rows)
	}
	if rows[0].ServerID != "a" || rows[0].Type != model.CommandTypeIngestPlugins {
		t.Fatalf("投影字段不一致: %+v", rows[0])
	}
}

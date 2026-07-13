package service

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestV2DefaultEntryServerIDsResolvesFromServerColumn 复现 default-entry 双真源断层（ADR-0067）：
// 管理台唯一可写的 v2 server.is_default_entry 必须被发现 / 实例视图的默认入口解析器消费，
// 否则 BC fallback 注入永远看不到管理台的默认入口变更。
func TestV2DefaultEntryServerIDsResolvesFromServerColumn(t *testing.T) {
	f := setupFR155Cluster(t)
	rowA := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	rowB := f.approveServer(t, fr155IdentityB, "lobby-2", model.ServerKindBackend)
	f.assign(t, rowA, model.AssignmentTargetZone, f.zoneA.ID, true)  // 分配时勾选默认入口
	f.assign(t, rowB, model.AssignmentTargetZone, f.zoneA.ID, false) // 同小区非默认入口

	set, err := f.svc.DefaultEntryServerIDs(f.ns.Code)
	if err != nil {
		t.Fatalf("解析默认入口集合失败: %v", err)
	}
	if !set["lobby-1"] || set["lobby-2"] {
		t.Fatalf("默认入口集合应只含 lobby-1，实际 %v", set)
	}

	// 独立 toggle 清除后集合应同步为空。
	if _, err := f.svc.SetServerDefaultEntry(SetServerDefaultEntryParams{ServerRowID: rowA, Value: false, Operator: "admin"}); err != nil {
		t.Fatalf("清除默认入口失败: %v", err)
	}
	set, err = f.svc.DefaultEntryServerIDs(f.ns.Code)
	if err != nil {
		t.Fatalf("再次解析失败: %v", err)
	}
	if len(set) != 0 {
		t.Fatalf("清除后默认入口集合应为空，实际 %v", set)
	}

	// namespace 隔离：未知 namespace 返回空集不报错（发现链路不因坏参数中断）。
	set, err = f.svc.DefaultEntryServerIDs("no-such-ns")
	if err != nil || len(set) != 0 {
		t.Fatalf("未知 namespace 应空集无错，实际 %v err=%v", set, err)
	}
}

// TestV2ListDefaultEntries 列表视图 v2 背书（v1 GET /admin/v1/zones/default-entry 兼容形状，Legacy 只读消费）：
// group 过滤按大区名匹配，行含 (namespace, group, zone, serverId)。
func TestV2ListDefaultEntries(t *testing.T) {
	f := setupFR155Cluster(t)
	rowA := f.approveServer(t, fr155IdentityA, "lobby-1", model.ServerKindBackend)
	f.assign(t, rowA, model.AssignmentTargetZone, f.zoneA.ID, true)

	items, err := f.svc.ListDefaultEntries(f.ns.Code, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("应列出 1 条默认入口，实际 %d err=%v", len(items), err)
	}
	got := items[0]
	if got.Namespace != f.ns.Code || got.Group != "r1" || got.Zone != "z-a" || got.DefaultServerID != "lobby-1" {
		t.Fatalf("默认入口行不符: %+v", got)
	}

	// group 过滤：命中与不命中。
	if items, err = f.svc.ListDefaultEntries(f.ns.Code, "r1"); err != nil || len(items) != 1 {
		t.Fatalf("group=r1 应 1 条，实际 %d err=%v", len(items), err)
	}
	if items, err = f.svc.ListDefaultEntries(f.ns.Code, "r-none"); err != nil || len(items) != 0 {
		t.Fatalf("group=r-none 应 0 条，实际 %d err=%v", len(items), err)
	}
}

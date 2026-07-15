package repository

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// activeBlobOrderStatuses 是数据面归属校验认可的活动 / 已审批单状态集（与 service 层 deliveryBlobOrderStatuses 同口径）。
var activeBlobOrderStatuses = []string{
	model.ChangeOrderStatusApproved, model.ChangeOrderStatusRolling,
	model.ChangeOrderStatusPaused, model.ChangeOrderStatusRollingBack,
}

// blobTerminalStatuses 是清理视角的已了结单状态集（与 service 层 changeOrderTerminalStatuses 同口径）。
var blobTerminalStatuses = []string{
	model.ChangeOrderStatusCompleted, model.ChangeOrderStatusCancelled, model.ChangeOrderStatusRolledBack,
}

// TestDeliveryConfigArtifactUpsertAndList UpsertBatch 幂等覆盖（同键不新增行、覆盖 sha/size）+ 按单目标列取（path 升序）。
func TestDeliveryConfigArtifactUpsertAndList(t *testing.T) {
	db := newDeliveryTestDB(t)
	repo := NewDeliveryConfigArtifactRepository(db)

	if err := repo.UpsertBatch([]model.DeliveryConfigArtifact{
		{OrderID: 1, ServerID: "t-1", Path: "plugins/a.yml", SHA256: "sha-a", SizeBytes: 3},
		{OrderID: 1, ServerID: "t-1", Path: "plugins/b.yml", SHA256: "sha-b", SizeBytes: 4},
	}); err != nil {
		t.Fatalf("首次落工件失败: %v", err)
	}
	// 同 (单, 目标, 路径) 覆盖：a.yml 的 sha 与 size 改写，不新增行。
	if err := repo.UpsertBatch([]model.DeliveryConfigArtifact{
		{OrderID: 1, ServerID: "t-1", Path: "plugins/a.yml", SHA256: "sha-a2", SizeBytes: 9},
	}); err != nil {
		t.Fatalf("覆盖落工件失败: %v", err)
	}
	arts, err := repo.ListByOrderServer(1, "t-1")
	if err != nil {
		t.Fatalf("列取失败: %v", err)
	}
	if len(arts) != 2 {
		t.Fatalf("同键覆盖不应新增行，期望 2 实际 %d", len(arts))
	}
	if arts[0].Path != "plugins/a.yml" || arts[0].SHA256 != "sha-a2" || arts[0].SizeBytes != 9 {
		t.Fatalf("a.yml 应被覆盖为 sha-a2/9（path 升序在前），实际 %+v", arts[0])
	}
	// 空入参 no-op，不报错。
	if err := repo.UpsertBatch(nil); err != nil {
		t.Fatalf("空 upsert 应 no-op: %v", err)
	}
}

// TestDeliveryConfigArtifactExistsAuthorizedSHA 下载授权反查：活动单本目标命中；非目标 / 跨 namespace / 空状态集 / 单转终态不命中。
func TestDeliveryConfigArtifactExistsAuthorizedSHA(t *testing.T) {
	db := newDeliveryTestDB(t)
	repo := NewDeliveryConfigArtifactRepository(db)
	order := mkOrder(7, model.ChangeOrderStatusRolling)
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("建活动单失败: %v", err)
	}
	if err := repo.UpsertBatch([]model.DeliveryConfigArtifact{
		{OrderID: order.ID, ServerID: "t-1", Path: "plugins/a.yml", SHA256: "sha-a", SizeBytes: 3},
	}); err != nil {
		t.Fatalf("落工件失败: %v", err)
	}

	if ok, err := repo.ExistsAuthorizedSHA(7, "t-1", "sha-a", activeBlobOrderStatuses); err != nil || !ok {
		t.Fatalf("活动单本目标应授权，ok=%v err=%v", ok, err)
	}
	if ok, _ := repo.ExistsAuthorizedSHA(7, "t-2", "sha-a", activeBlobOrderStatuses); ok {
		t.Fatal("非目标 serverID 不应授权")
	}
	if ok, _ := repo.ExistsAuthorizedSHA(8, "t-1", "sha-a", activeBlobOrderStatuses); ok {
		t.Fatal("跨 namespace 不应授权")
	}
	if ok, _ := repo.ExistsAuthorizedSHA(7, "t-1", "sha-a", nil); ok {
		t.Fatal("空状态集不应授权")
	}
	// 单转终态（completed 不在活动集）后不再授权。
	if err := db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).
		Update("status", model.ChangeOrderStatusCompleted).Error; err != nil {
		t.Fatalf("置单终态失败: %v", err)
	}
	if ok, _ := repo.ExistsAuthorizedSHA(7, "t-1", "sha-a", activeBlobOrderStatuses); ok {
		t.Fatal("单转终态后不应授权")
	}
}

// TestDeliveryConfigArtifactCleanupGuardAndListSHAs 清理护栏：非终态单 config 工件 sha 受保护、终态单不受保护；ListSHAsByOrder 去重。
func TestDeliveryConfigArtifactCleanupGuardAndListSHAs(t *testing.T) {
	db := newDeliveryTestDB(t)
	repo := NewDeliveryConfigArtifactRepository(db)
	rolling := mkOrder(1, model.ChangeOrderStatusRolling)
	done := mkOrder(1, model.ChangeOrderStatusCompleted)
	if err := db.Create(rolling).Error; err != nil {
		t.Fatalf("建活动单失败: %v", err)
	}
	if err := db.Create(done).Error; err != nil {
		t.Fatalf("建终态单失败: %v", err)
	}
	// rolling 单同 sha 两目标（验证 ListSHAsByOrder 去重）。
	if err := repo.UpsertBatch([]model.DeliveryConfigArtifact{
		{OrderID: rolling.ID, ServerID: "t-1", Path: "plugins/a.yml", SHA256: "sha-live", SizeBytes: 3},
		{OrderID: rolling.ID, ServerID: "t-2", Path: "plugins/a.yml", SHA256: "sha-live", SizeBytes: 3},
	}); err != nil {
		t.Fatalf("落活动单工件失败: %v", err)
	}
	if err := repo.UpsertBatch([]model.DeliveryConfigArtifact{
		{OrderID: done.ID, ServerID: "t-1", Path: "plugins/a.yml", SHA256: "sha-done", SizeBytes: 3},
	}); err != nil {
		t.Fatalf("落终态单工件失败: %v", err)
	}

	shas, err := repo.ListSHAsByOrder(rolling.ID)
	if err != nil {
		t.Fatalf("取单工件 sha 失败: %v", err)
	}
	if len(shas) != 1 || shas[0] != "sha-live" {
		t.Fatalf("同 sha 两目标应去重为 1，实际 %v", shas)
	}

	blocked, err := repo.ListSHAsReferencedByStatusNotIn([]string{"sha-live", "sha-done"}, blobTerminalStatuses)
	if err != nil {
		t.Fatalf("清理护栏反查失败: %v", err)
	}
	if _, ok := blocked["sha-live"]; !ok {
		t.Fatal("非终态单的 config 工件 sha 应受保护（阻断清理）")
	}
	if _, ok := blocked["sha-done"]; ok {
		t.Fatal("终态单的 config 工件 sha 不应阻断清理")
	}
}

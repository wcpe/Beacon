package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件覆盖 FR-232 的第二类自动消解触发点：实例 / 环境被**归档或永久删除**时，其未处理告警必须自动关闭。
// 背景：这类实例已从 server 表消失，不可能再走「恢复 online」消解，此前其告警会永久滞留 open
// （prod 实测 155 条 open 告警涉及 53 个已不存在的实例）。
// 所有用例都同时断言**同环境其它实例 / 其它环境**的告警原样未动——一条 UPDATE 打宽了会把无关告警
// 误标为「已处理」，运维再也看不到真正待办，这比不消解更危险。

// seedLifecycleAlert 直插一条指定状态的告警（绕过 Record 的 FR-232 收敛，便于精确构造行）。
func seedLifecycleAlert(t *testing.T, db *gorm.DB, ns, serverID, status string) uint {
	t.Helper()
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
		Namespace: ns, ServerID: serverID, Message: ns + "/" + serverID + " 状态异常",
		Status: status, OccurrenceCount: 1,
	}
	if err := db.Create(e).Error; err != nil {
		t.Fatalf("落库告警失败: %v", err)
	}
	return e.ID
}

// assertAlertAutoResolved 断言该行已被系统自动消解（resolved + handled_by=system + 指定 note），返回落库结果。
func assertAlertAutoResolved(t *testing.T, db *gorm.DB, id uint, note string) model.AlertEvent {
	t.Helper()
	var got model.AlertEvent
	if err := db.First(&got, id).Error; err != nil {
		t.Fatalf("回读告警 %d 失败: %v", id, err)
	}
	if got.Status != model.AlertEventStatusResolved || got.HandledBy != model.AutoResolveOperator || got.HandledAt == nil || got.HandleNote != note {
		t.Fatalf("告警 %d 应被自动消解（resolved / handled_by=system / note=%q），实际 %+v", id, note, got)
	}
	return got
}

// assertAlertUntouched 断言该行未被触碰（防误伤：别的实例 / 别的环境的告警必须仍可处理）。
func assertAlertUntouched(t *testing.T, db *gorm.DB, id uint, wantStatus string) {
	t.Helper()
	var got model.AlertEvent
	if err := db.First(&got, id).Error; err != nil {
		t.Fatalf("回读告警 %d 失败: %v", id, err)
	}
	if got.Status != wantStatus || got.HandledBy != "" || got.HandledAt != nil || got.HandleNote != "" {
		t.Fatalf("告警 %d 不应被触碰（应为 %q 且无处理痕迹），实际 %+v", id, wantStatus, got)
	}
}

// TestServerArchiveAutoResolvesAlertsWithoutCollateral 实例归档：该实例 open / acknowledged 告警全部消解，
// 同环境兄弟实例与另一环境的同名实例告警不受影响。
func TestServerArchiveAutoResolvesAlertsWithoutCollateral(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	openID := seedLifecycleAlert(t, db, "prod", server.ServerID, model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, "prod", server.ServerID, model.AlertEventStatusAcknowledged)
	siblingID := seedLifecycleAlert(t, db, "prod", "game-2", model.AlertEventStatusOpen)
	otherEnvID := seedLifecycleAlert(t, db, "dev", server.ServerID, model.AlertEventStatusOpen)

	requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "archive-alerts", server.ID, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))

	assertAlertAutoResolved(t, db, openID, "实例已归档，自动消解")
	assertAlertAutoResolved(t, db, ackID, "实例已归档，自动消解")
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, otherEnvID, model.AlertEventStatusOpen)
}

// TestServerPermanentDeleteAutoResolvesAlertsWithoutCollateral 实例永久删除（审批真路径
// RequestPermanentDeleteServer → applyServerPermanentDelete）：告警消解且不误伤其它实例 / 环境。
func TestServerPermanentDeleteAutoResolvesAlertsWithoutCollateral(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2 := approval.preparer.(*V2ControlPlaneService)
	requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "permanent-delete-archive", server.ID, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	// 归档后才落告警：确保消解确由永久删除这一步触发，而非上一步归档。
	openID := seedLifecycleAlert(t, db, "prod", server.ServerID, model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, "prod", server.ServerID, model.AlertEventStatusAcknowledged)
	siblingID := seedLifecycleAlert(t, db, "prod", "game-2", model.AlertEventStatusOpen)
	otherEnvID := seedLifecycleAlert(t, db, "dev", server.ServerID, model.AlertEventStatusOpen)

	ticket, err := v2.RequestPermanentDeleteServer(server.ID, NamespaceLifecycleParams{
		Reason: "永久删除服务", Operator: "alice", ClientIP: "127.0.0.1", Confirmation: server.ServerID,
	}, auth.HumanPrincipal("alice"), "permanent-delete-alerts")
	if err != nil {
		t.Fatalf("提审永久删除服务失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准永久删除服务失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行永久删除服务失败，processed=%d err=%v", processed, err)
	}

	assertAlertAutoResolved(t, db, openID, "实例已永久删除，自动消解")
	assertAlertAutoResolved(t, db, ackID, "实例已永久删除，自动消解")
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, otherEnvID, model.AlertEventStatusOpen)
}

// TestServerPermanentDeleteViaLifecycleUpdateAutoResolvesAlerts 覆盖另一条永久删除落地路径
// （updateServerLifecycle 内与 expireServerCommands 并列的分支）：审批适配器不会路由到它，
// 但该分支确实存在，直接驱动以免「加了分支却没测」。
func TestServerPermanentDeleteViaLifecycleUpdateAutoResolvesAlerts(t *testing.T) {
	_, db, server := newServerLifecycleTestSuite(t)
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("lifecycle", model.ServerLifecycleArchived).Error; err != nil {
		t.Fatalf("置为已归档失败: %v", err)
	}
	openID := seedLifecycleAlert(t, db, "prod", server.ServerID, model.AlertEventStatusOpen)
	siblingID := seedLifecycleAlert(t, db, "prod", "game-2", model.AlertEventStatusOpen)

	payload := serverLifecyclePayload{
		OperationKey: authz.OperationServerPermanentDelete, Operator: "human:alice", ArchiveReason: "永久删除",
		ExpectedLifecycle: model.ServerLifecycleArchived,
		Server:            serverLifecycleSnapshot{ID: server.ID, NamespaceID: server.NamespaceID, ServerID: server.ServerID},
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return updateServerLifecycle(tx, payload, authz.Permit{}, authz.OperationServerPermanentDelete)
	}); err != nil {
		t.Fatalf("永久删除分支执行失败: %v", err)
	}

	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil || saved.Lifecycle != model.ServerLifecycleTombstoned {
		t.Fatalf("实例应进入墓碑状态，server=%+v err=%v", saved, err)
	}
	assertAlertAutoResolved(t, db, openID, "实例已永久删除，自动消解")
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
}

// TestNamespaceArchiveAutoResolvesAlertsWithoutCollateral 环境归档：该环境下全部实例（含集群级无实例告警）
// 的告警消解，另一环境的告警不受影响。
func TestNamespaceArchiveAutoResolvesAlertsWithoutCollateral(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	code := lifecycleNamespaceCode(t, db, server.NamespaceID)
	openID := seedLifecycleAlert(t, db, code, server.ServerID, model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, code, "game-2", model.AlertEventStatusAcknowledged)
	clusterID := seedLifecycleAlert(t, db, code, "", model.AlertEventStatusOpen)
	otherEnvID := seedLifecycleAlert(t, db, "dev", server.ServerID, model.AlertEventStatusOpen)

	requestAndApproveNamespaceLifecycleWithKey(t, approval, server.NamespaceID, "namespace-archive-alerts", false)

	assertAlertAutoResolved(t, db, openID, "环境已归档，自动消解")
	assertAlertAutoResolved(t, db, ackID, "环境已归档，自动消解")
	assertAlertAutoResolved(t, db, clusterID, "环境已归档，自动消解")
	assertAlertUntouched(t, db, otherEnvID, model.AlertEventStatusOpen)
}

// TestNamespacePermanentDeleteAutoResolvesAlertsWithoutCollateral 环境永久删除：同上，note 区分删除语义。
func TestNamespacePermanentDeleteAutoResolvesAlertsWithoutCollateral(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	code := lifecycleNamespaceCode(t, db, server.NamespaceID)
	requestAndApproveNamespaceLifecycleWithKey(t, approval, server.NamespaceID, "namespace-delete-archive", false)
	openID := seedLifecycleAlert(t, db, code, server.ServerID, model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, code, "game-2", model.AlertEventStatusAcknowledged)
	otherEnvID := seedLifecycleAlert(t, db, "dev", server.ServerID, model.AlertEventStatusOpen)

	requestAndApproveNamespaceLifecycleWithKey(t, approval, server.NamespaceID, "namespace-permanent-delete-alerts", true)

	assertAlertAutoResolved(t, db, openID, "环境已永久删除，自动消解")
	assertAlertAutoResolved(t, db, ackID, "环境已永久删除，自动消解")
	assertAlertUntouched(t, db, otherEnvID, model.AlertEventStatusOpen)
}

// TestLifecycleAlertAutoResolveIsIdempotent 重复执行不报错、不改写既有处理痕迹：
// 归档 → 恢复 → 再归档（无未处理行可关）；helper 重复调用同样安全。
func TestLifecycleAlertAutoResolveIsIdempotent(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	requester, approver := auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob")
	code := lifecycleNamespaceCode(t, db, server.NamespaceID)
	alertID := seedLifecycleAlert(t, db, code, server.ServerID, model.AlertEventStatusOpen)

	requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "idempotent-archive-1", server.ID, requester, approver)
	first := assertAlertAutoResolved(t, db, alertID, "实例已归档，自动消解")

	requestAndApproveLifecycle(t, approval, authz.OperationServerRestore, "idempotent-restore", server.ID, requester, approver)
	requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "idempotent-archive-2", server.ID, requester, approver)
	second := assertAlertAutoResolved(t, db, alertID, "实例已归档，自动消解")
	if !second.HandledAt.Equal(*first.HandledAt) {
		t.Fatalf("重复归档不得改写已消解告警的处理时刻，first=%v second=%v", first.HandledAt, second.HandledAt)
	}

	// 无未处理行时重复调用 helper：仍返回 nil（关闭失败会让生命周期操作整体失败，属不可接受的回归）。
	if err := autoResolveServerAlerts(db, server.NamespaceID, server.ServerID, "实例已归档，自动消解"); err != nil {
		t.Fatalf("重复消解实例告警不应报错: %v", err)
	}
	if err := autoResolveNamespaceAlerts(db, code, "环境已归档，自动消解"); err != nil {
		t.Fatalf("重复消解环境告警不应报错: %v", err)
	}
}

// lifecycleNamespaceCode 取 namespace code（alert_event.namespace 存的是 code 而非主键 id）。
func lifecycleNamespaceCode(t *testing.T, db *gorm.DB, namespaceID uint) string {
	t.Helper()
	var namespace model.Namespace
	if err := db.First(&namespace, namespaceID).Error; err != nil {
		t.Fatalf("读取 namespace 失败: %v", err)
	}
	return namespace.Code
}

// requestAndApproveNamespaceLifecycleWithKey 与既有 requestAndApproveNamespaceLifecycle 同流程，
// 区别是允许指定幂等键——幂等用例需要「归档 → 恢复 → 再归档」，复用同一键会被幂等去重挡住。
func requestAndApproveNamespaceLifecycleWithKey(t *testing.T, approval *ApprovalService, namespaceID uint, key string, permanent bool) {
	t.Helper()
	v2 := approval.preparer.(*V2ControlPlaneService)
	params := NamespaceLifecycleParams{Reason: "环境生命周期变更", Operator: "alice", ClientIP: "127.0.0.1"}
	if permanent {
		var namespace model.Namespace
		if err := approval.db.First(&namespace, namespaceID).Error; err != nil {
			t.Fatalf("读取 namespace 失败: %v", err)
		}
		params.Confirmation = namespace.Code
	}
	var ticket ApprovalTicketView
	var err error
	if permanent {
		ticket, err = v2.RequestPermanentDeleteNamespace(namespaceID, params, auth.HumanPrincipal("alice"), key)
	} else {
		ticket, err = v2.RequestArchiveNamespace(namespaceID, params, auth.HumanPrincipal("alice"), key)
	}
	if err != nil {
		t.Fatalf("提审环境生命周期操作失败（key=%s）: %v", key, err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准环境生命周期操作失败（key=%s）: %v", key, err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行环境生命周期操作失败（key=%s），processed=%d err=%v", key, processed, err)
	}
}

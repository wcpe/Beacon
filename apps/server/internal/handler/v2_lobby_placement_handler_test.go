package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

func TestFR199LobbyPlacementHTTPAndReadShape(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, ServerID: "lobby-http", Kind: model.ServerKindBackend, BootID: "boot-http",
	}); err != nil {
		t.Fatalf("注册 backend 失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(identityID, service.ApproveAgentIdentityParams{ServerID: "lobby-http", Operator: "admin"}); err != nil {
		t.Fatalf("确认 backend 失败: %v", err)
	}
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}

	code, body := invokeJSON(h.TransferServerPlacement, http.MethodPost, "/admin/v2/server-placement-transfers", "", map[string]any{
		"serverId": "lobby-http", "target": map[string]any{"kind": "lobby_cluster", "id": lobby.ID}, "reason": "设置首次大厅",
	})
	if code != http.StatusOK || body["placementKind"] != "lobby_cluster" || body["lobbyClusterId"] != float64(lobby.ID) {
		t.Fatalf("迁入大厅 HTTP 响应不符，实际 %d：%v", code, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v2/lobby-clusters?namespaceId="+lobbyUintText(ns.ID), nil)
	rr := httptest.NewRecorder()
	h.ListLobbyClusters(rr, req)
	code, body = decodeRecorder(rr)
	if code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("大厅列表应按 namespace 返回摘要，实际 %d：%v", code, body)
	}

	code, body = invokeJSONWithParam(h.GetLobbyCluster, http.MethodGet,
		"/admin/v2/lobby-clusters/"+lobbyUintText(lobby.ID), "", nil, "id", lobbyUintText(lobby.ID))
	if code != http.StatusOK || body["memberTotal"] != float64(1) {
		t.Fatalf("大厅详情应返回成员分页，实际 %d：%v", code, body)
	}
}

func TestFR199LobbyPlacementRejectsBadTarget(t *testing.T) {
	_, _, h := newV2HandlerTestService(t)
	code, body := invokeJSON(h.TransferServerPlacement, http.MethodPost, "/admin/v2/server-placement-transfers", "", map[string]any{
		"serverId": "lobby-http", "target": map[string]any{"kind": "bad", "id": 1}, "reason": "错误目标",
	})
	if code != http.StatusBadRequest || body["code"] != "INVALID_PARAM" {
		t.Fatalf("非法迁移目标应返回 INVALID_PARAM，实际 %d：%v", code, body)
	}
}

func lobbyUintText(value uint) string {
	return fmt.Sprintf("%d", value)
}

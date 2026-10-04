package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// 本文件覆盖真机接入验收暴露的三个卡点（拓扑创建 / 大厅成员分配的接入体验）：
//  1. 三个创建端点统一接受父级别名 parentId（并与 MCP 建树工具同名同义），旧字段名继续可用；
//  2. name / code / displayName 的拒绝文案必须能指导修正；
//  3. server-assignments 直接支持 target.kind=lobby_cluster，无需改调 placement-transfers 端点。

func createTopologyNode(t *testing.T, handler http.HandlerFunc, path string, body map[string]any) uint {
	t.Helper()
	code, parsed := invokeJSON(handler, http.MethodPost, path, "", body)
	if code != http.StatusCreated {
		t.Fatalf("%s 应 201，实际 %d：%v", path, code, parsed)
	}
	id, ok := parsed["id"].(float64)
	if !ok || id == 0 {
		t.Fatalf("%s 响应缺少 id：%v", path, parsed)
	}
	return uint(id)
}

func assertErrorCodeAndMessage(t *testing.T, code int, body map[string]any, wantCode string, wants ...string) {
	t.Helper()
	if code != http.StatusBadRequest || body["code"] != wantCode {
		t.Fatalf("应 400 %s，实际 %d：%v", wantCode, code, body)
	}
	message, _ := body["message"].(string)
	for _, want := range wants {
		if !strings.Contains(message, want) {
			t.Fatalf("错误文案应包含 %q 以便直接修正，实际 %q", want, message)
		}
	}
}

// TestV2TopologyCreateAcceptsParentIDAlias 校验卡点 1：parentId 是三个创建端点的统一父级字段。
func TestV2TopologyCreateAcceptsParentIDAlias(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	clusterID := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{
		"parentId": ns.ID, "code": "onb-bc1", "displayName": "接入验收集群",
	})
	regionID := createTopologyNode(t, h.CreateRegion, "/admin/v2/regions", map[string]any{
		"parentId": clusterID, "code": "onb-r1", "displayName": "接入验收大区",
	})
	zoneID := createTopologyNode(t, h.CreateZone, "/admin/v2/zones", map[string]any{
		"parentId": regionID, "code": "onb-z1", "displayName": "接入验收小区",
	})

	var cluster model.BCCluster
	if err := db.First(&cluster, clusterID).Error; err != nil {
		t.Fatalf("读取 BC 集群失败: %v", err)
	}
	var region model.Region
	if err := db.First(&region, regionID).Error; err != nil {
		t.Fatalf("读取大区失败: %v", err)
	}
	var zone model.Zone
	if err := db.First(&zone, zoneID).Error; err != nil {
		t.Fatalf("读取小区失败: %v", err)
	}
	if cluster.NamespaceID != ns.ID || cluster.Code != "onb-bc1" || cluster.Name != "接入验收集群" {
		t.Fatalf("parentId 建集群应落到目标 namespace，实际 %+v", cluster)
	}
	if region.BCClusterID != clusterID || zone.RegionID != regionID {
		t.Fatalf("parentId 建大区 / 小区应落到目标父级，实际 region=%+v zone=%+v", region, zone)
	}
}

// TestV2TopologyCreateKeepsLegacyParentFields 校验兼容性：旧字段名与「两者都给且一致」都可继续使用。
func TestV2TopologyCreateKeepsLegacyParentFields(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	// 旧字段名（既有 e2e / 前端路径）：namespaceId / bcClusterId / regionId。
	legacyClusterID := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{
		"namespaceId": ns.ID, "name": "legacy-bc",
	})
	var legacyCluster model.BCCluster
	if err := db.First(&legacyCluster, legacyClusterID).Error; err != nil || legacyCluster.NamespaceID != ns.ID {
		t.Fatalf("旧字段建集群应保持可用，实际 %+v err=%v", legacyCluster, err)
	}
	legacyRegionID := createTopologyNode(t, h.CreateRegion, "/admin/v2/regions", map[string]any{
		"bcClusterId": legacyClusterID, "name": "legacy-r",
	})
	legacyZoneID := createTopologyNode(t, h.CreateZone, "/admin/v2/zones", map[string]any{
		"regionId": legacyRegionID, "name": "legacy-z",
	})
	var legacyZone model.Zone
	if err := db.First(&legacyZone, legacyZoneID).Error; err != nil || legacyZone.RegionID != legacyRegionID {
		t.Fatalf("旧字段建小区应保持可用，实际 %+v err=%v", legacyZone, err)
	}

	// 两个字段都给出且一致：接受（便于调用方在迁移期同时带上新旧字段）。
	bothClusterID := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{
		"namespaceId": ns.ID, "parentId": ns.ID, "code": "both-bc",
	})
	bothRegionID := createTopologyNode(t, h.CreateRegion, "/admin/v2/regions", map[string]any{
		"bcClusterId": bothClusterID, "parentId": bothClusterID, "code": "both-r",
	})
	createTopologyNode(t, h.CreateZone, "/admin/v2/zones", map[string]any{
		"regionId": bothRegionID, "parentId": bothRegionID, "code": "both-z",
	})
}

// TestV2TopologyCreateRejectsConflictingParentFields 校验父级别名与旧字段冲突时报可读参数错误。
func TestV2TopologyCreateRejectsConflictingParentFields(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	clusterID := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{
		"parentId": ns.ID, "code": "conf-bc",
	})
	regionID := createTopologyNode(t, h.CreateRegion, "/admin/v2/regions", map[string]any{
		"parentId": clusterID, "code": "conf-r",
	})

	cases := []struct {
		name        string
		handler     http.HandlerFunc
		path        string
		body        map[string]any
		legacyField string
	}{
		{"BC 集群", h.CreateBCCluster, "/admin/v2/bc-clusters",
			map[string]any{"namespaceId": ns.ID + 1, "parentId": ns.ID, "code": "conflict-bc"}, "namespaceId"},
		{"大区", h.CreateRegion, "/admin/v2/regions",
			map[string]any{"bcClusterId": clusterID + 1, "parentId": clusterID, "code": "conflict-r"}, "bcClusterId"},
		{"小区", h.CreateZone, "/admin/v2/zones",
			map[string]any{"regionId": regionID + 1, "parentId": regionID, "code": "conflict-z"}, "regionId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := invokeJSON(tc.handler, http.MethodPost, tc.path, "", tc.body)
			assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "parentId", tc.legacyField, "冲突")
		})
	}

	// 冲突时不得落库。
	var count int64
	if err := db.Model(&model.BCCluster{}).Where("code = ?", "conflict-bc").Count(&count).Error; err != nil {
		t.Fatalf("统计集群失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("冲突请求不得创建节点，实际 %d 条", count)
	}
}

// TestV2TopologyCreateMissingParentGivesActionableError 校验缺父级时的文案直接点名要传哪个字段。
func TestV2TopologyCreateMissingParentGivesActionableError(t *testing.T) {
	_, _, h := newV2HandlerTestService(t)
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		path        string
		body        map[string]any
		legacyField string
	}{
		{"BC 集群", h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{"code": "no-parent-bc"}, "namespaceId"},
		{"大区", h.CreateRegion, "/admin/v2/regions", map[string]any{"code": "no-parent-r"}, "bcClusterId"},
		{"小区", h.CreateZone, "/admin/v2/zones", map[string]any{"code": "no-parent-z"}, "regionId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := invokeJSON(tc.handler, http.MethodPost, tc.path, "", tc.body)
			assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "parentId", tc.legacyField)
		})
	}
	// parentId 显式 0 同样按「缺有效父级」给出可读说明。
	code, body := invokeJSON(h.CreateBCCluster, http.MethodPost, "/admin/v2/bc-clusters", "", map[string]any{
		"parentId": 0, "code": "zero-parent-bc",
	})
	assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "parentId", "namespaceId")
}

// TestV2TopologyCreateZeroParentIDFallsBackToLegacyField 校验 parentId 的零值等同「未提供」：
// 「同时序列化新旧两个字段、新字段取零值」的客户端仍能建树成功——旧版本控制面根本不认 parentId
// （未知字段被忽略），把 parentId:0 判成「与旧字段冲突」或「父级为 0」都是对这类客户端的后向兼容回归。
func TestV2TopologyCreateZeroParentIDFallsBackToLegacyField(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	clusterID := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", map[string]any{
		"namespaceId": ns.ID, "parentId": 0, "code": "zero-bc",
	})
	var cluster model.BCCluster
	if err := db.First(&cluster, clusterID).Error; err != nil || cluster.NamespaceID != ns.ID {
		t.Fatalf("parentId:0 应回落到 namespaceId 建集群，实际 %+v err=%v", cluster, err)
	}
	regionID := createTopologyNode(t, h.CreateRegion, "/admin/v2/regions", map[string]any{
		"bcClusterId": clusterID, "parentId": 0, "code": "zero-r",
	})
	var region model.Region
	if err := db.First(&region, regionID).Error; err != nil || region.BCClusterID != clusterID {
		t.Fatalf("parentId:0 应回落到 bcClusterId 建大区，实际 %+v err=%v", region, err)
	}
	zoneID := createTopologyNode(t, h.CreateZone, "/admin/v2/zones", map[string]any{
		"regionId": regionID, "parentId": 0, "code": "zero-z",
	})
	var zone model.Zone
	if err := db.First(&zone, zoneID).Error; err != nil || zone.RegionID != regionID {
		t.Fatalf("parentId:0 应回落到 regionId 建小区，实际 %+v err=%v", zone, err)
	}

	// 零值豁免不放宽真正的冲突判定：parentId > 0 且与旧字段不一致仍拒绝。
	code, body := invokeJSON(h.CreateBCCluster, http.MethodPost, "/admin/v2/bc-clusters", "", map[string]any{
		"namespaceId": ns.ID + 1, "parentId": ns.ID, "code": "zero-conflict-bc",
	})
	assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "parentId", "namespaceId", "冲突")

	// parentId:0 且旧字段也缺省 → 仍是「缺父级」（零值不代表「父级 = 0」），文案照旧点名应传的字段。
	code, body = invokeJSON(h.CreateBCCluster, http.MethodPost, "/admin/v2/bc-clusters", "", map[string]any{
		"parentId": 0, "code": "zero-no-parent-bc",
	})
	assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "parentId", "namespaceId")
	var count int64
	if err := db.Model(&model.BCCluster{}).Where("code IN ?", []string{"zero-conflict-bc", "zero-no-parent-bc"}).
		Count(&count).Error; err != nil {
		t.Fatalf("统计集群失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒请求不得创建节点，实际 %d 条", count)
	}
}

// TestV2TopologyCreateNameCodeDisplayNameCombinations 校验卡点 2：
// 合法组合继续通过，name != code 的拒绝文案必须点明「name 是旧字段、展示名用 displayName」。
func TestV2TopologyCreateNameCodeDisplayNameCombinations(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	cases := []struct {
		name        string
		body        map[string]any
		wantCode    string
		wantDisplay string
	}{
		{"只传 code", map[string]any{"parentId": ns.ID, "code": "only-code"}, "only-code", "only-code"},
		{"code + displayName", map[string]any{"parentId": ns.ID, "code": "code-display", "displayName": "展示名"}, "code-display", "展示名"},
		{"name 等于 code（旧调用方）", map[string]any{"parentId": ns.ID, "name": "name-eq-code", "code": "name-eq-code"}, "name-eq-code", "name-eq-code"},
		{"只传 name（最旧调用方）", map[string]any{"parentId": ns.ID, "name": "only-name"}, "only-name", "only-name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := createTopologyNode(t, h.CreateBCCluster, "/admin/v2/bc-clusters", tc.body)
			var cluster model.BCCluster
			if err := db.First(&cluster, id).Error; err != nil {
				t.Fatalf("读取集群失败: %v", err)
			}
			if cluster.Code != tc.wantCode || cluster.Name != tc.wantDisplay {
				t.Fatalf("code / displayName 落库不符：期望 %q / %q，实际 %q / %q",
					tc.wantCode, tc.wantDisplay, cluster.Code, cluster.Name)
			}
		})
	}

	t.Run("name 与 code 不一致", func(t *testing.T) {
		code, body := invokeJSON(h.CreateBCCluster, http.MethodPost, "/admin/v2/bc-clusters", "", map[string]any{
			"parentId": ns.ID, "name": "接入验收集群", "code": "onb-bc1",
		})
		assertErrorCodeAndMessage(t, code, body, "AMBIGUOUS_IDENTIFIER", "name", "code", "displayName")
	})
}

// TestV2ServerAssignmentsSupportsLobbyClusterTarget 校验卡点 3（方案 A）：
// server-assignments 直接受理 target.kind=lobby_cluster，内部转发到单服大厅迁移入口；
// 原 placement-transfers 端点行为不变。
func TestV2ServerAssignmentsSupportsLobbyClusterTarget(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	first := registerAndApproveBackendForHandler(t, db, svc, token, 1, "onb-1")
	second := registerAndApproveBackendForHandler(t, db, svc, token, 2, "onb-2")
	third := registerAndApproveBackendForHandler(t, db, svc, token, 3, "onb-3")
	lobby := lobbyForHandlerTest(t, db, ns.ID)

	t.Run("单台", func(t *testing.T) {
		code, body := invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{first.ID},
			"target":    map[string]any{"kind": "lobby_cluster", "id": lobby.ID},
			"reason":    "接入验收迁入大厅",
		})
		if code != http.StatusAccepted || body["operationKey"] != "topology.lobby_member.move" {
			t.Fatalf("lobby_cluster 目标应 202 + 大厅迁移票据，实际 %d：%v", code, body)
		}
		tickets := decodeLobbyAssignmentTickets(t, body)
		if len(tickets) != 1 || tickets[0]["serverId"] != first.ServerID {
			t.Fatalf("票据应按请求顺序逐台给出，实际 %v", tickets)
		}
		runV2ApprovalForHandlerFixture(t, db, svc, service.ApprovalTicketView{
			ApprovalRequestID: body["approvalRequestId"].(string),
		})
		assertServerInLobbyCluster(t, db, first.ID, lobby.ID)
	})

	t.Run("批量逐台出票", func(t *testing.T) {
		code, body := invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{second.ID, third.ID},
			"target":    map[string]any{"kind": "lobby_cluster", "id": lobby.ID},
			"reason":    "接入验收批量迁入大厅",
		})
		if code != http.StatusAccepted {
			t.Fatalf("批量 lobby_cluster 应 202，实际 %d：%v", code, body)
		}
		tickets := decodeLobbyAssignmentTickets(t, body)
		if len(tickets) != 2 {
			t.Fatalf("两台应给出两张票据，实际 %v", tickets)
		}
		for i, want := range []model.Server{second, third} {
			if tickets[i]["serverId"] != want.ServerID {
				t.Fatalf("第 %d 张票据应对应 %s，实际 %v", i, want.ServerID, tickets[i])
			}
			requestID, _ := tickets[i]["approvalRequestId"].(string)
			runV2ApprovalForHandlerFixture(t, db, svc, service.ApprovalTicketView{ApprovalRequestID: requestID})
			assertServerInLobbyCluster(t, db, want.ID, lobby.ID)
		}
		var assignAudits int64
		if err := db.Model(&model.AuditLog{}).Where("action = ?", "lobby_cluster.member.assign").
			Count(&assignAudits).Error; err != nil {
			t.Fatalf("统计大厅首次迁入审计失败: %v", err)
		}
		if assignAudits != 3 {
			t.Fatalf("三台迁入应各记一次 lobby_cluster.member.assign 审计，实际 %d", assignAudits)
		}
	})

	t.Run("非法入参给出可读指引", func(t *testing.T) {
		code, body := invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{}, "target": map[string]any{"kind": "lobby_cluster", "id": lobby.ID}, "reason": "空批量",
		})
		assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "serverIds")

		code, body = invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{first.ID}, "target": map[string]any{"kind": "lobby_cluster", "id": lobby.ID},
			"isDefaultEntry": true, "reason": "误勾默认入口",
		})
		assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "isDefaultEntry")

		code, body = invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{first.ID}, "target": map[string]any{"kind": "lobby_cluster", "id": 0}, "reason": "缺目标",
		})
		assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "target.id")

		code, body = invokeJSON(h.AssignServers, http.MethodPost, "/admin/v2/server-assignments", "", map[string]any{
			"serverIds": []uint{999999}, "target": map[string]any{"kind": "lobby_cluster", "id": lobby.ID}, "reason": "不存在",
		})
		assertErrorCodeAndMessage(t, code, body, "INVALID_PARAM", "server")
	})
}

// TestV2ServerPlacementTransferUnchangedAfterLobbyAlias 校验原单服迁移端点未因卡点 3 的转发而回归。
func TestV2ServerPlacementTransferUnchangedAfterLobbyAlias(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := registerAndApproveBackendForHandler(t, db, svc, token, 1, "onb-transfer")
	lobby := lobbyForHandlerTest(t, db, ns.ID)

	code, body := invokeJSON(h.TransferServerPlacement, http.MethodPost, "/admin/v2/server-placement-transfers", "", map[string]any{
		"serverId": server.ServerID, "target": map[string]any{"kind": "lobby_cluster", "id": lobby.ID}, "reason": "接入验收单服迁移",
	})
	if code != http.StatusAccepted || body["operationKey"] != "topology.lobby_member.move" {
		t.Fatalf("原端点应保持 202 + 同形票据，实际 %d：%v", code, body)
	}
	runV2ApprovalForHandlerFixture(t, db, svc, service.ApprovalTicketView{
		ApprovalRequestID: body["approvalRequestId"].(string),
	})
	assertServerInLobbyCluster(t, db, server.ID, lobby.ID)
}

func registerAndApproveBackendForHandler(t *testing.T, db *gorm.DB, svc *service.V2ControlPlaneService, token string, seq int, serverID string) model.Server {
	t.Helper()
	identityID := handlerTestIdentityID(seq)
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, ServerID: serverID, Kind: model.ServerKindBackend, BootID: "boot-" + serverID,
	}); err != nil {
		t.Fatalf("注册 backend %s 失败: %v", serverID, err)
	}
	approveV2IdentityForHandlerFixture(t, db, svc, identityID, serverID)
	var server model.Server
	if err := db.Where("server_id = ?", serverID).First(&server).Error; err != nil {
		t.Fatalf("读取 backend %s 失败: %v", serverID, err)
	}
	return server
}

// handlerTestIdentityID 由序号生成确定且合法的 UUID（v4 形状），避免同用例内多个身份撞号。
func handlerTestIdentityID(seq int) string {
	return fmt.Sprintf("%08d-1111-4111-8111-%012d", seq, seq)
}

func lobbyForHandlerTest(t *testing.T, db *gorm.DB, namespaceID uint) model.LobbyCluster {
	t.Helper()
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", namespaceID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	return lobby
}

func decodeLobbyAssignmentTickets(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["tickets"].([]any)
	if !ok {
		t.Fatalf("响应应带 tickets 列表，实际 %v", body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		ticket, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("票据项格式不符，实际 %v", item)
		}
		out = append(out, ticket)
	}
	return out
}

func assertServerInLobbyCluster(t *testing.T, db *gorm.DB, rowID, lobbyID uint) {
	t.Helper()
	var server model.Server
	if err := db.First(&server, rowID).Error; err != nil {
		t.Fatalf("读取 server %d 失败: %v", rowID, err)
	}
	if server.LobbyClusterID == nil || *server.LobbyClusterID != lobbyID {
		t.Fatalf("server %d 应落入大厅 %d，实际 %+v", rowID, lobbyID, server)
	}
	if server.ZoneID != nil || server.BCClusterID != nil || server.IsDefaultEntry {
		t.Fatalf("大厅归属须与小区 / 集群 / 默认入口互斥，实际 %+v", server)
	}
}

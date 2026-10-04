package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件覆盖卡点 3 的服务层契约：server-assignments 的 lobby_cluster 目标逐台转发到
// 既有的单服迁移入口（RequestTransferServerPlacement），不复制归属迁移逻辑。

func assertServiceInvalidParam(t *testing.T, err error, wants ...string) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.ErrInvalidParam.Code || ae.Status != apperr.ErrInvalidParam.Status {
		t.Fatalf("应返回 %d %s，实际 %v", apperr.ErrInvalidParam.Status, apperr.ErrInvalidParam.Code, err)
	}
	for _, want := range wants {
		if !strings.Contains(ae.Message, want) {
			t.Fatalf("错误文案应包含 %q 以便直接修正，实际 %q", want, ae.Message)
		}
	}
}

func lobbyMemberAssignmentFixture(t *testing.T) (*V2ControlPlaneService, *model.Server, *model.Server, model.LobbyCluster) {
	t.Helper()
	approval, db, seeded := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	lobby := lobbyForNamespace(t, db, seeded.NamespaceID)
	second := model.Server{NamespaceID: seeded.NamespaceID, ServerID: "onb-2", DisplayName: "接入验收二服", Kind: model.ServerKindBackend}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("写入第二台 server 失败: %v", err)
	}
	return v2, &seeded, &second, lobby
}

// TestV2RequestLobbyMemberAssignmentsValidatesInputBeforeApproval 校验可读错误且不产生副作用。
func TestV2RequestLobbyMemberAssignmentsValidatesInputBeforeApproval(t *testing.T) {
	v2, server, _, lobby := lobbyMemberAssignmentFixture(t)
	principal := auth.HumanPrincipal("alice")

	_, err := v2.RequestLobbyMemberAssignments(AssignServersParams{
		TargetID: lobby.ID, Reason: "空批量", Operator: "alice",
	}, principal, "")
	assertServiceInvalidParam(t, err, "serverIds")

	_, err = v2.RequestLobbyMemberAssignments(AssignServersParams{
		ServerIDs: []uint{server.ID}, TargetID: 0, Reason: "缺目标", Operator: "alice",
	}, principal, "")
	assertServiceInvalidParam(t, err, "target.id")

	_, err = v2.RequestLobbyMemberAssignments(AssignServersParams{
		ServerIDs: []uint{server.ID}, TargetID: lobby.ID, IsDefaultEntry: true, Reason: "误勾默认入口", Operator: "alice",
	}, principal, "")
	assertServiceInvalidParam(t, err, "isDefaultEntry")

	_, err = v2.RequestLobbyMemberAssignments(AssignServersParams{
		ServerIDs: []uint{987654}, TargetID: lobby.ID, Reason: "不存在的 id", Operator: "alice",
	}, principal, "")
	assertServiceInvalidParam(t, err, "server")

	var count int64
	if err := v2.db.Model(&model.ApprovalRequest{}).Count(&count).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("入参不合法的请求不得创建审批，实际 %d 条", count)
	}
}

// TestV2RequestLobbyMemberAssignmentsIssuesOneTicketPerServer 校验逐台出票、按请求顺序、重复提交幂等。
func TestV2RequestLobbyMemberAssignmentsIssuesOneTicketPerServer(t *testing.T) {
	v2, first, second, lobby := lobbyMemberAssignmentFixture(t)
	principal := auth.HumanPrincipal("alice")
	params := AssignServersParams{
		ServerIDs: []uint{second.ID, first.ID}, TargetID: lobby.ID,
		Reason: "接入验收迁入大厅", Operator: "alice",
	}

	tickets, err := v2.RequestLobbyMemberAssignments(params, principal, "onb-lobby-batch")
	if err != nil {
		t.Fatalf("lobby_cluster 转发应成功: %v", err)
	}
	if len(tickets) != 2 {
		t.Fatalf("两台应各出一张票据，实际 %d 张", len(tickets))
	}
	for i, want := range []string{second.ServerID, first.ServerID} {
		if tickets[i].ServerID != want {
			t.Fatalf("第 %d 张票据应对应 %s，实际 %s", i, want, tickets[i].ServerID)
		}
		if tickets[i].OperationKey != authz.OperationTopologyLobbyMemberMove {
			t.Fatalf("票据应复用单服大厅迁移操作，实际 %s", tickets[i].OperationKey)
		}
		if tickets[i].Status != model.ApprovalStatusPending {
			t.Fatalf("票据应为待审批，实际 %s", tickets[i].Status)
		}
	}

	// 同一批量 + 同一 Idempotency-Key 重放：派生键稳定，返回同一批申请而不新建。
	replayed, err := v2.RequestLobbyMemberAssignments(params, principal, "onb-lobby-batch")
	if err != nil {
		t.Fatalf("重放应幂等成功: %v", err)
	}
	if len(replayed) != len(tickets) {
		t.Fatalf("重放票据数应一致，实际 %d", len(replayed))
	}
	for i := range tickets {
		if replayed[i].ApprovalRequestID != tickets[i].ApprovalRequestID {
			t.Fatalf("重放应返回同一申请（%s ≠ %s）", replayed[i].ApprovalRequestID, tickets[i].ApprovalRequestID)
		}
	}
}

// TestV2RequestAssignServersGuidesLobbyTarget 校验非 HTTP 调用方（MCP / 内部）拿到的不再是泛化参数错误。
func TestV2RequestAssignServersGuidesLobbyTarget(t *testing.T) {
	v2, server, _, lobby := lobbyMemberAssignmentFixture(t)
	_, err := v2.RequestAssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID}, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "大厅归属", Operator: "alice",
	}, auth.HumanPrincipal("alice"), "")
	assertServiceInvalidParam(t, err, "server-placement-transfers")
}

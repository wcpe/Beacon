package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// seedConflictIdentity 直接落一个 conflict 态身份（含 namespace 与 conflict_peers JSON），供 HTTP 层测试冲突详情 / 处置端点。
func seedConflictIdentity(t *testing.T, db *gorm.DB) string {
	t.Helper()
	ns := &model.Namespace{Code: "prod", Name: "prod"}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("落 namespace 失败: %v", err)
	}
	identityID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	peers, _ := json.Marshal([]map[string]any{
		{"bootId": "boot-A", "lastAddr": "10.0.0.1:25565", "lastSeenAt": time.Now().UTC().Format(time.RFC3339)},
		{"bootId": "boot-B", "lastAddr": "10.0.0.2:25565", "lastSeenAt": time.Now().UTC().Format(time.RFC3339)},
	})
	ident := &model.AgentIdentity{
		IdentityID: identityID, NamespaceID: ns.ID, ServerID: "lobby-1", Kind: model.ServerKindBackend,
		Status: model.AgentIdentityStatusConflict, BootID: "boot-B",
		ConflictReason: "duplicate-boot-id", ConflictPeers: string(peers), StatusChangedAt: time.Now().UTC(),
	}
	if err := db.Create(ident).Error; err != nil {
		t.Fatalf("落 conflict 身份失败: %v", err)
	}
	return identityID
}

// TestV2IdentityDetailReturnsConflictPeers 验证冲突态详情端点回显 conflictPeers（FR-177，spec §5.2）。
func TestV2IdentityDetailReturnsConflictPeers(t *testing.T) {
	db, _, h := newV2HandlerTestService(t)
	id := seedConflictIdentity(t, db)

	code, parsed := invokeJSONWithParam(h.GetAgentIdentity, http.MethodGet,
		"/admin/v2/agent-identities/"+id, "", nil, "identityId", id)
	if code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d：%v", code, parsed)
	}
	peers, ok := parsed["conflictPeers"].([]any)
	if !ok || len(peers) != 2 {
		t.Fatalf("冲突详情应回显 2 个 conflictPeers，实际 %v", parsed["conflictPeers"])
	}
}

// TestV2ResolveConflictHTTP 验证冲突处置端点：keepBootId 不在双方 → 400、有效 → 200 active、非 conflict → 409。
func TestV2ResolveConflictHTTP(t *testing.T) {
	db, _, h := newV2HandlerTestService(t)
	id := seedConflictIdentity(t, db)

	// keepBootId 不在冲突双方 → 400。
	code, _ := invokeJSONWithParam(h.ResolveAgentIdentityConflict, http.MethodPost,
		"/admin/v2/agent-identities/"+id+"/resolve-conflict", "",
		map[string]any{"keepBootId": "boot-X", "reason": "保留"}, "identityId", id)
	if code != http.StatusBadRequest {
		t.Fatalf("keepBootId 不在双方应 400，实际 %d", code)
	}

	// 有效处置 → 200 active。
	code, parsed := invokeJSONWithParam(h.ResolveAgentIdentityConflict, http.MethodPost,
		"/admin/v2/agent-identities/"+id+"/resolve-conflict", "",
		map[string]any{"keepBootId": "boot-A", "reason": "保留原主实例"}, "identityId", id)
	if code != http.StatusOK || parsed["status"] != model.AgentIdentityStatusActive {
		t.Fatalf("有效处置应 200 active，实际 %d：%v", code, parsed)
	}

	// 已恢复 active，再次处置 → 409。
	code, _ = invokeJSONWithParam(h.ResolveAgentIdentityConflict, http.MethodPost,
		"/admin/v2/agent-identities/"+id+"/resolve-conflict", "",
		map[string]any{"keepBootId": "boot-A", "reason": "重复处置"}, "identityId", id)
	if code != http.StatusConflict {
		t.Fatalf("非 conflict 处置应 409，实际 %d", code)
	}
}

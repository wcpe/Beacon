package alert

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestGradeAlertMatrix 穷举判定矩阵（FR-231 §3）：健康级别 × 角色 → 告警级别。
func TestGradeAlertMatrix(t *testing.T) {
	cases := []struct {
		health string
		role   string
		want   string
	}{
		{"offline", RoleProxy, model.AlertLevelCritical},
		{"offline", RoleLobby, model.AlertLevelCritical},
		{"offline", RoleBackend, model.AlertLevelWarning},
		{"lost", RoleProxy, model.AlertLevelCritical},
		{"lost", RoleLobby, model.AlertLevelCritical},
		{"lost", RoleBackend, model.AlertLevelWarning},
		{"degraded", RoleProxy, model.AlertLevelWarning},
		{"degraded", RoleLobby, model.AlertLevelWarning},
		{"degraded", RoleBackend, model.AlertLevelInfo},
		// 未知健康级别 → info 兜底
		{"online", RoleProxy, model.AlertLevelInfo},
		{"", RoleBackend, model.AlertLevelInfo},
		// 未知 / 空角色 → 按 backend 规则安全降级
		{"offline", "", model.AlertLevelWarning},
		{"offline", "unknown", model.AlertLevelWarning},
		{"degraded", "", model.AlertLevelInfo},
	}
	for _, c := range cases {
		if got := GradeAlert(c.health, c.role, nil); got != c.want {
			t.Fatalf("GradeAlert(%q,%q) 应 %q，实际 %q", c.health, c.role, c.want, got)
		}
	}
}

// TestGradeAlertOverrideWins 人工覆盖非空时以人工值为准，矩阵不参与。
func TestGradeAlertOverrideWins(t *testing.T) {
	ov := model.AlertLevelInfo
	if got := GradeAlert("offline", RoleProxy, &ov); got != model.AlertLevelInfo {
		t.Fatalf("覆盖应优先，实际 %q", got)
	}
	empty := ""
	if got := GradeAlert("offline", RoleProxy, &empty); got != model.AlertLevelCritical {
		t.Fatalf("空覆盖应退回矩阵，实际 %q", got)
	}
}

// TestRoleOf 角色解析：proxy / lobby（lobby_cluster_id != 0）/ backend。
func TestRoleOf(t *testing.T) {
	if got := RoleOf(model.ServerKindProxy, 0); got != RoleProxy {
		t.Fatalf("proxy 应 proxy，实际 %q", got)
	}
	if got := RoleOf(model.ServerKindBackend, 7); got != RoleLobby {
		t.Fatalf("有大厅归属应 lobby，实际 %q", got)
	}
	if got := RoleOf(model.ServerKindBackend, 0); got != RoleBackend {
		t.Fatalf("无大厅归属应 backend，实际 %q", got)
	}
}

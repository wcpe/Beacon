package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/runtime"
)

// TestAssignRejectsBungee 验证 zone 指派对 role=bungee 的 BC 代理实例拒绝（FR-8/FR-35 纵深防御）。
// bungee 守卫在任何 DB 访问前返回，故无需真实库即可单测（repo/db 传 nil 不会被触达）。
func TestAssignRejectsBungee(t *testing.T) {
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: roleBungee, Address: "10.0.0.9:25577",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册 bc 实例失败: %v", err)
	}
	// db/repo 传 nil：bungee 守卫先于事务与仓库访问返回，不会触达。
	svc := NewZoneService(nil, nil, nil, reg)

	_, err := svc.Assign("prod", "bc-1", "area1", "zoneA", "admin", "", "10.0.0.1")
	if !errors.Is(err, apperr.ErrZoneNotAssignableToBC) {
		t.Fatalf("对 bungee 实例应返回 ErrZoneNotAssignableToBC，实际 %v", err)
	}
}

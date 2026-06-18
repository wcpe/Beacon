package runtime

import (
	"context"
	"testing"
	"time"

	"beacon/internal/runtime/alert"
)

// capturingAlerter 收集分发到的告警，供断言触发判定。
type capturingAlerter struct {
	got []alert.Alert
}

func (c *capturingAlerter) Name() string { return "capturing" }

func (c *capturingAlerter) Notify(_ context.Context, a alert.Alert) error {
	c.got = append(c.got, a)
	return nil
}

// TestScannerAlertsOnAbnormalTransitions 进入 degraded/lost/offline 触发告警，恢复 online 不触发（FR-28）。
func TestScannerAlertsOnAbnormalTransitions(t *testing.T) {
	cap := &capturingAlerter{}
	s := NewHealthScanner(NewRegistry(), degradedAfter, ttl, offlineGrace, time.Second, alert.NewDispatcher(cap))

	changed := []*Instance{
		{Namespace: "prod", ServerID: "a", Address: "1.1.1.1:1", PrevStatus: StatusOnline, Status: StatusDegraded},
		{Namespace: "prod", ServerID: "b", Address: "1.1.1.2:1", PrevStatus: StatusDegraded, Status: StatusLost},
		{Namespace: "prod", ServerID: "c", Address: "1.1.1.3:1", PrevStatus: StatusLost, Status: StatusOffline},
		{Namespace: "prod", ServerID: "d", Address: "1.1.1.4:1", PrevStatus: StatusLost, Status: StatusOnline}, // 恢复，不告警
	}
	s.dispatchAlerts(context.Background(), changed)

	if len(cap.got) != 3 {
		t.Fatalf("3 个异常转移应各告警 1 次（恢复不告警），实际 %d", len(cap.got))
	}
	for _, a := range cap.got {
		if a.Status == StatusOnline {
			t.Fatalf("恢复 online 不应告警，却收到 %+v", a)
		}
	}
}

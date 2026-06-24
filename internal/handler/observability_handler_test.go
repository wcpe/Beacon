package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
)

// dbStatsStub 是 dbStatsProvider 的测试替身（service 包内接口为非导出，经构造函数注入）。
type dbStatsStub struct{ stats sql.DBStats }

func (d dbStatsStub) Stats() sql.DBStats { return d.stats }

// waiterStub 是 waiterCounter 的测试替身：返回预置挂起数。
type waiterStub int

func (w waiterStub) WaiterCount() int { return int(w) }

// cmdCounterStub 是 commandCounter 的测试替身：返回预置计数。
type cmdCounterStub map[string]int

func (c cmdCounterStub) CountByStatus() (map[string]int, error) { return c, nil }

// TestObservabilityHandler 验证端点返回 200 且四组指标字段就位。
func TestObservabilityHandler(t *testing.T) {
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{Namespace: "prod", ServerID: "lobby-1", Address: "10.0.0.1:25565"},
		30*time.Second, time.Now()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	svc := service.NewObservabilityService(
		dbStatsStub{stats: sql.DBStats{MaxOpenConnections: 10, OpenConnections: 2, InUse: 1, Idle: 1, WaitCount: 3, WaitDuration: 100 * time.Millisecond}},
		reg,
		waiterStub(1), waiterStub(0), waiterStub(0), waiterStub(2),
		cmdCounterStub{"pending": 4},
	)
	h := NewObservabilityHandler(svc)

	rec := httptest.NewRecorder()
	h.Observability(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/system/observability", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body observabilityView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if body.DBPool.MaxOpenConnections != 10 || body.DBPool.OpenConnections != 2 || body.DBPool.WaitDurationMs != 100 {
		t.Fatalf("连接池字段不一致: %+v", body.DBPool)
	}
	if body.Longpoll.Total != 3 || body.Longpoll.Config != 1 || body.Longpoll.Command != 2 {
		t.Fatalf("长轮询字段不一致: %+v", body.Longpoll)
	}
	if body.RegistryTotal != 1 || body.RegistryByStatus["online"] != 1 {
		t.Fatalf("注册表规模不一致: total=%d byStatus=%+v", body.RegistryTotal, body.RegistryByStatus)
	}
	if body.CommandByStatus["pending"] != 4 {
		t.Fatalf("命令队列计数不一致: %+v", body.CommandByStatus)
	}
}

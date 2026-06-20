package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"beacon/internal/runtime"
	"beacon/internal/service"
)

// pingFunc 是 dbPinger 的函数式测试替身（service 包内 dbPinger 为非导出接口，handler 测试经其构造函数注入）。
type pingFunc func() error

func (f pingFunc) Ping() error { return f() }

// cpuStub 是 cpuSampler 的测试替身：按预置值返回进程 CPU 占比与可用性。
type cpuStub struct {
	percent   float64
	available bool
}

func (c cpuStub) Percent() (float64, bool) { return c.percent, c.available }

// TestSystemStatusHandlerConnected 验证端点返回 200 且 DB 连通、字段就位。
func TestSystemStatusHandlerConnected(t *testing.T) {
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{Namespace: "prod", ServerID: "lobby-1", Address: "10.0.0.1:25565"},
		30*time.Second, time.Now()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	start := time.Now().Add(-2 * time.Minute)
	svc := service.NewSystemService("v0.5.0", start, pingFunc(func() error { return nil }), reg, true,
		cpuStub{percent: 12.3, available: true})
	h := NewSystemHandler(svc)

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/system/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body systemStatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if body.Version != "v0.5.0" {
		t.Fatalf("版本应为 v0.5.0，实际 %q", body.Version)
	}
	if !body.DB.Connected {
		t.Fatalf("DB 应连通，实际 %+v", body.DB)
	}
	if body.OnlineInstances != 1 {
		t.Fatalf("在线实例数应为 1，实际 %d", body.OnlineInstances)
	}
	if body.UptimeSeconds < 100 {
		t.Fatalf("运行时长应≥100s，实际 %d", body.UptimeSeconds)
	}
	if !body.SamplerEnabled {
		t.Fatal("采样器应标记为启用")
	}
	if !body.CPUAvailable {
		t.Fatal("注入可用采样器时 CPUAvailable 应为 true")
	}
	if body.CPUPercent != 12.3 {
		t.Fatalf("CPUPercent 应透传采样值 12.3，实际 %v", body.CPUPercent)
	}
	if body.Runtime.Goroutines <= 0 {
		t.Fatalf("goroutine 数应为正，实际 %d", body.Runtime.Goroutines)
	}
}

// TestSystemStatusHandlerDBDown 验证 DB 断开时端点仍返回 200，但 db.connected=false 并带错误说明。
func TestSystemStatusHandlerDBDown(t *testing.T) {
	svc := service.NewSystemService("v1", time.Now(), pingFunc(func() error { return errors.New("库已停") }),
		runtime.NewRegistry(), false, cpuStub{available: false})
	h := NewSystemHandler(svc)

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/system/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("DB 断开时端点仍应返回 200（反映状态而非报错），实际 %d", rec.Code)
	}
	var body systemStatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if body.DB.Connected {
		t.Fatal("DB 应反映为断开")
	}
	if body.DB.Error == "" {
		t.Fatal("断开时应带错误说明")
	}
}

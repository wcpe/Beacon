package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/model"
)

// recordingAuditCreator 是审计落库的内存假实现，供中间件单测断言补记内容，不连库。
type recordingAuditCreator struct {
	mu      sync.Mutex
	entries []model.AuditLog
	failErr error // 非空则模拟落库失败
}

func (r *recordingAuditCreator) Create(entry *model.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failErr != nil {
		return r.failErr
	}
	r.entries = append(r.entries, *entry)
	return nil
}

func (r *recordingAuditCreator) all() []model.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.AuditLog, len(r.entries))
	copy(out, r.entries)
	return out
}

// newAuditMiddlewareRouter 装配一个最小 chi 路由：注入固定 operator + 兜底审计中间件 + 若干合成端点。
// 合成端点不依赖真实业务，仅用于驱动中间件的补记 / 跳过 / 状态判定路径。
func newAuditMiddlewareRouter(creator auditCreator) http.Handler {
	r := chi.NewRouter()
	// 模拟鉴权中间件：把固定 operator 注入 context（中间件据此取操作者）。
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithOperator(req.Context(), "tester")
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(auditWriteMiddleware(creator))

	// 未被专项审计覆盖的合成写端点（应被兜底补记）。
	r.Post("/admin/v1/widgets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	r.Put("/admin/v1/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Delete("/admin/v1/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// 失败响应（非 2xx）的合成写端点。
	r.Post("/admin/v1/widgets/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	// 读端点（不应被记）。
	r.Get("/admin/v1/widgets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// 已在覆盖集合内的端点（专项审计已记，中间件不应重复补记）。
	r.Post("/admin/v1/configs", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })

	return r
}

// doReq 对测试路由发起一次请求并返回响应状态码。
func doReq(t *testing.T, h http.Handler, method, target string, body string) int {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestAuditMiddlewareRecordsUncoveredWrite 守护 FR-72 核心：未被专项审计覆盖的写端点经中间件补记一条，
// operator / action / targetType / targetRef / result / clientIp 正确。
func TestAuditMiddlewareRecordsUncoveredWrite(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodPut, "/admin/v1/widgets/w-7", `{"k":"v"}`); code != http.StatusOK {
		t.Fatalf("PUT widgets 应 200，实际 %d", code)
	}

	entries := creator.all()
	if len(entries) != 1 {
		t.Fatalf("未覆盖写端点应兜底补记 1 条，实际 %d", len(entries))
	}
	got := entries[0]
	if got.Operator != "tester" {
		t.Fatalf("审计 operator 应为 tester，实际 %q", got.Operator)
	}
	if got.Action != "widget.update" {
		t.Fatalf("审计 action 应为 widget.update，实际 %q", got.Action)
	}
	if got.TargetType != "widget" {
		t.Fatalf("审计 targetType 应为 widget，实际 %q", got.TargetType)
	}
	if got.TargetRef != "w-7" {
		t.Fatalf("审计 targetRef 应为路径参数 w-7，实际 %q", got.TargetRef)
	}
	if got.Result != model.ResultOK {
		t.Fatalf("审计 result 应为 ok，实际 %q", got.Result)
	}
	if got.ClientIP != "203.0.113.9" {
		t.Fatalf("审计 clientIp 应落库，实际 %q", got.ClientIP)
	}
}

// TestAuditMiddlewareActionDerivation 校验 action / target 推导：POST→create、DELETE→delete、无路径参数取资源词。
func TestAuditMiddlewareActionDerivation(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodPost, "/admin/v1/widgets", ""); code != http.StatusCreated {
		t.Fatalf("POST widgets 应 201，实际 %d", code)
	}
	if code := doReq(t, h, http.MethodDelete, "/admin/v1/widgets/w-9", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE widgets 应 204，实际 %d", code)
	}

	entries := creator.all()
	if len(entries) != 2 {
		t.Fatalf("应补记 2 条，实际 %d", len(entries))
	}
	// POST 无路径参数：action=widget.create、targetRef 退回资源词 widget。
	if entries[0].Action != "widget.create" || entries[0].TargetRef != "widget" {
		t.Fatalf("POST 推导错误：action=%q targetRef=%q", entries[0].Action, entries[0].TargetRef)
	}
	// DELETE 带路径参数：action=widget.delete、targetRef=w-9。
	if entries[1].Action != "widget.delete" || entries[1].TargetRef != "w-9" {
		t.Fatalf("DELETE 推导错误：action=%q targetRef=%q", entries[1].Action, entries[1].TargetRef)
	}
}

// TestAuditMiddlewareSkipsCovered 守护边界：已在覆盖集合内的端点不被中间件重复补记（避免与专项审计双记）。
func TestAuditMiddlewareSkipsCovered(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodPost, "/admin/v1/configs", `{"k":"v"}`); code != http.StatusCreated {
		t.Fatalf("POST configs 应 201，实际 %d", code)
	}
	if n := len(creator.all()); n != 0 {
		t.Fatalf("已覆盖端点不应兜底补记，实际 %d 条", n)
	}
}

// TestAuditMiddlewareIgnoresReads 守护：GET 等读方法不产生兜底审计。
func TestAuditMiddlewareIgnoresReads(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodGet, "/admin/v1/widgets", ""); code != http.StatusOK {
		t.Fatalf("GET widgets 应 200，实际 %d", code)
	}
	if n := len(creator.all()); n != 0 {
		t.Fatalf("读方法不应产生审计，实际 %d 条", n)
	}
}

// TestAuditMiddlewareFailResult 校验：失败响应（非 2xx）兜底审计 result=fail。
func TestAuditMiddlewareFailResult(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodPost, "/admin/v1/widgets/fail", ""); code != http.StatusBadRequest {
		t.Fatalf("POST widgets/fail 应 400，实际 %d", code)
	}
	entries := creator.all()
	if len(entries) != 1 {
		t.Fatalf("失败写端点仍应兜底补记 1 条，实际 %d", len(entries))
	}
	if entries[0].Result != model.ResultFail {
		t.Fatalf("失败响应 result 应为 fail，实际 %q", entries[0].Result)
	}
}

// TestAuditMiddlewareNoBodyInDetail 守护安全底线：兜底审计 detail 严禁含请求体内容。
func TestAuditMiddlewareNoBodyInDetail(t *testing.T) {
	creator := &recordingAuditCreator{}
	h := newAuditMiddlewareRouter(creator)

	const secret = "super-secret-token-value"
	if code := doReq(t, h, http.MethodPut, "/admin/v1/widgets/w-1", `{"password":"`+secret+`"}`); code != http.StatusOK {
		t.Fatalf("PUT widgets 应 200，实际 %d", code)
	}
	entries := creator.all()
	if len(entries) != 1 {
		t.Fatalf("应补记 1 条，实际 %d", len(entries))
	}
	if strings.Contains(entries[0].Detail, secret) {
		t.Fatalf("兜底审计 detail 不得含请求体内容，实际 %q", entries[0].Detail)
	}
}

// TestAuditMiddlewareCreateFailureNotBlocking 守护旁路语义：审计落库失败不得阻断主响应。
func TestAuditMiddlewareCreateFailureNotBlocking(t *testing.T) {
	creator := &recordingAuditCreator{failErr: errFakeAudit}
	h := newAuditMiddlewareRouter(creator)

	if code := doReq(t, h, http.MethodPost, "/admin/v1/widgets", ""); code != http.StatusCreated {
		t.Fatalf("审计落库失败时主响应仍应 201，实际 %d", code)
	}
}

// errFakeAudit 是测试用的假落库错误。
var errFakeAudit = errFake("模拟审计落库失败")

// errFake 是无外部依赖的简单错误类型。
type errFake string

func (e errFake) Error() string { return string(e) }

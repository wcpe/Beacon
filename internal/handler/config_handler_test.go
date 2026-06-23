package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/merge"
)

// reqWithIDParam 构造带 chi 路径参数 {id} 的请求，供 parseID 单测。
func reqWithIDParam(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// TestParseID 验证 {id} 解析：合法返回数值，空 / 非数字 / 小数 / 负数 / 越界均映射为 ErrInvalidParam。
func TestParseID(t *testing.T) {
	t.Run("合法", func(t *testing.T) {
		id, err := parseID(reqWithIDParam("123"))
		if err != nil || id != 123 {
			t.Fatalf("应解析为 123，实际 id=%d err=%v", id, err)
		}
	})
	t.Run("零合法", func(t *testing.T) {
		id, err := parseID(reqWithIDParam("0"))
		if err != nil || id != 0 {
			t.Fatalf("0 应合法，实际 id=%d err=%v", id, err)
		}
	})
	for _, bad := range []string{"", "abc", "-1", "1.5", "99999999999999999999999999"} {
		t.Run("非法_"+bad, func(t *testing.T) {
			if _, err := parseID(reqWithIDParam(bad)); !errors.Is(err, apperr.ErrInvalidParam) {
				t.Fatalf("输入 %q 应返回 ErrInvalidParam，实际 %v", bad, err)
			}
		})
	}
}

// TestToProvViewsNilPathSerializesEmptyArray 复现并守护文件树预览整页白屏（FR-45 真机暴露）：
// 整文件来源（wholeFile）的 KeyProvenance.Path 为 nil，旧实现直传致 JSON 序列化为 "path":null，
// 前端 src.path.length 抛 TypeError 整页白屏。修复后须归一为 []（契约声明 path 为 string[]）。
func TestToProvViewsNilPathSerializesEmptyArray(t *testing.T) {
	views := toProvViews([]merge.KeyProvenance{{Path: nil, Scope: "zone"}})
	if len(views) != 1 {
		t.Fatalf("应有 1 条来源视图，实际 %d", len(views))
	}
	if views[0].Path == nil {
		t.Fatalf("整文件来源的 nil path 应归一为非 nil 空数组，实际仍为 nil")
	}
	data, err := json.Marshal(views[0])
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if got := string(data); !strings.Contains(got, `"path":[]`) {
		t.Fatalf("nil path 应序列化为 [] 而非 null，实际：%s", got)
	}
}

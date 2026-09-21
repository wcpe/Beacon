package agentauth

import (
	"context"
	"testing"
)

// TestTrustedInternalMarker 锁定 FR-222 的调用方类型标记语义：
// 未标记即 false（默认拒绝），显式标记后才为 true——注册分支据此判定，不依赖请求体。
func TestTrustedInternalMarker(t *testing.T) {
	if IsTrustedInternal(context.Background()) {
		t.Fatal("默认 context 不得被视为受信内部调用方")
	}
	marked := WithTrustedInternal(context.Background())
	if !IsTrustedInternal(marked) {
		t.Fatal("显式标记后应被识别为受信内部调用方")
	}
	// 标记只在本 context 生效，不污染派生前后的无关 context。
	if IsTrustedInternal(context.Background()) {
		t.Fatal("标记不得跨 context 泄漏")
	}
}

// TestIdentityAndTrustedMarkerCoexist 两个 context 值互不干扰（v2 身份与受信标记可同时存在）。
func TestIdentityAndTrustedMarkerCoexist(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{IdentityID: "id-1", ServerID: "srv-1"})
	ctx = WithTrustedInternal(ctx)
	if !IsTrustedInternal(ctx) {
		t.Fatal("受信标记应保留")
	}
	id, ok := FromContext(ctx)
	if !ok || id.IdentityID != "id-1" || id.ServerID != "srv-1" {
		t.Fatalf("v2 身份应保留，实际 %+v ok=%v", id, ok)
	}
}

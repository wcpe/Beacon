package auth

import (
	"context"
	"testing"
)

// TestOperatorRoundTrip 操作者身份写入 context 后可原样取回。
func TestOperatorRoundTrip(t *testing.T) {
	ctx := WithOperator(context.Background(), "alice")
	if got := Operator(ctx); got != "alice" {
		t.Fatalf("应取回 alice，实际 %q", got)
	}
}

// TestOperatorMissing 无操作者时取空串。
func TestOperatorMissing(t *testing.T) {
	if got := Operator(context.Background()); got != "" {
		t.Fatalf("无操作者应取空串，实际 %q", got)
	}
}

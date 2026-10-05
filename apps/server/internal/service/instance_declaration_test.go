package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// newDeclarationTestService 构造只依赖内存注册表的实例服务（db / 仓库传 nil）：
// 声明刷新的真源是内存注册表，校验与部分刷新语义无需 DB；nil auditRepo 同时钉住
// 「声明不写审计」这条口径——一旦 Declare 试图写审计会直接 panic 暴露。
func newDeclarationTestService(t *testing.T) (*InstanceService, *runtime.Registry) {
	t.Helper()
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", Address: "10.0.0.1:25565",
		Capacity: 200, Metadata: map[string]string{"mode": "survival"},
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	return NewInstanceService(nil, reg, nil, nil, nil, 10*time.Second, 30*time.Second), reg
}

// TestDeclareAppliesAndReturnsEffectiveValues 验证声明刷新成功时回带**生效后**的值自证（含只刷新单项的部分刷新）。
func TestDeclareAppliesAndReturnsEffectiveValues(t *testing.T) {
	svc, reg := newDeclarationTestService(t)

	capacity := 350
	res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Capacity: &capacity})
	if err != nil {
		t.Fatalf("声明应成功: %v", err)
	}
	if res.Capacity != 350 || res.Labels["mode"] != "survival" {
		t.Fatalf("只给 capacity 时应回带新容量 + 未变标签，实际 %+v", res)
	}

	res, err = svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Labels: map[string]string{"mode": "creative"}})
	if err != nil {
		t.Fatalf("声明应成功: %v", err)
	}
	if res.Capacity != 350 || res.Labels["mode"] != "creative" {
		t.Fatalf("只给 labels 时应回带未变容量 + 新标签，实际 %+v", res)
	}
	if got := reg.Get("prod", "lobby-1"); got.Capacity != 350 || got.Metadata["mode"] != "creative" {
		t.Fatalf("内存注册表应已生效，实际 capacity=%d labels=%v", got.Capacity, got.Metadata)
	}
}

// TestDeclareZeroCapacityApplies 验证 capacity=0 生效并回带 0（与「缺键不刷新」区分）。
func TestDeclareZeroCapacityApplies(t *testing.T) {
	svc, _ := newDeclarationTestService(t)
	zero := 0
	res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Capacity: &zero})
	if err != nil {
		t.Fatalf("capacity=0 应被接受: %v", err)
	}
	if res.Capacity != 0 {
		t.Fatalf("capacity=0 应生效并回带，实际 %d", res.Capacity)
	}
}

// TestDeclareEmptyLabelsClearsAndReturnsObject 验证空标签集清空全部标签，且回带值为空 map（序列化为 {} 而非 null）。
func TestDeclareEmptyLabelsClearsAndReturnsObject(t *testing.T) {
	svc, reg := newDeclarationTestService(t)
	res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Labels: map[string]string{}})
	if err != nil {
		t.Fatalf("空标签集应被接受（= 清空）: %v", err)
	}
	if res.Labels == nil {
		t.Fatal("回带标签必须是非 nil map，否则响应序列化为 null 而非 {}")
	}
	if len(res.Labels) != 0 {
		t.Fatalf("空标签集应清空全部标签，实际 %v", res.Labels)
	}
	if got := reg.Get("prod", "lobby-1"); len(got.Metadata) != 0 {
		t.Fatalf("内存注册表标签应已清空，实际 %v", got.Metadata)
	}
}

// TestDeclareMissingIdentityRejected 验证身份缺失被拒（IDENTITY_REQUIRED，400）。
func TestDeclareMissingIdentityRejected(t *testing.T) {
	svc, _ := newDeclarationTestService(t)
	capacity := 100
	for _, p := range []DeclarationParams{
		{ServerID: "lobby-1", Capacity: &capacity},
		{Namespace: "prod", Capacity: &capacity},
	} {
		if _, err := svc.Declare(p); !errors.Is(err, apperr.ErrIdentityRequired) {
			t.Fatalf("身份缺失应回 IDENTITY_REQUIRED，实际 %v", err)
		}
	}
}

// TestDeclareBothFieldsMissingRejected 验证两个声明字段全缺被拒（无意义空操作，400 INVALID_PARAM）。
func TestDeclareBothFieldsMissingRejected(t *testing.T) {
	svc, _ := newDeclarationTestService(t)
	if _, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1"}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("两字段全缺应回 INVALID_PARAM，实际 %v", err)
	}
}

// TestDeclareNegativeCapacityRejected 验证 capacity < 0 被拒（400 INVALID_PARAM）。
func TestDeclareNegativeCapacityRejected(t *testing.T) {
	svc, reg := newDeclarationTestService(t)
	negative := -1
	if _, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Capacity: &negative}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("capacity<0 应回 INVALID_PARAM，实际 %v", err)
	}
	if got := reg.Get("prod", "lobby-1"); got.Capacity != 200 {
		t.Fatalf("被拒声明不得改内存，实际 capacity=%d", got.Capacity)
	}
}

// TestDeclareLabelLimitsRejected 验证标签沿用 FR-227 的同一组约束：
// key 字符集 / key ≤ 32 / value ≤ 128 / 单节点 ≤ 20，任一项超界即 400 INVALID_PARAM（声明被拒）。
func TestDeclareLabelLimitsRejected(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{name: "key 含非法字符", labels: map[string]string{"bad key": "v"}},
		{name: "key 为空", labels: map[string]string{"   ": "v"}},
		{name: "key 超 32 字", labels: map[string]string{strings.Repeat("k", 33): "v"}},
		{name: "value 超 128 字", labels: map[string]string{"k": strings.Repeat("v", 129)}},
		{name: "标签数超 20", labels: manyDeclaredLabels(21)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, reg := newDeclarationTestService(t)
			if _, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Labels: tc.labels}); !errors.Is(err, apperr.ErrInvalidParam) {
				t.Fatalf("超界声明应回 INVALID_PARAM，实际 %v", err)
			}
			if got := reg.Get("prod", "lobby-1"); got.Metadata["mode"] != "survival" {
				t.Fatalf("被拒声明不得改内存，实际 %v", got.Metadata)
			}
		})
	}
}

// TestDeclareLabelBoundaryAccepted 验证边界值（key=32 / value=128 / 标签数=20）恰好在界内可被接受。
func TestDeclareLabelBoundaryAccepted(t *testing.T) {
	svc, _ := newDeclarationTestService(t)
	// 19 个普通标签 + 1 个 key/value 取边界长度的标签 = 恰好 20 个（单节点上限）。
	labels := manyDeclaredLabels(19)
	labels[strings.Repeat("k", 32)] = strings.Repeat("v", 128)
	res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Labels: labels})
	if err != nil {
		t.Fatalf("边界值应被接受: %v", err)
	}
	if len(res.Labels) != 20 {
		t.Fatalf("应回带 20 个标签，实际 %d", len(res.Labels))
	}
}

// TestDeclareTrimsLabelKey 验证 key 首尾空白被规整（与 FR-227 管理面标签同一口径）。
func TestDeclareTrimsLabelKey(t *testing.T) {
	svc, _ := newDeclarationTestService(t)
	res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Labels: map[string]string{" mode ": "creative"}})
	if err != nil {
		t.Fatalf("声明应成功: %v", err)
	}
	if _, ok := res.Labels["mode"]; !ok {
		t.Fatalf("key 应去首尾空白后落库，实际 %v", res.Labels)
	}
}

// TestDeclareUnregisteredRejected 验证未注册实例（含未挂载数据面）被拒为 NOT_REGISTERED（404）。
func TestDeclareUnregisteredRejected(t *testing.T) {
	svc, reg := newDeclarationTestService(t)
	capacity := 100
	if _, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "ghost", Capacity: &capacity}); !errors.Is(err, apperr.ErrNotRegistered) {
		t.Fatalf("未注册应回 NOT_REGISTERED，实际 %v", err)
	}
	if n := reg.CountByNamespace("prod"); n != 1 {
		t.Fatalf("被拒声明不得新增实例条目，实际 %d 条", n)
	}
}

// TestDeclareIdempotent 验证重复上报幂等：实例条目数不变、标签不叠加、结果与一次上报一致。
func TestDeclareIdempotent(t *testing.T) {
	svc, reg := newDeclarationTestService(t)
	capacity := 500
	labels := map[string]string{"mode": "creative", "tier": "core"}
	for i := 0; i < 3; i++ {
		res, err := svc.Declare(DeclarationParams{Namespace: "prod", ServerID: "lobby-1", Capacity: &capacity, Labels: labels})
		if err != nil {
			t.Fatalf("第 %d 次声明应成功: %v", i+1, err)
		}
		if res.Capacity != 500 || len(res.Labels) != 2 {
			t.Fatalf("第 %d 次声明结果应一致，实际 %+v", i+1, res)
		}
	}
	if n := reg.CountByNamespace("prod"); n != 1 {
		t.Fatalf("重复声明不得新增实例条目，实际 %d 条", n)
	}
}

// manyDeclaredLabels 造 n 个合法标签（key 合法且互不相同）。
func manyDeclaredLabels(n int) map[string]string {
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out["k"+strings.Repeat("x", i%8)+string(rune('a'+i/8))+string(rune('a'+i%8))] = "v"
	}
	return out
}

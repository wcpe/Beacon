package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDeclarationRequestPartialRefreshSemantics 验证请求体的部分刷新语义：
// capacity / labels 各自「缺键 = 不刷新」，且 capacity 的显式 0 与缺键、labels 的显式 {} 与缺键都严格区分。
func TestDeclarationRequestPartialRefreshSemantics(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantCapacity bool // 是否提供了 capacity 键
		wantCape     int
		wantLabels   bool // 是否提供了 labels 键
		wantLabelLen int
	}{
		{name: "两字段都提供", body: `{"namespace":"prod","serverId":"lobby-1","capacity":100,"labels":{"mode":"creative"}}`,
			wantCapacity: true, wantCape: 100, wantLabels: true, wantLabelLen: 1},
		{name: "只提供 capacity", body: `{"namespace":"prod","serverId":"lobby-1","capacity":100}`,
			wantCapacity: true, wantCape: 100, wantLabels: false},
		{name: "只提供 labels", body: `{"namespace":"prod","serverId":"lobby-1","labels":{"mode":"creative"}}`,
			wantLabels: true, wantLabelLen: 1},
		{name: "capacity 显式 0", body: `{"namespace":"prod","serverId":"lobby-1","capacity":0}`,
			wantCapacity: true, wantCape: 0},
		{name: "labels 显式空对象", body: `{"namespace":"prod","serverId":"lobby-1","labels":{}}`,
			wantLabels: true, wantLabelLen: 0},
		{name: "两字段全缺", body: `{"namespace":"prod","serverId":"lobby-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req declarationRequest
			if err := json.NewDecoder(strings.NewReader(tc.body)).Decode(&req); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if (req.Capacity != nil) != tc.wantCapacity {
				t.Fatalf("capacity 缺键语义错误：want 提供=%v，实际 %v", tc.wantCapacity, req.Capacity)
			}
			if tc.wantCapacity && *req.Capacity != tc.wantCape {
				t.Fatalf("capacity 取值错误：want %d，实际 %d", tc.wantCape, *req.Capacity)
			}
			if (req.Labels != nil) != tc.wantLabels {
				t.Fatalf("labels 缺键语义错误：want 提供=%v，实际 %v", tc.wantLabels, req.Labels)
			}
			if tc.wantLabels && len(req.Labels) != tc.wantLabelLen {
				t.Fatalf("labels 条目数错误：want %d，实际 %d（%v）", tc.wantLabelLen, len(req.Labels), req.Labels)
			}
		})
	}
}

// TestDeclarationRequestMissingKeyIsNil 锁定「缺键 → nil」这一契约：调用方据此判定不刷新该项，
// 一旦声明字段类型退化为非指针 / 非 map（0 与缺键不可分），本用例即失败。
func TestDeclarationRequestMissingKeyIsNil(t *testing.T) {
	var req declarationRequest
	if err := json.NewDecoder(strings.NewReader(`{"namespace":"prod","serverId":"lobby-1"}`)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.Capacity != nil {
		t.Fatalf("缺 capacity 键应为 nil 指针，实际 %d", *req.Capacity)
	}
	if req.Labels != nil {
		t.Fatalf("缺 labels 键应为 nil map，实际 %v", req.Labels)
	}
	if req.Namespace != "prod" || req.ServerID != "lobby-1" {
		t.Fatalf("身份字段解析错误：%+v", req)
	}
}

// TestDeclarationRequestBodyRejectedOnBadJSON 验证请求体不是 JSON 对象时解析失败（handler 据此回 400 INVALID_PARAM）。
func TestDeclarationRequestBodyRejectedOnBadJSON(t *testing.T) {
	var req declarationRequest
	if err := json.NewDecoder(strings.NewReader(`"not-an-object"`)).Decode(&req); err == nil {
		t.Fatal("非对象请求体应解析失败")
	}
}

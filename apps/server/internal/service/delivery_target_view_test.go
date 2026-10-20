package service

import (
	"encoding/json"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestChangeTargetViewCarriesReloadReport 断言目标视图透出 reload 段（FR-274）。
//
// 契约面：`packages/contracts/src/delivery.ts` 的 ChangeTarget.reloadReport。
// 形状必须与 agent 上报原文一致（前端按 plugins[] 逐项渲染）。
func TestChangeTargetViewCarriesReloadReport(t *testing.T) {
	raw := `{"total":2,"okCount":1,"failedCount":1,"timeoutMs":30000,` +
		`"plugins":[{"listener":"A","status":"ok","reason":"","timeout":false,` +
		`"appliedMd5":"md5-new","appliedVersion":"v7","appliedCount":12},` +
		`{"listener":"B","status":"failed","reason":"解析失败","timeout":false,` +
		`"appliedMd5":"","appliedVersion":""}]}`
	targets := []model.ChangeTarget{
		{ServerID: "lobby-1", BatchID: 1, Status: model.ChangeTargetStatusFailed, ReloadReport: raw},
		// 老 agent 未上报：必须保持 null，前端据此显示「无重载信息」。
		{ServerID: "lobby-2", BatchID: 1, Status: model.ChangeTargetStatusActivated},
	}

	views := changeTargetViews(targets, map[uint]int{1: 1}, nil)

	if views[0].ReloadReport == nil {
		t.Fatal("上报了 reload 段的目标必须透出 reloadReport")
	}
	var got struct {
		Total       int `json:"total"`
		FailedCount int `json:"failedCount"`
		Plugins     []struct {
			Listener       string `json:"listener"`
			Status         string `json:"status"`
			AppliedVersion string `json:"appliedVersion"`
			AppliedCount   *int   `json:"appliedCount"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(views[0].ReloadReport, &got); err != nil {
		t.Fatalf("reloadReport 必须是合法 JSON：%v", err)
	}
	if got.Total != 2 || got.FailedCount != 1 || len(got.Plugins) != 2 {
		t.Fatalf("reloadReport 形状不符：%+v", got)
	}
	if got.Plugins[0].AppliedVersion != "v7" || got.Plugins[0].AppliedCount == nil {
		t.Fatalf("自报字段必须保留：%+v", got.Plugins[0])
	}
	// 未自报 appliedCount 必须是「缺席」，不得补 0（0 是合法值）。
	if got.Plugins[1].AppliedCount != nil {
		t.Fatalf("未自报的 appliedCount 必须缺席，实际 %v", *got.Plugins[1].AppliedCount)
	}

	if views[1].ReloadReport != nil {
		t.Fatal("老 agent 未上报时必须为 null，不得写成空对象（否则前端会把「不知道」显示成「全部成功」）")
	}
}

// TestRawJSONOrNil 断言脏数据不会拖垮整页响应。
func TestRawJSONOrNil(t *testing.T) {
	if rawJSONOrNil("") != nil {
		t.Fatal("空串必须返回 nil")
	}
	if rawJSONOrNil("not-json") != nil {
		t.Fatal("非法 JSON 必须返回 nil，否则整个列表响应会序列化失败")
	}
	if got := rawJSONOrNil(`{"a":1}`); string(got) != `{"a":1}` {
		t.Fatalf("合法 JSON 必须原样透传，实际 %q", string(got))
	}
}

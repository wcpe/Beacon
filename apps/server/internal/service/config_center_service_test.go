package service

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// wantAppErrCode 断言错误为指定业务码的 apperr。
func wantAppErrCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("应报 %s，得到 %v", code, err)
	}
}

func TestParseConfigContent_语法门(t *testing.T) {
	if _, err := parseConfigContent(merge.FormatYAML, "a: [broken"); err == nil {
		t.Fatal("坏 yaml 应报语法错误")
	} else {
		wantAppErrCode(t, err, "CONFIG_SYNTAX_INVALID")
	}
	_, err := parseConfigContent(merge.FormatYAML, "")
	wantAppErrCode(t, err, "CONFIG_SYNTAX_INVALID") // 空内容
	_, err = parseConfigContent(merge.FormatYAML, "- a\n- b")
	wantAppErrCode(t, err, "CONFIG_SYNTAX_INVALID") // 顶层列表
	_, err = parseConfigContent(merge.FormatYAML, "just a scalar")
	wantAppErrCode(t, err, "CONFIG_SYNTAX_INVALID") // 顶层标量
	_, err = parseConfigContent(merge.FormatYAML, "nest:\n  1: x")
	wantAppErrCode(t, err, "CONFIG_SYNTAX_INVALID") // 嵌套非字符串键
	if _, err := parseConfigContent(merge.FormatYAML, "a: 1\nnest:\n  b: 2"); err != nil {
		t.Fatalf("合法内容不应报错: %v", err)
	}
}

func TestCheckBasedOnVersion_乐观并发(t *testing.T) {
	head := &model.ConfigLayerVersion{ID: 7}
	seven, eight := uint(7), uint(8)
	if err := checkBasedOnVersion(nil, nil); err != nil {
		t.Fatalf("链空 + null 基线应通过: %v", err)
	}
	if err := checkBasedOnVersion(head, &seven); err != nil {
		t.Fatalf("基线等于 head 应通过: %v", err)
	}
	wantAppErrCode(t, checkBasedOnVersion(head, &eight), "CONFIG_VERSION_CONFLICT")
	wantAppErrCode(t, checkBasedOnVersion(head, nil), "CONFIG_VERSION_CONFLICT")
	wantAppErrCode(t, checkBasedOnVersion(nil, &seven), "CONFIG_VERSION_CONFLICT")
}

func TestNormalizeContent_键序幂等(t *testing.T) {
	a, err := parseConfigContent(merge.FormatYAML, "b: 2\na: 1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	b, err := parseConfigContent(merge.FormatYAML, "a: 1\nb: 2")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	_, hashA, err := normalizeContent(merge.FormatYAML, a)
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	_, hashB, err := normalizeContent(merge.FormatYAML, b)
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if hashA != hashB {
		t.Fatal("同内容不同键序归一化 hash 应幂等")
	}
}

func TestDiffLeafPaths_审计摘要(t *testing.T) {
	oldLeaves := map[string]string{"a": "1", "b": "2", "gone": "x"}
	newLeaves := map[string]string{"a": "1", "b": "3", "fresh": "y"}
	added, changed, removed := diffLeafPaths(oldLeaves, newLeaves)
	if len(added) != 1 || added[0] != "fresh" {
		t.Fatalf("added = %v", added)
	}
	if len(changed) != 1 || changed[0] != "b" {
		t.Fatalf("changed = %v", changed)
	}
	if len(removed) != 1 || removed[0] != "gone" {
		t.Fatalf("removed = %v", removed)
	}
}

func TestFlattenLeafValues(t *testing.T) {
	parsed, err := merge.Parse(merge.FormatYAML, "a: 1\nnest:\n  b: x\n  deep:\n    c: true\nlist:\n  - 1\n  - 2\nnul: null")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := flattenLeafValues(parsed)
	want := map[string]string{"a": "1", "nest.b": "x", "nest.deep.c": "true", "list": "[1,2]", "nul": "null"}
	if len(got) != len(want) {
		t.Fatalf("叶子数 = %d，期望 %d：%v", len(got), len(want), got)
	}
	for path, value := range want {
		if got[path] != value {
			t.Errorf("%s = %q，期望 %q", path, got[path], value)
		}
	}
	if n := len(flattenLeafValues(nil)); n != 0 {
		t.Fatalf("nil 整体应得空映射，实际 %d 项", n)
	}
}

func TestBuildDiffView_三类差异与unified(t *testing.T) {
	left := map[string]string{"same": "1", "changed": "old", "gone": "x"}
	right := map[string]string{"same": "1", "changed": "new", "fresh": "y"}
	view := buildDiffView("scope:namespace:1", "scope:zone:3", left, right)
	if len(view.Added) != 1 || view.Added[0].Path != "fresh" || view.Added[0].Right != "y" {
		t.Fatalf("added = %+v", view.Added)
	}
	if len(view.Removed) != 1 || view.Removed[0].Path != "gone" || view.Removed[0].Left != "x" {
		t.Fatalf("removed = %+v", view.Removed)
	}
	if len(view.Changed) != 1 || view.Changed[0].Left != "old" || view.Changed[0].Right != "new" {
		t.Fatalf("changed = %+v", view.Changed)
	}
	wantDiff := "--- scope:namespace:1\n+++ scope:zone:3\n- gone: x\n- changed: old\n+ changed: new\n+ fresh: y"
	if view.UnifiedDiff != wantDiff {
		t.Fatalf("unifiedDiff =\n%s\n期望\n%s", view.UnifiedDiff, wantDiff)
	}
}

func TestProvScope_编解码往返(t *testing.T) {
	ref := configScopeRef{Level: model.ConfigScopeZone, RefID: 42}
	encoded := encodeProvScope(ref, 7)
	gotRef, gotNo := decodeProvScope(encoded)
	if gotRef != ref || gotNo != 7 {
		t.Fatalf("编解码不闭环: %v / %d", gotRef, gotNo)
	}
}

func TestSortScopeSummaries_层序低到高(t *testing.T) {
	list := []ConfigScopeSummaryView{
		{ScopeLevel: model.ConfigScopeServer, ScopeRefID: 9},
		{ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: 1},
		{ScopeLevel: model.ConfigScopeZone, ScopeRefID: 5},
		{ScopeLevel: model.ConfigScopeZone, ScopeRefID: 3},
	}
	sortScopeSummaries(list)
	var levels []string
	for _, item := range list {
		levels = append(levels, item.ScopeLevel)
	}
	if levels[0] != model.ConfigScopeNamespace || levels[1] != model.ConfigScopeZone ||
		levels[2] != model.ConfigScopeZone || levels[3] != model.ConfigScopeServer {
		t.Fatalf("层序错误: %v", levels)
	}
	if list[1].ScopeRefID != 3 {
		t.Fatal("同层应按 refId 升序")
	}
}

func TestPaginateItems(t *testing.T) {
	items := make([]ConfigFileItemView, 5)
	for i := range items {
		items[i].ID = uint(i + 1)
	}
	page, total := paginateItems(items, 2, 2)
	if total != 5 || len(page) != 2 || page[0].ID != 3 {
		t.Fatalf("分页错误: total=%d page=%+v", total, page)
	}
	empty, total := paginateItems(items, 9, 2)
	if total != 5 || len(empty) != 0 {
		t.Fatal("越界页应得空列表")
	}
}

func TestEncodeSensitivePaths_编解码闭环(t *testing.T) {
	if encodeSensitivePaths(nil) != "" {
		t.Fatal("空列表应落空串")
	}
	paths := decodeSensitivePaths(encodeSensitivePaths([]string{"a.b", "c"}))
	if len(paths) != 2 || paths[0] != "a.b" || paths[1] != "c" {
		t.Fatalf("编解码不闭环: %v", paths)
	}
	if got := decodeSensitivePaths("not-json"); len(got) != 0 {
		t.Fatal("坏 json 应得空数组")
	}
}

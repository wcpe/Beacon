package filetree

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// ---- 多层结构化深合并改无损（FR-57）：值保真 + 注释保留 ----

// TestResolveLosslessYAMLScalarFidelity 多层 yml 深合并保留未被覆盖键的原文 token（007/1.10/日期/大整数）。
func TestResolveLosslessYAMLScalarFidelity(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "zip: 007\nversion: 1.10\nrelease: 2026-06-22\nid: 123456789012345678\nport: 0\n"),
		fo("app.yml", model.ScopeServer, "port: 25565\n"),
	})
	if len(files) != 1 {
		t.Fatalf("应只剩一份合并文件，实际 %d", len(files))
	}
	got := files[0].Content
	for _, want := range []string{"zip: 007", "version: 1.10", "release: 2026-06-22", "id: 123456789012345678", "port: 25565"} {
		if !strings.Contains(got, want) {
			t.Errorf("多层深合并应保真 %q，实际：\n%s", want, got)
		}
	}
	if strings.Contains(got, "zip: 7") || strings.Contains(got, "version: 1.1\n") {
		t.Errorf("多层深合并不应归一化值，实际：\n%s", got)
	}
}

// TestResolveLosslessYAMLComments 多层 yml 深合并保留注释（头/行/嵌套）。
func TestResolveLosslessYAMLComments(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "# 头注释\nfoo: 1 # 行注释\nbar:\n  # 嵌套头\n  x: 2\n"),
		fo("app.yml", model.ScopeServer, "baz: 3\n"),
	})
	got := files[0].Content
	for _, c := range []string{"# 头注释", "# 行注释", "# 嵌套头"} {
		if !strings.Contains(got, c) {
			t.Errorf("多层深合并应保留注释 %q，实际：\n%s", c, got)
		}
	}
}

// TestResolveLosslessJSONBigInt 多层 json 深合并保留大整数精度。
func TestResolveLosslessJSONBigInt(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.json", model.ScopeGlobal, `{"id":123456789012345678,"keep":1}`),
		fo("a.json", model.ScopeServer, `{"keep":2}`),
	})
	got := files[0].Content
	if !strings.Contains(got, "123456789012345678") || strings.Contains(got, "123456789012345680") {
		t.Errorf("多层 json 深合并应保留大整数精度，实际：\n%s", got)
	}
}

// TestResolveLosslessPropertiesComments 多层 properties 深合并保留前置注释与原值。
func TestResolveLosslessPropertiesComments(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.properties", model.ScopeGlobal, "# url 说明\nurl=jdbc:base\nzip=007\n"),
		fo("a.properties", model.ScopeServer, "zip=008\n"),
	})
	got := files[0].Content
	if !strings.Contains(got, "# url 说明") {
		t.Errorf("多层 properties 深合并应保留注释，实际：\n%s", got)
	}
	if !strings.Contains(got, "zip=008") || !strings.Contains(got, "url=jdbc:base") {
		t.Errorf("多层 properties 深合并值不符，实际：\n%s", got)
	}
}

// ---- 合并语义不变（与 FR-44 现有语义用例同断言）----

// TestResolveLosslessSemanticsUnchanged 标量覆盖 / map 深合并 / null 删键 / list 整替语义不变。
func TestResolveLosslessSemanticsUnchanged(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "a: 1\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
		fo("app.yml", model.ScopeServer, "a: null\nc: 3\n"),
	})
	assertParsedEqual(t, merge.FormatYAML, files[0].Content, "b:\n  x: 1\n  y: 2\nc: 3\n")
}

// ---- 单层短路 / 豁免 / 坏内容回退不变（无损改造不得影响这三条）----

// TestResolveLosslessSingleLayerStillPassthrough 单层结构化文件仍字节原样透传（不入无损合并路径）。
func TestResolveLosslessSingleLayerStillPassthrough(t *testing.T) {
	raw := "# 注释\nzip: 007\nversion: 1.10\n"
	files := Resolve([]model.FileObject{fo("a.yml", model.ScopeGlobal, raw)})
	if len(files) != 1 || files[0].Content != raw {
		t.Fatalf("单层结构化文件应字节原样透传，实际 %q", files[0].Content)
	}
}

// TestResolveLosslessWholeFileOverrideStillExempt 任一层标豁免仍整文件覆盖取最高层原文。
func TestResolveLosslessWholeFileOverrideStillExempt(t *testing.T) {
	winner := "# 注释保留\na: 1\n"
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 0\nb: 9\n"),
		foWhole("a.yml", model.ScopeServer, winner),
	})
	if len(files) != 1 || files[0].Content != winner {
		t.Fatalf("豁免文件应整文件取最高层原文，实际 %q", files[0].Content)
	}
}

// TestResolveLosslessBadStructuredStillFallback 坏结构化内容仍回退整文件取 winner。
func TestResolveLosslessBadStructuredStillFallback(t *testing.T) {
	winner := "a: [unterminated\n"
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yml", model.ScopeServer, winner),
	})
	if len(files) != 1 || files[0].Content != winner {
		t.Fatalf("坏结构化内容应回退整文件取 winner，实际 %q", files[0].Content)
	}
}

// ---- md5 幂等 ----

// TestResolveLosslessIdempotent 相同候选两次解析 → 无损合并内容与 md5 一致。
func TestResolveLosslessIdempotent(t *testing.T) {
	cands := []model.FileObject{
		fo("app.yml", model.ScopeGlobal, "zip: 007\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
		fo("app.yml", model.ScopeServer, "c: 3\n"),
	}
	first := Resolve(cands)
	second := Resolve(cands)
	if first[0].Content != second[0].Content || first[0].MD5 != second[0].MD5 {
		t.Fatalf("无损深合并不幂等：\n1=%q(%s)\n2=%q(%s)", first[0].Content, first[0].MD5, second[0].Content, second[0].MD5)
	}
	if first[0].MD5 != md5Hex(first[0].Content) {
		t.Fatalf("md5 应基于合并后内容")
	}
}

// ---- provenance 版无损 content 保真 + 与 Resolve 交叉一致 ----

// TestResolveProvLosslessFidelity provenance 版多层深合并 content 同样无损保真（007 保真）。
func TestResolveProvLosslessFidelity(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "zip: 007\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeServer, "b:\n  y: 2\n"),
	})
	f, ok := findProv(files, "app.yml")
	if !ok {
		t.Fatal("缺 app.yml")
	}
	if f.WholeFile {
		t.Fatal("结构化非豁免文件应为深合并模式")
	}
	if !strings.Contains(f.Content, "zip: 007") {
		t.Errorf("provenance 版应无损保真 007，实际：\n%s", f.Content)
	}
}

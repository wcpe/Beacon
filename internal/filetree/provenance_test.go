package filetree

import (
	"reflect"
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// findProv 在带来源的有效文件树里按 path 取一份，不存在返回 (_, false)。
func findProv(files []EffectiveFileProvenance, path string) (EffectiveFileProvenance, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return EffectiveFileProvenance{}, false
}

// scopeOfProv 在来源列表里按键路径查来源 scope（reflect.DeepEqual 精确匹配路径，含空路径）。
func scopeOfProv(list []merge.KeyProvenance, path ...string) (string, bool) {
	want := path
	if len(path) == 0 {
		want = nil // 空路径在 provenance 里以 nil 切片表达
	}
	for _, p := range list {
		got := p.Path
		if len(got) == 0 {
			got = nil
		}
		if reflect.DeepEqual(got, want) {
			return p.Scope, true
		}
	}
	return "", false
}

// ---- 结构化文件逐键来源（复用 merge provenance）----

// TestResolveProvStructuredPerKeySources 结构化 yml 深合并：逐叶子键来源 + null 删键记入 deletions。
func TestResolveProvStructuredPerKeySources(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "a: 1\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
		fo("app.yml", model.ScopeServer, "a: null\nc: 3\n"),
	})
	if len(files) != 1 {
		t.Fatalf("应只剩一份合并文件，实际 %d", len(files))
	}
	f := files[0]
	if f.WholeFile {
		t.Fatalf("结构化非豁免文件应为深合并模式，WholeFile 不应为真")
	}
	// 合并内容语义正确
	assertParsedEqual(t, merge.FormatYAML, f.Content, "b:\n  x: 1\n  y: 2\nc: 3\n")
	// 逐键来源
	if s, _ := scopeOfProv(f.Sources, "b", "x"); s != model.ScopeGlobal {
		t.Errorf("b.x 来源应为 global，实际 %q", s)
	}
	if s, _ := scopeOfProv(f.Sources, "b", "y"); s != model.ScopeZone {
		t.Errorf("b.y 来源应为 zone，实际 %q", s)
	}
	if s, _ := scopeOfProv(f.Sources, "c"); s != model.ScopeServer {
		t.Errorf("c 来源应为 server，实际 %q", s)
	}
	// 被删的 a 不应出现在 sources，但应记入 deletions（来源层 server）
	if _, ok := scopeOfProv(f.Sources, "a"); ok {
		t.Error("被减量的 a 不应出现在 sources")
	}
	if s, ok := scopeOfProv(f.Deletions, "a"); !ok || s != model.ScopeServer {
		t.Errorf("a 应记入 deletions 且来源 server，实际 ok=%v scope=%q", ok, s)
	}
}

// TestResolveProvStructuredJSON json 同样逐键来源。
func TestResolveProvStructuredJSON(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("a.json", model.ScopeGlobal, `{"a":1,"b":{"x":1}}`),
		fo("a.json", model.ScopeServer, `{"b":{"y":2}}`),
	})
	f, ok := findProv(files, "a.json")
	if !ok {
		t.Fatal("缺 a.json")
	}
	if s, _ := scopeOfProv(f.Sources, "a"); s != model.ScopeGlobal {
		t.Errorf("a 来源应为 global，实际 %q", s)
	}
	if s, _ := scopeOfProv(f.Sources, "b", "y"); s != model.ScopeServer {
		t.Errorf("b.y 来源应为 server，实际 %q", s)
	}
}

// TestResolveProvPropertiesDottedKey properties 含点键不被误拆嵌套路径。
func TestResolveProvPropertiesDottedKey(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("s.properties", model.ScopeGlobal, "server.max-players=50\nserver.motd=hi\n"),
		fo("s.properties", model.ScopeServer, "server.max-players=80\n"),
	})
	f, _ := findProv(files, "s.properties")
	if s, _ := scopeOfProv(f.Sources, "server.max-players"); s != model.ScopeServer {
		t.Errorf("server.max-players 来源应为 server，实际 %q", s)
	}
	if s, _ := scopeOfProv(f.Sources, "server.motd"); s != model.ScopeGlobal {
		t.Errorf("server.motd 来源应为 global，实际 %q", s)
	}
}

// ---- 非结构化 / 豁免 / 坏内容 → 整文件来源（单条空路径 = winner 层）----

// TestResolveProvNonStructuredWholeFile 非结构化文件整文件覆盖，来源为单条空路径 = winner 层。
func TestResolveProvNonStructuredWholeFile(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("conf.allin", model.ScopeGlobal, "g\n"),
		fo("conf.allin", model.ScopeZone, "z\n"),
	})
	f, ok := findProv(files, "conf.allin")
	if !ok {
		t.Fatal("缺 conf.allin")
	}
	if !f.WholeFile {
		t.Error("非结构化文件应为整文件模式")
	}
	if f.Content != "z\n" {
		t.Errorf("应取最高层 zone，实际 %q", f.Content)
	}
	if len(f.Sources) != 1 {
		t.Fatalf("整文件应只有一条来源，实际 %d", len(f.Sources))
	}
	if s, ok := scopeOfProv(f.Sources); !ok || s != model.ScopeZone {
		t.Errorf("整文件来源应为单条空路径 = zone，实际 ok=%v scope=%q", ok, s)
	}
	if len(f.Deletions) != 0 {
		t.Errorf("整文件不应有 deletions，实际 %v", f.Deletions)
	}
}

// TestResolveProvWholeFileOverrideExempt 结构化文件标豁免 → 整文件模式、来源单条空路径 = winner 层。
func TestResolveProvWholeFileOverrideExempt(t *testing.T) {
	winner := "# 注释\na: 1\n"
	files := ResolveWithProvenance([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 0\nb: 9\n"),
		foWhole("a.yml", model.ScopeServer, winner),
	})
	f, _ := findProv(files, "a.yml")
	if !f.WholeFile {
		t.Error("豁免文件应为整文件模式")
	}
	if f.Content != winner {
		t.Errorf("豁免文件应整文件取 winner 原文（含注释），实际 %q", f.Content)
	}
	if s, ok := scopeOfProv(f.Sources); !ok || s != model.ScopeServer {
		t.Errorf("豁免文件来源应为单条空路径 = server，实际 ok=%v scope=%q", ok, s)
	}
}

// TestResolveProvBadStructuredFallback 坏结构化内容 → 回退整文件取 winner、整文件来源（不 panic）。
func TestResolveProvBadStructuredFallback(t *testing.T) {
	winner := "a: [unterminated\n"
	files := ResolveWithProvenance([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yml", model.ScopeServer, winner),
	})
	f, _ := findProv(files, "a.yml")
	if !f.WholeFile {
		t.Error("坏结构化内容应回退整文件模式")
	}
	if f.Content != winner {
		t.Errorf("应回退整文件取 winner，实际 %q", f.Content)
	}
	if s, ok := scopeOfProv(f.Sources); !ok || s != model.ScopeServer {
		t.Errorf("回退整文件来源应为单条空路径 = server，实际 ok=%v scope=%q", ok, s)
	}
}

// TestResolveProvSortedByPath 结果按 path 字典序稳定排序。
func TestResolveProvSortedByPath(t *testing.T) {
	files := ResolveWithProvenance([]model.FileObject{
		fo("z.yml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yml", model.ScopeGlobal, "a: 1\n"),
		fo("m.txt", model.ScopeGlobal, "x"),
	})
	if len(files) != 3 || files[0].Path != "a.yml" || files[1].Path != "m.txt" || files[2].Path != "z.yml" {
		t.Fatalf("结果未按 path 排序：%+v", files)
	}
}

// TestResolveProvEmpty 空候选 → 空文件树。
func TestResolveProvEmpty(t *testing.T) {
	if files := ResolveWithProvenance(nil); len(files) != 0 {
		t.Fatalf("空候选应返回空，实际 %+v", files)
	}
}

// ---- 与 Resolve 逐一致（防双实现漂移：provenance 版与下发解析的 content/md5 必须逐字节相等）----

// TestResolveProvConsistencyWithResolve 对各类候选集，ResolveWithProvenance 每个 path 的
// Content/MD5 必须与 Resolve 完全一致（结构化深合并 / 非结构化 / 豁免 / 坏内容 / 混合层）。
func TestResolveProvConsistencyWithResolve(t *testing.T) {
	cases := []struct {
		name       string
		candidates []model.FileObject
	}{
		{"结构化深合并", []model.FileObject{
			fo("app.yml", model.ScopeGlobal, "a: 1\nb:\n  x: 1\n"),
			fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
			fo("app.yml", model.ScopeServer, "a: null\nc: 3\n"),
		}},
		{"list 整替", []model.FileObject{
			fo("l.yml", model.ScopeGlobal, "items:\n  - a\n  - b\n"),
			fo("l.yml", model.ScopeServer, "items:\n  - c\n"),
		}},
		{"json 深合并", []model.FileObject{
			fo("a.json", model.ScopeGlobal, `{"a":1,"b":{"x":1}}`),
			fo("a.json", model.ScopeServer, `{"b":{"y":2}}`),
		}},
		{"properties 覆盖", []model.FileObject{
			fo("a.properties", model.ScopeGlobal, "k1=1\nk2=2\n"),
			fo("a.properties", model.ScopeServer, "k2=9\nk3=3\n"),
		}},
		{"非结构化整文件", []model.FileObject{
			fo("s.js", model.ScopeGlobal, "var x=1"),
			fo("s.js", model.ScopeServer, "var x=2"),
		}},
		{"结构化豁免", []model.FileObject{
			fo("e.yml", model.ScopeGlobal, "a: 0\n"),
			foWhole("e.yml", model.ScopeServer, "# 注释\na: 1\n"),
		}},
		{"坏结构化内容回退", []model.FileObject{
			fo("bad.yml", model.ScopeGlobal, "a: 1\n"),
			fo("bad.yml", model.ScopeServer, "a: [oops\n"),
		}},
		{"混合多文件", []model.FileObject{
			fo("conf.yml", model.ScopeGlobal, "a: 1\n"),
			fo("conf.yml", model.ScopeServer, "b: 2\n"),
			fo("script.js", model.ScopeGlobal, "v1"),
			fo("script.js", model.ScopeServer, "v2"),
			foWhole("raw.yml", model.ScopeServer, "# keep\nx: 1\n"),
		}},
		{"空层不贡献", []model.FileObject{
			fo("sp.yml", model.ScopeGlobal, "a: 1\n"),
			fo("sp.yml", model.ScopeZone, "   \n"),
			fo("sp.yml", model.ScopeServer, "b: 2\n"),
		}},
		{"单层结构化透传（防有损往返）", []model.FileObject{
			fo("solo.yml", model.ScopeGlobal, "zip: 007\nversion: 1.10\nrelease: 2026-06-22\n"),
		}},
		{"低层豁免 path 级", []model.FileObject{
			foWhole("lvl.yml", model.ScopeGlobal, "extra: 1\nport: 0\n"),
			fo("lvl.yml", model.ScopeServer, "port: 25565\n"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := Resolve(c.candidates)
			prov := ResolveWithProvenance(c.candidates)
			if len(base) != len(prov) {
				t.Fatalf("文件数漂移：Resolve=%d ResolveWithProvenance=%d", len(base), len(prov))
			}
			for _, bf := range base {
				pf, ok := findProv(prov, bf.Path)
				if !ok {
					t.Fatalf("path %q 在 provenance 版缺失", bf.Path)
				}
				if pf.Content != bf.Content {
					t.Fatalf("path %q 合并内容漂移：\nResolve=%q\nWithProvenance=%q", bf.Path, bf.Content, pf.Content)
				}
				if pf.MD5 != bf.MD5 {
					t.Fatalf("path %q md5 漂移：Resolve=%s WithProvenance=%s", bf.Path, bf.MD5, pf.MD5)
				}
			}
		})
	}
}

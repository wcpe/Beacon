package filetree

import (
	"reflect"
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// fo 构造一个测试用 FileObject（仅填解析关注的字段，WholeFileOverride 默认 false）。
func fo(path, level, content string) model.FileObject {
	return model.FileObject{
		Path:       path,
		ScopeLevel: level,
		Content:    content,
		ContentMD5: md5Hex(content),
		Enabled:    true,
	}
}

// foWhole 构造一个标记「整文件覆盖豁免」的 FileObject（结构化文件也不深合并）。
func foWhole(path, level, content string) model.FileObject {
	f := fo(path, level, content)
	f.WholeFileOverride = true
	return f
}

// findFile 在有效文件树里按 path 取一份，不存在返回 (EffectiveFile{}, false)。
func findFile(files []EffectiveFile, path string) (EffectiveFile, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return EffectiveFile{}, false
}

// assertParsedEqual 比对两份同格式内容解析后的结构相等（规避序列化排版差异，专注语义）。
func assertParsedEqual(t *testing.T, format, got, want string) {
	t.Helper()
	g, err := merge.Parse(format, got)
	if err != nil {
		t.Fatalf("got 解析失败：%v（内容 %q）", err, got)
	}
	w, err := merge.Parse(format, want)
	if err != nil {
		t.Fatalf("want 解析失败：%v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("合并结果不符：\n got=%#v\nwant=%#v\n(原文 got=%q)", g, w, got)
	}
}

// ---- 解析骨架（与格式无关的覆盖链行为，用非结构化后缀避免深合并 reserialize 噪声）----

// TestResolveSingleLayer 单层单文件（非结构化）：原样返回。
func TestResolveSingleLayer(t *testing.T) {
	files := Resolve([]model.FileObject{fo("a.txt", model.ScopeGlobal, "x")})
	if len(files) != 1 || files[0].Path != "a.txt" || files[0].Content != "x" {
		t.Fatalf("单层解析错误：%+v", files)
	}
}

// TestResolveHigherLayerWins 同一 path 多层（非结构化）：取层级最高那份（整文件覆盖）。
func TestResolveHigherLayerWins(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.txt", model.ScopeGlobal, "global"),
		fo("a.txt", model.ScopeGroup, "group"),
		fo("a.txt", model.ScopeServer, "server"),
		fo("a.txt", model.ScopeZone, "zone"),
	})
	if len(files) != 1 {
		t.Fatalf("同一 path 应只剩一份，实际 %d", len(files))
	}
	if files[0].Content != "server" {
		t.Fatalf("应取最高层 server，实际 %q", files[0].Content)
	}
}

// TestResolveNonStructuredNoDeepMerge 非结构化文件高层整文件替换低层，绝不拼接/合并。
func TestResolveNonStructuredNoDeepMerge(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("conf.allin", model.ScopeGlobal, "line1\nline2\n"),
		fo("conf.allin", model.ScopeZone, "only-this\n"),
	})
	if len(files) != 1 || files[0].Content != "only-this\n" {
		t.Fatalf("非结构化文件应整文件覆盖而非深合并，实际 %+v", files)
	}
}

// TestResolveDistinctPathsCoexist 不同 path 各取各的最高层，互不影响（非结构化）。
func TestResolveDistinctPathsCoexist(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.txt", model.ScopeGlobal, "ga"),
		fo("b.js", model.ScopeServer, "sb"),
		fo("b.js", model.ScopeGlobal, "gb"),
		fo("c/d.lang", model.ScopeGroup, "grp"),
	})
	if len(files) != 3 {
		t.Fatalf("应有 3 个不同 path，实际 %d：%+v", len(files), files)
	}
	if f, _ := findFile(files, "a.txt"); f.Content != "ga" {
		t.Errorf("a.txt 应取 global=ga，实际 %q", f.Content)
	}
	if f, _ := findFile(files, "b.js"); f.Content != "sb" {
		t.Errorf("b.js 应取 server=sb，实际 %q", f.Content)
	}
	if f, _ := findFile(files, "c/d.lang"); f.Content != "grp" {
		t.Errorf("c/d.lang 应取 group=grp，实际 %q", f.Content)
	}
}

// TestResolveSortedByPath 结果按 path 字典序稳定排序（保证 manifest/md5 幂等）。
func TestResolveSortedByPath(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("z.txt", model.ScopeGlobal, "1"),
		fo("a.txt", model.ScopeGlobal, "2"),
		fo("m.txt", model.ScopeGlobal, "3"),
	})
	if files[0].Path != "a.txt" || files[1].Path != "m.txt" || files[2].Path != "z.txt" {
		t.Fatalf("结果未按 path 排序：%+v", files)
	}
}

// TestResolveIgnoresUnknownLevel 非法覆盖层不参与解析（非结构化）。
func TestResolveIgnoresUnknownLevel(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.txt", "bogus", "bad"),
		fo("a.txt", model.ScopeGlobal, "good"),
	})
	if len(files) != 1 || files[0].Content != "good" {
		t.Fatalf("非法层应被忽略，实际 %+v", files)
	}
}

// TestResolveEmpty 空候选 → 空文件树。
func TestResolveEmpty(t *testing.T) {
	if files := Resolve(nil); len(files) != 0 {
		t.Fatalf("空候选应返回空，实际 %+v", files)
	}
}

// ---- 结构化深合并（FR-44 / ADR-0029）----

// TestResolveStructuredDeepMergeYAML 结构化 yml 跨层按键深合并：标量覆盖 / map 深合并 / 高层 null 删键。
func TestResolveStructuredDeepMergeYAML(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("app.yml", model.ScopeGlobal, "a: 1\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
		fo("app.yml", model.ScopeServer, "a: null\nc: 3\n"),
	})
	if len(files) != 1 {
		t.Fatalf("应只剩一份合并文件，实际 %d", len(files))
	}
	// 期望：a 被单服 null 删除；b 深合并 {x:1,y:2}；c 新增 3
	assertParsedEqual(t, merge.FormatYAML, files[0].Content, "b:\n  x: 1\n  y: 2\nc: 3\n")
}

// TestResolveStructuredListReplace list 整体替换（高层 list 整替低层，不拼接）。
func TestResolveStructuredListReplace(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("l.yml", model.ScopeGlobal, "items:\n  - a\n  - b\n"),
		fo("l.yml", model.ScopeServer, "items:\n  - c\n"),
	})
	assertParsedEqual(t, merge.FormatYAML, files[0].Content, "items:\n  - c\n")
}

// TestResolveStructuredDeepMergeJSON json 同样按键深合并。
func TestResolveStructuredDeepMergeJSON(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.json", model.ScopeGlobal, `{"a":1,"b":{"x":1}}`),
		fo("a.json", model.ScopeServer, `{"b":{"y":2}}`),
	})
	assertParsedEqual(t, merge.FormatJSON, files[0].Content, `{"a":1,"b":{"x":1,"y":2}}`)
}

// TestResolveStructuredDeepMergeProperties properties 扁平键覆盖。
func TestResolveStructuredDeepMergeProperties(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.properties", model.ScopeGlobal, "k1=1\nk2=2\n"),
		fo("a.properties", model.ScopeServer, "k2=9\nk3=3\n"),
	})
	assertParsedEqual(t, merge.FormatProperties, files[0].Content, "k1=1\nk2=9\nk3=3\n")
}

// TestResolveYAMLExtensionAlias .yaml 与 .yml 同等按 yaml 深合并。
func TestResolveYAMLExtensionAlias(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.yaml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yaml", model.ScopeServer, "b: 2\n"),
	})
	assertParsedEqual(t, merge.FormatYAML, files[0].Content, "a: 1\nb: 2\n")
}

// TestResolveWholeFileOverrideOptOut 结构化文件标豁免 → 整文件覆盖（取最高层、内容逐字节不变、不深合并）。
func TestResolveWholeFileOverrideOptOut(t *testing.T) {
	winner := "# 注释保留\na: 1\n"
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 0\nb: 9\n"),
		foWhole("a.yml", model.ScopeServer, winner),
	})
	if len(files) != 1 || files[0].Content != winner {
		t.Fatalf("豁免文件应整文件取最高层原文（含注释），实际 %q", files[0].Content)
	}
}

// TestResolveMergedMD5BasedOnMergedContent 合并文件的 md5 = 合并后整文件 md5（非任一层原始 md5）。
func TestResolveMergedMD5BasedOnMergedContent(t *testing.T) {
	g := fo("app.yml", model.ScopeGlobal, "a: 1\n")
	s := fo("app.yml", model.ScopeServer, "b: 2\n")
	files := Resolve([]model.FileObject{g, s})
	if files[0].MD5 != md5Hex(files[0].Content) {
		t.Fatalf("md5 应基于合并后内容，实际 md5=%s content=%q", files[0].MD5, files[0].Content)
	}
	if files[0].MD5 == g.ContentMD5 || files[0].MD5 == s.ContentMD5 {
		t.Fatalf("合并后 md5 不应等于任一层原始内容 md5")
	}
}

// TestResolveMergedIdempotent 相同候选两次解析 → 合并内容与 md5 完全一致（长轮询比对依赖）。
func TestResolveMergedIdempotent(t *testing.T) {
	cands := []model.FileObject{
		fo("app.yml", model.ScopeGlobal, "a: 1\nb:\n  x: 1\n"),
		fo("app.yml", model.ScopeZone, "b:\n  y: 2\n"),
		fo("app.yml", model.ScopeServer, "c: 3\n"),
	}
	first := Resolve(cands)
	second := Resolve(cands)
	if first[0].Content != second[0].Content || first[0].MD5 != second[0].MD5 {
		t.Fatalf("深合并不幂等：\n1=%q(%s)\n2=%q(%s)", first[0].Content, first[0].MD5, second[0].Content, second[0].MD5)
	}
}

// TestResolveBadStructuredFallback 某层结构化内容解析失败 → 该 path 回退整文件取 winner（不 panic、不中断整树）。
func TestResolveBadStructuredFallback(t *testing.T) {
	winner := "a: [unterminated\n" // 故意坏 yaml，作为最高层 winner
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yml", model.ScopeServer, winner),
	})
	if len(files) != 1 || files[0].Content != winner {
		t.Fatalf("坏结构化内容应回退整文件取 winner，实际 %q", files[0].Content)
	}
	if files[0].MD5 != md5Hex(winner) {
		t.Fatalf("回退后 md5 应为 winner 内容 md5")
	}
}

// TestResolveMixedStructuredAndNonStructured 同次解析里结构化深合并、非结构化整文件各行其是。
func TestResolveMixedStructuredAndNonStructured(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("conf.yml", model.ScopeGlobal, "a: 1\n"),
		fo("conf.yml", model.ScopeServer, "b: 2\n"),
		fo("script.js", model.ScopeGlobal, "var x=1"),
		fo("script.js", model.ScopeServer, "var x=2"),
	})
	if f, ok := findFile(files, "conf.yml"); !ok {
		t.Fatal("缺 conf.yml")
	} else {
		assertParsedEqual(t, merge.FormatYAML, f.Content, "a: 1\nb: 2\n")
	}
	if f, _ := findFile(files, "script.js"); f.Content != "var x=2" {
		t.Errorf("非结构化 script.js 应取最高层整文件，实际 %q", f.Content)
	}
}

// ---- manifest / fileTreeMd5（与解析下游，行为不变）----

// TestManifestPathToMD5 manifest = path→md5 映射。
func TestManifestPathToMD5(t *testing.T) {
	files := []EffectiveFile{
		{Path: "a.yml", MD5: "aaa"},
		{Path: "b.js", MD5: "bbb"},
	}
	m := Manifest(files)
	if len(m) != 2 || m["a.yml"] != "aaa" || m["b.js"] != "bbb" {
		t.Fatalf("manifest 错误：%+v", m)
	}
}

// TestFileTreeMD5Known 已知值校验（空树 = md5 of ""）。
func TestFileTreeMD5Known(t *testing.T) {
	if got := FileTreeMD5(map[string]string{}); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("空树 md5 错误：%s", got)
	}
}

// TestFileTreeMD5AvoidsSetCollision {a:x} 与 {b:x} 必须算出不同 md5（path 名纳入哈希）。
func TestFileTreeMD5AvoidsSetCollision(t *testing.T) {
	a := FileTreeMD5(map[string]string{"a.yml": "deadbeef"})
	b := FileTreeMD5(map[string]string{"b.yml": "deadbeef"})
	if a == b {
		t.Error("path 名未纳入哈希：集合碰撞未消除")
	}
}

// TestFileTreeMD5OrderIndependent fileTreeMd5 与 map 遍历顺序无关（内部按 path 排序）。
func TestFileTreeMD5OrderIndependent(t *testing.T) {
	want := FileTreeMD5(map[string]string{"a.yml": "1", "b.js": "2", "c/d.lang": "3"})
	for i := 0; i < 50; i++ {
		again := map[string]string{"c/d.lang": "3", "a.yml": "1", "b.js": "2"}
		if FileTreeMD5(again) != want {
			t.Fatalf("第 %d 次 fileTreeMd5 漂移", i)
		}
	}
}

// TestFileTreeMD5ChangesWithContent 任一文件内容变 → fileTreeMd5 变。
func TestFileTreeMD5ChangesWithContent(t *testing.T) {
	base := FileTreeMD5(map[string]string{"a.yml": "x", "b.js": "y"})
	changed := FileTreeMD5(map[string]string{"a.yml": "x", "b.js": "z"})
	if base == changed {
		t.Error("内容变化未反映到 fileTreeMd5")
	}
}

// TestFileTreeMD5ChangesWithPathSet 增删 path → fileTreeMd5 变。
func TestFileTreeMD5ChangesWithPathSet(t *testing.T) {
	base := FileTreeMD5(map[string]string{"a.yml": "x"})
	added := FileTreeMD5(map[string]string{"a.yml": "x", "b.js": "y"})
	if base == added {
		t.Error("新增 path 未反映到 fileTreeMd5")
	}
}

// TestResolveThenMD5Idempotent 端到端：相同候选两次解析→manifest→md5 结果一致。
func TestResolveThenMD5Idempotent(t *testing.T) {
	candidates := []model.FileObject{
		fo("a.yml", model.ScopeGlobal, "a: 1\n"),
		fo("a.yml", model.ScopeServer, "b: 2\n"),
		fo("b.js", model.ScopeGroup, "gb"),
	}
	first := FileTreeMD5(Manifest(Resolve(candidates)))
	second := FileTreeMD5(Manifest(Resolve(candidates)))
	if first != second {
		t.Fatalf("解析→md5 不幂等：%s != %s", first, second)
	}
}

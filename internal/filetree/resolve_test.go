package filetree

import (
	"testing"

	"beacon/internal/model"
)

// fo 构造一个测试用 FileObject（仅填解析关注的字段）。
func fo(path, level, content string) model.FileObject {
	return model.FileObject{
		Path:       path,
		ScopeLevel: level,
		Content:    content,
		ContentMD5: md5Hex(content),
		Enabled:    true,
	}
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

// TestResolveSingleLayer 单层单文件：原样返回。
func TestResolveSingleLayer(t *testing.T) {
	files := Resolve([]model.FileObject{fo("a.yml", model.ScopeGlobal, "x")})
	if len(files) != 1 || files[0].Path != "a.yml" || files[0].Content != "x" {
		t.Fatalf("单层解析错误：%+v", files)
	}
}

// TestResolveHigherLayerWins 同一 path 多层：取层级最高那份（整文件覆盖，不合并内容）。
func TestResolveHigherLayerWins(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "global"),
		fo("a.yml", model.ScopeGroup, "group"),
		fo("a.yml", model.ScopeServer, "server"),
		fo("a.yml", model.ScopeZone, "zone"),
	})
	if len(files) != 1 {
		t.Fatalf("同一 path 应只剩一份，实际 %d", len(files))
	}
	if files[0].Content != "server" {
		t.Fatalf("应取最高层 server，实际 %q", files[0].Content)
	}
}

// TestResolveNoDeepMerge 高层内容整文件替换低层，绝不拼接/合并。
func TestResolveNoDeepMerge(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("conf.allin", model.ScopeGlobal, "line1\nline2\n"),
		fo("conf.allin", model.ScopeZone, "only-this\n"),
	})
	if len(files) != 1 || files[0].Content != "only-this\n" {
		t.Fatalf("文件树应整文件覆盖而非深合并，实际 %+v", files)
	}
}

// TestResolveDistinctPathsCoexist 不同 path 各取各的最高层，互不影响。
func TestResolveDistinctPathsCoexist(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.yml", model.ScopeGlobal, "ga"),
		fo("b.js", model.ScopeServer, "sb"),
		fo("b.js", model.ScopeGlobal, "gb"),
		fo("c/d.lang", model.ScopeGroup, "grp"),
	})
	if len(files) != 3 {
		t.Fatalf("应有 3 个不同 path，实际 %d：%+v", len(files), files)
	}
	if f, _ := findFile(files, "a.yml"); f.Content != "ga" {
		t.Errorf("a.yml 应取 global=ga，实际 %q", f.Content)
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
		fo("z.yml", model.ScopeGlobal, "1"),
		fo("a.yml", model.ScopeGlobal, "2"),
		fo("m.yml", model.ScopeGlobal, "3"),
	})
	if files[0].Path != "a.yml" || files[1].Path != "m.yml" || files[2].Path != "z.yml" {
		t.Fatalf("结果未按 path 排序：%+v", files)
	}
}

// TestResolveIgnoresUnknownLevel 非法覆盖层不参与解析。
func TestResolveIgnoresUnknownLevel(t *testing.T) {
	files := Resolve([]model.FileObject{
		fo("a.yml", "bogus", "bad"),
		fo("a.yml", model.ScopeGlobal, "good"),
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
		fo("a.yml", model.ScopeGlobal, "ga"),
		fo("a.yml", model.ScopeServer, "sa"),
		fo("b.js", model.ScopeGroup, "gb"),
	}
	first := FileTreeMD5(Manifest(Resolve(candidates)))
	second := FileTreeMD5(Manifest(Resolve(candidates)))
	if first != second {
		t.Fatalf("解析→md5 不幂等：%s != %s", first, second)
	}
}

package merge_test

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
)

// scopeOf 在 provenance 列表里按键路径查来源 scope。
func scopeOf(list []merge.KeyProvenance, path ...string) (string, bool) {
	want := strings.Join(path, "\x00")
	for _, p := range list {
		if strings.Join(p.Path, "\x00") == want {
			return p.Scope, true
		}
	}
	return "", false
}

func mustScope(t *testing.T, list []merge.KeyProvenance, scope string, path ...string) {
	t.Helper()
	got, ok := scopeOf(list, path...)
	if !ok {
		t.Fatalf("路径 %v 未在 provenance 中找到", path)
	}
	if got != scope {
		t.Fatalf("路径 %v 来源期望 %s，实际 %s", path, scope, got)
	}
}

// 单层 global 基线：所有键来源 global，无删除。
func TestProvenance_GlobalBaselineOnly(t *testing.T) {
	content, sources, dels, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "a: 1\nb: 2\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "a: 1") || !strings.Contains(content, "b: 2") {
		t.Fatalf("合并内容不含基线键: %q", content)
	}
	mustScope(t, sources, "global", "a")
	mustScope(t, sources, "global", "b")
	if len(dels) != 0 {
		t.Fatalf("不应有删除记录，实际 %v", dels)
	}
}

// server 层增量：改键 b、加键 c，其余继承基线。
func TestProvenance_ServerIncrement(t *testing.T) {
	_, sources, dels, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "a: 1\nb: 2\n"},
		{Scope: "server", Content: "b: 3\nc: 4\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "global", "a")
	mustScope(t, sources, "server", "b")
	mustScope(t, sources, "server", "c")
	if len(dels) != 0 {
		t.Fatalf("增量不应产生删除，实际 %v", dels)
	}
}

// server 层减量：写 null 删掉基线键 b。
func TestProvenance_ServerDecrement(t *testing.T) {
	content, sources, dels, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "a: 1\nb: 2\n"},
		{Scope: "server", Content: "b: null\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "b:") {
		t.Fatalf("减量后 b 不应出现在合并内容: %q", content)
	}
	mustScope(t, sources, "global", "a")
	if _, ok := scopeOf(sources, "b"); ok {
		t.Fatal("被删的 b 不应出现在 sources")
	}
	mustScope(t, dels, "server", "b")
}

// 嵌套 map 深合并：逐叶子键各有来源。
func TestProvenance_Nested(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "db:\n  host: h\n  port: 1\n"},
		{Scope: "zone", Content: "db:\n  port: 2\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "global", "db", "host")
	mustScope(t, sources, "zone", "db", "port")
}

// list 整体替换：高层胜，list 作为单个叶子记来源。
func TestProvenance_ListReplace(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "items:\n  - 1\n  - 2\n"},
		{Scope: "server", Content: "items:\n  - 3\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "server", "items")
}

// 删后重加：group 删、server 又加 → 最终存在、来源 server、不计入减量。
func TestProvenance_DeleteThenReadd(t *testing.T) {
	content, sources, dels, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "a: 1\n"},
		{Scope: "group", Content: "a: null\n"},
		{Scope: "server", Content: "a: 9\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "a: 9") {
		t.Fatalf("重加后 a 应为 9: %q", content)
	}
	mustScope(t, sources, "server", "a")
	if len(dels) != 0 {
		t.Fatalf("删后重加不应计入减量，实际 %v", dels)
	}
}

// 删整子树后高层重加部分子键：父键不应再算减量（其子键最终存在），避免与 sources 自相矛盾。
func TestProvenance_SubtreeDeleteThenPartialReadd(t *testing.T) {
	content, sources, dels, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "db:\n  host: h\n  port: 1\n"},
		{Scope: "group", Content: "db: null\n"},
		{Scope: "zone", Content: "db:\n  host: z\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "host: z") {
		t.Fatalf("重加后 db.host 应为 z: %q", content)
	}
	mustScope(t, sources, "zone", "db", "host")
	if len(dels) != 0 {
		t.Fatalf("子树删后又重加子键，父键不应报减量，实际 %v", dels)
	}
}

// 标量被高层替换为 map：来源记到新叶子键，原标量来源被清。
func TestProvenance_ScalarToMap(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "a: scalar\n"},
		{Scope: "server", Content: "a:\n  x: 1\n  y: 2\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "server", "a", "x")
	mustScope(t, sources, "server", "a", "y")
	if _, ok := scopeOf(sources, "a"); ok {
		t.Fatal("a 被替换为 map 后不应再作为标量叶子")
	}
}

// 顶层为标量：path 为空的单条来源（高层整体替换）。
func TestProvenance_TopLevelScalar(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "hello\n"},
		{Scope: "server", Content: "world\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Scope != "server" || len(sources[0].Path) != 0 {
		t.Fatalf("顶层标量应有单条空路径来源 server，实际 %v", sources)
	}
}

// properties 扁平含点键：键本身带 '.'，不应被误拆成嵌套路径。
func TestProvenance_PropertiesDottedKeys(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("properties", []merge.ProvLayer{
		{Scope: "global", Content: "server.max-players=50\nserver.motd=hi\n"},
		{Scope: "server", Content: "server.max-players=80\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "server", "server.max-players")
	mustScope(t, sources, "global", "server.motd")
}

// 空层跳过：global 空、server 提供键。
func TestProvenance_EmptyLayerSkipped(t *testing.T) {
	_, sources, _, err := merge.MergeDataIDWithProvenance("yaml", []merge.ProvLayer{
		{Scope: "global", Content: "   \n"},
		{Scope: "server", Content: "a: 1\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustScope(t, sources, "server", "a")
}

// 关键：合并文本与 MergeDataID 逐一致（防 provenance 版与热路径合并实现漂移）。
func TestProvenance_ConsistencyWithMergeDataID(t *testing.T) {
	cases := []struct {
		name   string
		format string
		layers []merge.ProvLayer
	}{
		{"yaml-scalar-override", "yaml", []merge.ProvLayer{
			{"global", "a: 1\nb: 2\n"}, {"server", "b: 3\nc: 4\n"},
		}},
		{"yaml-nested", "yaml", []merge.ProvLayer{
			{"global", "db:\n  host: h\n  port: 1\nx: 1\n"},
			{"group", "db:\n  port: 2\n"},
			{"server", "db:\n  user: root\n"},
		}},
		{"yaml-null-delete", "yaml", []merge.ProvLayer{
			{"global", "a: 1\nb: 2\nc: 3\n"}, {"zone", "b: null\n"},
		}},
		{"yaml-delete-readd", "yaml", []merge.ProvLayer{
			{"global", "a: 1\n"}, {"group", "a: null\n"}, {"server", "a: 9\n"},
		}},
		{"yaml-list-replace", "yaml", []merge.ProvLayer{
			{"global", "items:\n  - 1\n  - 2\n"}, {"server", "items:\n  - 3\n"},
		}},
		{"json-nested", "json", []merge.ProvLayer{
			{"global", `{"a":{"x":1,"y":2},"k":1}`}, {"server", `{"a":{"y":9},"z":3}`},
		}},
		{"properties", "properties", []merge.ProvLayer{
			{"global", "server.max-players=50\nserver.motd=hi\n"},
			{"server", "server.max-players=80\nrcon=true\n"},
		}},
		{"empty-layers", "yaml", []merge.ProvLayer{
			{"global", "\n"}, {"group", "a: 1\n"}, {"zone", "   \n"},
		}},
		{"type-mismatch", "yaml", []merge.ProvLayer{
			{"global", "a:\n  b: 1\n"}, {"server", "a: scalar\n"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			contents := make([]string, len(c.layers))
			for i, l := range c.layers {
				contents[i] = l.Content
			}
			want, err := merge.MergeDataID(c.format, contents)
			if err != nil {
				t.Fatalf("MergeDataID 失败: %v", err)
			}
			got, _, _, err := merge.MergeDataIDWithProvenance(c.format, c.layers)
			if err != nil {
				t.Fatalf("MergeDataIDWithProvenance 失败: %v", err)
			}
			if got != want {
				t.Fatalf("合并结果漂移:\n--- MergeDataID ---\n%q\n--- WithProvenance ---\n%q", want, got)
			}
		})
	}
}

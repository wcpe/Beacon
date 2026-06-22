package merge

import (
	"reflect"
	"strings"
	"testing"
)

// ---- 三格式值保真 round-trip（无损合并不归一化叶子标量）----

// TestLosslessYAMLScalarFidelity 多层 yaml 合并保留叶子标量原文 token（前导零 / 版本号 / 日期 / 大整数）。
func TestLosslessYAMLScalarFidelity(t *testing.T) {
	// 低层基线，高层只覆盖一个键；其余键必须原文保真。
	low := "zip: 007\nversion: 1.10\nrelease: 2026-06-22\nid: 123456789012345678\nkeep: 1\n"
	high := "keep: 2\n"
	out, err := MergeDataIDLossless(FormatYAML, []string{low, high})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	for _, want := range []string{"zip: 007", "version: 1.10", "release: 2026-06-22", "id: 123456789012345678", "keep: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("无损 yaml 合并应保留 %q，实际输出：\n%s", want, out)
		}
	}
	// 不得出现归一化后的值
	for _, bad := range []string{"zip: 7", "version: 1.1\n", "keep: 1\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("无损 yaml 合并不应出现归一化值 %q，实际输出：\n%s", bad, out)
		}
	}
}

// TestLosslessJSONBigIntFidelity 多层 json 合并保留大整数精度（不经 float64 失精度）。
func TestLosslessJSONBigIntFidelity(t *testing.T) {
	low := `{"id":123456789012345678,"zip":"007","keep":1}`
	high := `{"keep":2}`
	out, err := MergeDataIDLossless(FormatJSON, []string{low, high})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	if !strings.Contains(out, "123456789012345678") {
		t.Errorf("无损 json 合并应保留大整数精度，实际输出：\n%s", out)
	}
	if strings.Contains(out, "123456789012345680") {
		t.Errorf("无损 json 合并不应失精度（…680），实际输出：\n%s", out)
	}
	if !strings.Contains(out, `"007"`) || !strings.Contains(out, `"keep": 2`) {
		t.Errorf("无损 json 合并键值不符，实际输出：\n%s", out)
	}
}

// TestLosslessPropertiesValueFidelity properties 保留原值文本（不解析数字 / 不归一化）。
func TestLosslessPropertiesValueFidelity(t *testing.T) {
	low := "zip=007\nversion=1.10\nkeep=1\n"
	high := "keep=2\n"
	out, err := MergeDataIDLossless(FormatProperties, []string{low, high})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	want := "keep=2\nversion=1.10\nzip=007\n" // 键字典序
	if out != want {
		t.Errorf("properties 无损合并值保真错误：\ngot=%q\nwant=%q", out, want)
	}
}

// ---- YAML 注释保留（头 / 行 / 脚 + 嵌套）----

// TestLosslessYAMLCommentsPreserved 多层 yaml 合并保留各类注释（随其归属键搬）。
func TestLosslessYAMLCommentsPreserved(t *testing.T) {
	low := "# 头注释\nfoo: 1 # 行注释\nbar:\n  # 嵌套头\n  x: 2\n"
	high := "baz: 3\n"
	out, err := MergeDataIDLossless(FormatYAML, []string{low, high})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	for _, c := range []string{"# 头注释", "# 行注释", "# 嵌套头"} {
		if !strings.Contains(out, c) {
			t.Errorf("无损 yaml 合并应保留注释 %q，实际输出：\n%s", c, out)
		}
	}
}

// TestLosslessYAMLCommentTravelsWithKeyOnReorder 注释随键搬：键被排序后注释仍贴在对应键上。
func TestLosslessYAMLCommentTravelsWithKeyOnReorder(t *testing.T) {
	// 输入键序 z,a；确定性键序输出 a,z。a 带头注释应随 a 搬。
	out, err := MergeDataIDLossless(FormatYAML, []string{"z: 1\n# a 的注释\na: 2\n"})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// 找到 "# a 的注释" 行，下一行应是 a:
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "# a 的注释") {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[idx+1]), "a:") {
		t.Errorf("a 的注释未随 a 键搬动，实际输出：\n%s", out)
	}
}

// ---- properties 注释保留 ----

// TestLosslessPropertiesCommentsPreserved key 前置注释行随键保留。
func TestLosslessPropertiesCommentsPreserved(t *testing.T) {
	low := "# url 说明\nurl=jdbc:base\n# pool 说明\npool=10\n"
	high := "pool=20\n"
	out, err := MergeDataIDLossless(FormatProperties, []string{low, high})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	if !strings.Contains(out, "# url 说明") || !strings.Contains(out, "# pool 说明") {
		t.Errorf("properties 无损合并应保留前置注释，实际输出：\n%s", out)
	}
	// 注释贴在对应键上方
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "# pool 说明" {
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "pool=") {
				t.Errorf("# pool 说明 未贴在 pool 键上方，实际输出：\n%s", out)
			}
		}
	}
}

// ---- 合并语义不变（与 MergeDataID 现有语义用例对齐）----

// TestLosslessMergeSemanticsYAML 标量覆盖 / map 深合并 / null 删键，语义与有损版一致。
func TestLosslessMergeSemanticsYAML(t *testing.T) {
	global := "a: 1\nb:\n  x: 1\n"
	zone := "b:\n  y: 2\n"
	server := "a: null\nc: 3\n"
	out, err := MergeDataIDLossless(FormatYAML, []string{global, zone, server})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	parsed, _ := Parse(FormatYAML, out)
	want := map[string]any{"b": map[string]any{"x": 1, "y": 2}, "c": 3}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("无损 yaml 合并语义错误：got=%v want=%v", parsed, want)
	}
}

// TestLosslessListReplace list 整体替换（不拼接）。
func TestLosslessListReplace(t *testing.T) {
	out, err := MergeDataIDLossless(FormatYAML, []string{"items:\n  - a\n  - b\n", "items:\n  - c\n"})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	parsed, _ := Parse(FormatYAML, out)
	want := map[string]any{"items": []any{"c"}}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("无损 list 整替错误：got=%v want=%v", parsed, want)
	}
}

// TestLosslessTypeMismatchReplace 类型不一致整替（map 被标量替换）。
func TestLosslessTypeMismatchReplace(t *testing.T) {
	out, err := MergeDataIDLossless(FormatYAML, []string{"a:\n  x: 1\n", "a: 5\n"})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	parsed, _ := Parse(FormatYAML, out)
	want := map[string]any{"a": 5}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("无损类型不一致替换错误：got=%v want=%v", parsed, want)
	}
}

// TestLosslessEmptyLayerSkipped 空层 / 纯注释层不贡献、不抹低层。
func TestLosslessEmptyLayerSkipped(t *testing.T) {
	out, err := MergeDataIDLossless(FormatYAML, []string{"a: 1\n", "", "   \n", "# 仅注释\n"})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	parsed, _ := Parse(FormatYAML, out)
	if !reflect.DeepEqual(parsed, map[string]any{"a": 1}) {
		t.Errorf("空层 / 纯注释层应被跳过：got=%v", parsed)
	}
}

// TestLosslessAllEmptyReturnsEmpty 全空层返回空串。
func TestLosslessAllEmptyReturnsEmpty(t *testing.T) {
	out, err := MergeDataIDLossless(FormatYAML, []string{"", "   \n"})
	if err != nil {
		t.Fatalf("无损合并失败: %v", err)
	}
	if out != "" {
		t.Errorf("全空层应返回空串，实际 %q", out)
	}
}

// ---- 确定性键序 / md5 幂等 ----

// TestLosslessIdempotentKeyOrderYAML 不同输入键序得到相同 md5（确定性键序）。
func TestLosslessIdempotentKeyOrderYAML(t *testing.T) {
	out1, _ := MergeDataIDLossless(FormatYAML, []string{"b: 2\na: 1\nc: 3\n"})
	out2, _ := MergeDataIDLossless(FormatYAML, []string{"c: 3\na: 1\nb: 2\n"})
	if MD5Hex(out1) != MD5Hex(out2) {
		t.Errorf("无损 yaml 非幂等：\nout1=%q\nout2=%q", out1, out2)
	}
}

// TestLosslessIdempotentKeyOrderJSON json 同理。
func TestLosslessIdempotentKeyOrderJSON(t *testing.T) {
	out1, _ := MergeDataIDLossless(FormatJSON, []string{`{"b":2,"a":1,"c":3}`})
	out2, _ := MergeDataIDLossless(FormatJSON, []string{`{"c":3,"a":1,"b":2}`})
	if MD5Hex(out1) != MD5Hex(out2) {
		t.Errorf("无损 json 非幂等：\nout1=%q\nout2=%q", out1, out2)
	}
}

// TestLosslessSerializationStable 同输入多次合并 md5 恒定（防 map 随机序）。
func TestLosslessSerializationStable(t *testing.T) {
	input := "a: 1\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 8\n"
	first, _ := MergeDataIDLossless(FormatYAML, []string{input})
	want := MD5Hex(first)
	for i := 0; i < 50; i++ {
		out, _ := MergeDataIDLossless(FormatYAML, []string{input})
		if MD5Hex(out) != want {
			t.Fatalf("第 %d 次无损序列化 md5 漂移", i)
		}
	}
}

// ---- 坏内容拒绝 ----

// TestLosslessInvalidContentRejected 坏内容解析报错（发布前据此拒绝、运行期据此回退）。
func TestLosslessInvalidContentRejected(t *testing.T) {
	if _, err := MergeDataIDLossless(FormatJSON, []string{`{"a": }`}); err == nil {
		t.Error("坏 json 应解析失败")
	}
	if _, err := MergeDataIDLossless(FormatYAML, []string{"a: [unterminated\n"}); err == nil {
		t.Error("坏 yaml 应解析失败")
	}
}

// ---- 无损 vs MergeDataID 语义相等交叉（无损只改表示、不改语义）----

// TestLosslessVsLossySemanticEquivalence 无损渲染再 parse 成类型模型，须与 MergeDataID 的类型模型逻辑相等。
func TestLosslessVsLossySemanticEquivalence(t *testing.T) {
	cases := []struct {
		name   string
		format string
		layers []string
	}{
		{"yaml 四层", FormatYAML, []string{"a: 1\nb: 2\nnest:\n  x: 1\n  y: 2\n", "b: 20\n", "nest:\n  y: 20\n", "c: 3\n"}},
		{"yaml null 删键", FormatYAML, []string{"a: 1\nb:\n  x: 1\n", "b:\n  y: 2\n", "a: null\nc: 3\n"}},
		{"yaml list 整替", FormatYAML, []string{"items:\n  - a\n  - b\n", "items:\n  - c\n"}},
		{"json 深合并", FormatJSON, []string{`{"a":1,"b":{"x":1}}`, `{"b":{"y":2}}`}},
		{"properties 覆盖", FormatProperties, []string{"k1=1\nk2=2\n", "k2=9\nk3=3\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lossy, err := MergeDataID(c.format, c.layers)
			if err != nil {
				t.Fatalf("有损合并失败: %v", err)
			}
			lossless, err := MergeDataIDLossless(c.format, c.layers)
			if err != nil {
				t.Fatalf("无损合并失败: %v", err)
			}
			lossyModel, _ := Parse(c.format, lossy)
			losslessModel, _ := Parse(c.format, lossless)
			if !reflect.DeepEqual(lossyModel, losslessModel) {
				t.Errorf("无损改变了语义：\n lossy=%#v\nlossless=%#v", lossyModel, losslessModel)
			}
		})
	}
}

// ---- provenance：无损 content + 逐键来源 ----

// TestLosslessProvenanceContentMatchesLossless WithProvenance 的 content 须与 MergeDataIDLossless 一致。
func TestLosslessProvenanceContentMatchesLossless(t *testing.T) {
	layers := []ProvLayer{
		{Scope: "global", Content: "zip: 007\nb:\n  x: 1\n"},
		{Scope: "zone", Content: "b:\n  y: 2\n"},
		{Scope: "server", Content: "c: 3\n"},
	}
	plain, _ := MergeDataIDLossless(FormatYAML, []string{layers[0].Content, layers[1].Content, layers[2].Content})
	content, sources, _, err := MergeDataIDLosslessWithProvenance(FormatYAML, layers)
	if err != nil {
		t.Fatalf("无损 provenance 合并失败: %v", err)
	}
	if content != plain {
		t.Errorf("无损 provenance content 与 MergeDataIDLossless 不一致：\nprov=%q\nplain=%q", content, plain)
	}
	if !strings.Contains(content, "zip: 007") {
		t.Errorf("无损 provenance content 应保真 007，实际：\n%s", content)
	}
	// 逐键来源正确
	if s := provScope(sources, "b", "x"); s != "global" {
		t.Errorf("b.x 来源应为 global，实际 %q", s)
	}
	if s := provScope(sources, "b", "y"); s != "zone" {
		t.Errorf("b.y 来源应为 zone，实际 %q", s)
	}
	if s := provScope(sources, "c"); s != "server" {
		t.Errorf("c 来源应为 server，实际 %q", s)
	}
}

// TestLosslessProvenanceSourcesEqualLossy 无损 provenance 的 sources/deletions 须与有损版逐一致（来源是语义、与表示无关）。
func TestLosslessProvenanceSourcesEqualLossy(t *testing.T) {
	layers := []ProvLayer{
		{Scope: "global", Content: "a: 1\nb:\n  x: 1\n"},
		{Scope: "zone", Content: "b:\n  y: 2\n"},
		{Scope: "server", Content: "a: null\nc: 3\n"},
	}
	_, lossySources, lossyDels, err := MergeDataIDWithProvenance(FormatYAML, layers)
	if err != nil {
		t.Fatalf("有损 provenance 失败: %v", err)
	}
	_, llSources, llDels, err := MergeDataIDLosslessWithProvenance(FormatYAML, layers)
	if err != nil {
		t.Fatalf("无损 provenance 失败: %v", err)
	}
	if !reflect.DeepEqual(lossySources, llSources) {
		t.Errorf("无损 sources 与有损不一致：\n lossy=%v\nlossless=%v", lossySources, llSources)
	}
	if !reflect.DeepEqual(lossyDels, llDels) {
		t.Errorf("无损 deletions 与有损不一致：\n lossy=%v\nlossless=%v", lossyDels, llDels)
	}
}

// provScope 在来源列表里按键路径查来源 scope（测试辅助）。
func provScope(list []KeyProvenance, path ...string) string {
	for _, p := range list {
		if reflect.DeepEqual(p.Path, path) {
			return p.Scope
		}
	}
	return ""
}

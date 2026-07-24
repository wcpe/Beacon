package merge

import (
	"reflect"
	"testing"
)

// TestDeepMergeScalarOverride 标量：高层覆盖低层。
func TestDeepMergeScalarOverride(t *testing.T) {
	base := map[string]any{"a": 1, "b": 2}
	override := map[string]any{"b": 3}
	got := DeepMerge(base, override)
	want := map[string]any{"a": 1, "b": 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("标量覆盖错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergeNestedMap map：递归深合并。
func TestDeepMergeNestedMap(t *testing.T) {
	base := map[string]any{"db": map[string]any{"host": "x", "port": 1}}
	override := map[string]any{"db": map[string]any{"port": 2}}
	got := DeepMerge(base, override)
	want := map[string]any{"db": map[string]any{"host": "x", "port": 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("map 深合并错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergeListReplace list：整体替换，不做元素级合并。
func TestDeepMergeListReplace(t *testing.T) {
	base := map[string]any{"l": []any{1, 2, 3}}
	override := map[string]any{"l": []any{9}}
	got := DeepMerge(base, override)
	want := map[string]any{"l": []any{9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list 整替错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergeNullDeletesKey 高层显式 null = 删除该键。
func TestDeepMergeNullDeletesKey(t *testing.T) {
	base := map[string]any{"a": 1, "b": 2}
	override := map[string]any{"b": nil}
	got := DeepMerge(base, override)
	want := map[string]any{"a": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("null 删键错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergeNullDeletesNestedKey 嵌套层内的 null 删键。
func TestDeepMergeNullDeletesNestedKey(t *testing.T) {
	base := map[string]any{"db": map[string]any{"host": "x", "port": 1}}
	override := map[string]any{"db": map[string]any{"port": nil}}
	got := DeepMerge(base, override)
	want := map[string]any{"db": map[string]any{"host": "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("嵌套 null 删键错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergeTypeMismatch 类型不一致：高层整体替换。
func TestDeepMergeTypeMismatch(t *testing.T) {
	// map 被标量替换
	got := DeepMerge(map[string]any{"a": map[string]any{"x": 1}}, map[string]any{"a": 5})
	want := map[string]any{"a": 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("map→标量替换错误：got=%v want=%v", got, want)
	}
	// 标量被 map 替换
	got2 := DeepMerge(map[string]any{"a": 5}, map[string]any{"a": map[string]any{"x": 1}})
	want2 := map[string]any{"a": map[string]any{"x": 1}}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("标量→map 替换错误：got=%v want=%v", got2, want2)
	}
}

// TestDeepMergeAddsNewKey 高层引入低层没有的键。
func TestDeepMergeAddsNewKey(t *testing.T) {
	got := DeepMerge(map[string]any{"a": 1}, map[string]any{"b": 2})
	want := map[string]any{"a": 1, "b": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("新增键错误：got=%v want=%v", got, want)
	}
}

// TestDeepMergePurity 合并不得修改入参。
func TestDeepMergePurity(t *testing.T) {
	base := map[string]any{"a": 1, "nest": map[string]any{"x": 1}}
	override := map[string]any{"a": 2, "nest": map[string]any{"y": 2}}
	baseCopy := map[string]any{"a": 1, "nest": map[string]any{"x": 1}}
	overrideCopy := map[string]any{"a": 2, "nest": map[string]any{"y": 2}}
	_ = DeepMerge(base, override)
	if !reflect.DeepEqual(base, baseCopy) {
		t.Errorf("DeepMerge 修改了 base：%v", base)
	}
	if !reflect.DeepEqual(override, overrideCopy) {
		t.Errorf("DeepMerge 修改了 override：%v", override)
	}
}

// TestMergeDataIDFourLayersYAML 四层 yaml 合并（global<group<zone<server）。
func TestMergeDataIDFourLayersYAML(t *testing.T) {
	global := "a: 1\nb: 2\nnest:\n  x: 1\n  y: 2\n"
	group := "b: 20\n"
	zone := "nest:\n  y: 20\n"
	server := "c: 3\n"
	out, err := MergeDataID(FormatYAML, []string{global, group, zone, server})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	parsed, err := Parse(FormatYAML, out)
	if err != nil {
		t.Fatalf("回解析失败: %v", err)
	}
	want := map[string]any{
		"a":    1,
		"b":    20,
		"nest": map[string]any{"x": 1, "y": 20},
		"c":    3,
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("四层合并结果错误：got=%v want=%v", parsed, want)
	}
}

// TestMergeDataIDJSON json 格式多层合并。
func TestMergeDataIDJSON(t *testing.T) {
	base := `{"area1":["zoneA","zoneB"],"max":10}`
	high := `{"max":20}`
	out, err := MergeDataID(FormatJSON, []string{base, high})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	parsed, _ := Parse(FormatJSON, out)
	want := map[string]any{"area1": []any{"zoneA", "zoneB"}, "max": float64(20)}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("json 合并错误：got=%v want=%v", parsed, want)
	}
}

// TestMergeDataIDProperties properties 按整 key 覆盖。
func TestMergeDataIDProperties(t *testing.T) {
	base := "# 注释\nurl=jdbc:base\npool=10\n"
	high := "pool=20\nextra=on\n"
	out, err := MergeDataID(FormatProperties, []string{base, high})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	want := "extra=on\npool=20\nurl=jdbc:base\n" // 键字典序
	if out != want {
		t.Errorf("properties 合并错误：\ngot=%q\nwant=%q", out, want)
	}
}

// TestMergeDataIDEmptyLayerSkipped 空层不贡献、不抹掉低层。
func TestMergeDataIDEmptyLayerSkipped(t *testing.T) {
	out, err := MergeDataID(FormatYAML, []string{"a: 1\n", "", "   \n"})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	parsed, _ := Parse(FormatYAML, out)
	want := map[string]any{"a": 1}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("空层应被跳过：got=%v want=%v", parsed, want)
	}
}

// TestMergeIdempotentKeyOrderYAML 不同键输入顺序得到相同 md5（序列化键序稳定）。
func TestMergeIdempotentKeyOrderYAML(t *testing.T) {
	out1, _ := MergeDataID(FormatYAML, []string{"b: 2\na: 1\nc: 3\n"})
	out2, _ := MergeDataID(FormatYAML, []string{"c: 3\na: 1\nb: 2\n"})
	if MD5Hex(out1) != MD5Hex(out2) {
		t.Errorf("yaml 序列化非幂等：\nout1=%q\nout2=%q", out1, out2)
	}
}

// TestMergeIdempotentKeyOrderJSON json 同理。
func TestMergeIdempotentKeyOrderJSON(t *testing.T) {
	out1, _ := MergeDataID(FormatJSON, []string{`{"b":2,"a":1,"c":3}`})
	out2, _ := MergeDataID(FormatJSON, []string{`{"c":3,"a":1,"b":2}`})
	if MD5Hex(out1) != MD5Hex(out2) {
		t.Errorf("json 序列化非幂等：\nout1=%q\nout2=%q", out1, out2)
	}
}

// TestMergeSerializationStable 多次序列化同一输入 md5 恒定（防 map 随机序）。
func TestMergeSerializationStable(t *testing.T) {
	input := "a: 1\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 8\n"
	first, _ := MergeDataID(FormatYAML, []string{input})
	want := MD5Hex(first)
	for i := 0; i < 50; i++ {
		out, _ := MergeDataID(FormatYAML, []string{input})
		if MD5Hex(out) != want {
			t.Fatalf("第 %d 次序列化 md5 漂移（map 随机序未消除）", i)
		}
	}
}

// TestMergeInvalidContentRejected 坏内容解析报错（发布前据此拒绝）。
func TestMergeInvalidContentRejected(t *testing.T) {
	if _, err := MergeDataID(FormatJSON, []string{`{"a": }`}); err == nil {
		t.Error("坏 json 应解析失败")
	}
	if _, err := MergeDataID(FormatYAML, []string{"a: 1\n  b: 2\n bad"}); err == nil {
		t.Error("坏 yaml 应解析失败")
	}
}

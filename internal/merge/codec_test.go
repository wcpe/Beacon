package merge

import "testing"

// TestSerializeJSONSortsKeys json 序列化按键字典序，稳定可比对。
func TestSerializeJSONSortsKeys(t *testing.T) {
	data := map[string]any{"b": 2, "a": 1, "c": 3}
	out, err := Serialize(FormatJSON, data)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n"
	if out != want {
		t.Errorf("json 键序不稳定：\ngot=%q\nwant=%q", out, want)
	}
}

// TestParseEmptyReturnsNil 空内容解析为 nil（该层不贡献）。
func TestParseEmptyReturnsNil(t *testing.T) {
	for _, f := range []string{FormatYAML, FormatJSON, FormatProperties} {
		v, err := Parse(f, "   \n")
		if err != nil {
			t.Errorf("%s 空内容解析报错: %v", f, err)
		}
		if v != nil {
			t.Errorf("%s 空内容应解析为 nil，实际 %v", f, v)
		}
	}
}

// TestParsePropertiesIgnoresComments properties 忽略注释与空行。
func TestParsePropertiesIgnoresComments(t *testing.T) {
	v, err := Parse(FormatProperties, "# 这是注释\n! 也是注释\n\nkey=value\nempty=\n")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("应解析为 map，实际 %T", v)
	}
	if m["key"] != "value" || m["empty"] != "" {
		t.Errorf("properties 解析错误：%v", m)
	}
	if len(m) != 2 {
		t.Errorf("注释/空行未被忽略：%v", m)
	}
}

// TestIsValidFormat 格式白名单。
func TestIsValidFormat(t *testing.T) {
	for _, f := range []string{FormatYAML, FormatJSON, FormatProperties} {
		if !IsValidFormat(f) {
			t.Errorf("%s 应为合法格式", f)
		}
	}
	if IsValidFormat("xml") {
		t.Error("xml 不应为合法格式")
	}
}

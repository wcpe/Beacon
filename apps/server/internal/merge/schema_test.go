package merge

import (
	"errors"
	"testing"
)

// TestValidateSchemaValid 合法键值文档放行（各格式）。
func TestValidateSchemaValid(t *testing.T) {
	cases := []struct {
		format  string
		content string
	}{
		{FormatYAML, "x: 1\ny:\n  z: 2\n"},
		{FormatJSON, `{"a":1,"b":{"c":2}}`},
		{FormatProperties, "key=value\nother=1\n"},
	}
	for _, c := range cases {
		if err := ValidateSchema(c.format, c.content); err != nil {
			t.Errorf("%s 合法内容不应报错，实际 %v", c.format, err)
		}
	}
}

// TestValidateSchemaEmptyPasses 空内容（该层不贡献）放行，不被结构校验拦。
func TestValidateSchemaEmptyPasses(t *testing.T) {
	for _, f := range []string{FormatYAML, FormatJSON, FormatProperties} {
		if err := ValidateSchema(f, "   \n"); err != nil {
			t.Errorf("%s 空内容应放行，实际 %v", f, err)
		}
	}
}

// TestValidateSchemaRejectsScalarRoot 顶层标量（非键值文档）被拦。
func TestValidateSchemaRejectsScalarRoot(t *testing.T) {
	cases := []struct {
		format  string
		content string
	}{
		{FormatYAML, "42\n"},
		{FormatYAML, "just text\n"},
		{FormatJSON, "42"},
		{FormatJSON, `"hello"`},
	}
	for _, c := range cases {
		err := ValidateSchema(c.format, c.content)
		if !errors.Is(err, ErrSchemaRootNotMap) {
			t.Errorf("%s 顶层标量应返回 ErrSchemaRootNotMap，实际 %v", c.format, err)
		}
	}
}

// TestValidateSchemaRejectsListRoot 顶层列表（非键值文档）被拦。
func TestValidateSchemaRejectsListRoot(t *testing.T) {
	cases := []struct {
		format  string
		content string
	}{
		{FormatYAML, "- a\n- b\n"},
		{FormatJSON, "[1,2,3]"},
	}
	for _, c := range cases {
		err := ValidateSchema(c.format, c.content)
		if !errors.Is(err, ErrSchemaRootNotMap) {
			t.Errorf("%s 顶层列表应返回 ErrSchemaRootNotMap，实际 %v", c.format, err)
		}
	}
}

// TestValidateSchemaRejectsEmptyKey 含空键 / 仅空白键被拦（含嵌套）。
func TestValidateSchemaRejectsEmptyKey(t *testing.T) {
	cases := []struct {
		name    string
		format  string
		content string
	}{
		{"顶层空键", FormatYAML, "a: 1\n\"\": 2\n"},
		{"顶层空白键", FormatYAML, "a: 1\n\"  \": 2\n"},
		{"嵌套空键", FormatYAML, "a:\n  \"\": 2\n"},
		{"json 空键", FormatJSON, `{"":1,"a":2}`},
	}
	for _, c := range cases {
		err := ValidateSchema(c.format, c.content)
		if !errors.Is(err, ErrSchemaEmptyKey) {
			t.Errorf("%s 应返回 ErrSchemaEmptyKey，实际 %v", c.name, err)
		}
	}
}

// TestValidateSchemaRejectsNonStringKey 顶层非字符串键（yaml `1: a`）被拦，且给准确语义错误（它本是 map）。
func TestValidateSchemaRejectsNonStringKey(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"整数键", "1: a\n"},
		{"日期键", "2024-01-01: x\n"},
		{"布尔键", "true: y\n"},
	}
	for _, c := range cases {
		err := ValidateSchema(FormatYAML, c.content)
		if !errors.Is(err, ErrSchemaNonStringKey) {
			t.Errorf("%s 应返回 ErrSchemaNonStringKey，实际 %v", c.name, err)
		}
	}
}

// TestValidateSchemaNestedListValueOk 键值文档里嵌套列表 / 标量值合法（只约束根与键，不约束值类型）。
func TestValidateSchemaNestedListValueOk(t *testing.T) {
	content := "servers:\n  - one\n  - two\nport: 8080\n"
	if err := ValidateSchema(FormatYAML, content); err != nil {
		t.Errorf("嵌套列表值应合法，实际 %v", err)
	}
}

package service

import (
	"errors"
	"testing"

	"beacon/internal/apperr"
	"beacon/internal/merge"
)

// TestValidateContentSchema 校验发布前内容校验：合法通过 / 各类非法被拦且错误码正确。
// 纯逻辑、无 DB 依赖（白盒单测直接调内部 validateContent）。
func TestValidateContentSchema(t *testing.T) {
	cases := []struct {
		name    string
		format  string
		content string
		wantErr *apperr.Error
	}{
		{"合法 yaml 键值文档", merge.FormatYAML, "x: 1\ny:\n  z: 2\n", nil},
		{"合法 json 键值文档", merge.FormatJSON, `{"a":1}`, nil},
		{"合法 properties", merge.FormatProperties, "key=value\n", nil},
		{"空内容放行", merge.FormatYAML, "  \n", nil},
		{"格式非法", "xml", "x: 1\n", apperr.ErrInvalidParam},
		{"无法解析的坏 yaml", merge.FormatYAML, "a: 1\n  b: 2\n bad", apperr.ErrContentInvalid},
		{"顶层标量被拦", merge.FormatYAML, "42\n", apperr.ErrContentSchemaInvalid},
		{"顶层字符串被拦", merge.FormatJSON, `"hi"`, apperr.ErrContentSchemaInvalid},
		{"顶层列表被拦", merge.FormatYAML, "- a\n- b\n", apperr.ErrContentSchemaInvalid},
		{"json 顶层数组被拦", merge.FormatJSON, "[1,2]", apperr.ErrContentSchemaInvalid},
		{"空键被拦", merge.FormatYAML, "a: 1\n\"\": 2\n", apperr.ErrContentSchemaInvalid},
		{"嵌套空键被拦", merge.FormatJSON, `{"a":{"":1}}`, apperr.ErrContentSchemaInvalid},
	}
	for _, c := range cases {
		err := validateContent(c.format, c.content)
		if c.wantErr == nil {
			if err != nil {
				t.Errorf("%s：应通过，实际 %v", c.name, err)
			}
			continue
		}
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s：应返回 %v，实际 %v", c.name, c.wantErr, err)
		}
	}
}

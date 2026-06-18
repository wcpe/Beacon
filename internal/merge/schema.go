package merge

import "errors"

// schema 校验的语义哨兵错误（由 service 层映射为对外 apperr）。
var (
	// ErrSchemaRootNotMap 非空配置的顶层不是键值映射（是裸标量或列表）。
	// 这类内容在 scope 覆盖链深合并里会整体替换其它层，且 agent 端按"键→值"加载会异常。
	ErrSchemaRootNotMap = errors.New("配置顶层必须是键值映射，不能是标量或列表")
	// ErrSchemaEmptyKey 配置中存在空键或仅空白的键（含嵌套层）。
	ErrSchemaEmptyKey = errors.New("配置存在空键或仅空白的键")
)

// ValidateSchema 在发布前对配置做结构与类型校验（FR-27）。
// 规则（刻意保守，仅约束"是一篇合法的键值配置文档"，不臆造业务字段规则）：
//   - 空内容（该层不贡献）放行；
//   - 非空内容顶层必须解析为键值映射（map），否则 ErrSchemaRootNotMap；
//   - 所有映射键（递归进嵌套 map）必须为非空字符串，否则 ErrSchemaEmptyKey。
//
// 仅约束根类型与键，不约束值类型（值可以是标量 / 列表 / 嵌套 map）。
func ValidateSchema(format, content string) error {
	parsed, err := Parse(format, content)
	if err != nil {
		// 解析层错误由调用方（service.validateContent）先行处理；此处防御性返回。
		return err
	}
	if parsed == nil {
		return nil // 空层不贡献，合法
	}
	m, ok := parsed.(map[string]any)
	if !ok {
		return ErrSchemaRootNotMap
	}
	return validateKeys(m)
}

// validateKeys 递归校验 map 的所有键非空（去空白后不为空）。
func validateKeys(m map[string]any) error {
	for k, v := range m {
		if isBlank(k) {
			return ErrSchemaEmptyKey
		}
		if child, ok := v.(map[string]any); ok {
			if err := validateKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// isBlank 判断字符串去除首尾空白后是否为空。
func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

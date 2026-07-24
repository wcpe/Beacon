package configschema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// flatSchema 是 properties 格式的扁平校验定义（spec §4.4）：
// 按扁平键名匹配 schema 顶层 properties 定义，值一律按 string 校验 pattern / enum；
// required 完整性只在 namespace 基线层强制。
type flatSchema struct {
	required []string
	props    map[string]flatProp
}

// flatProp 是单个扁平键的校验规则。
type flatProp struct {
	pattern    *regexp.Regexp
	patternRaw string
	enum       []string
	hasEnum    bool
}

// parseFlatSchema 从 schema 文本提取扁平校验定义（pattern 预编译，非法即拒）。
func parseFlatSchema(schemaJSON string) (*flatSchema, error) {
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &doc); err != nil {
		return nil, fmt.Errorf("schema 不是合法 JSON: %w", err)
	}
	out := &flatSchema{required: doc.Required, props: make(map[string]flatProp, len(doc.Properties))}
	for key, raw := range doc.Properties {
		prop, err := parseFlatProp(key, raw)
		if err != nil {
			return nil, err
		}
		out.props[key] = prop
	}
	return out, nil
}

// parseFlatProp 提取单键的 pattern / enum 规则（enum 值统一字符串化，与 properties 值口径一致）。
func parseFlatProp(key string, raw json.RawMessage) (flatProp, error) {
	var def struct {
		Pattern *string `json:"pattern"`
		Enum    []any   `json:"enum"`
	}
	if err := json.Unmarshal(raw, &def); err != nil {
		return flatProp{}, fmt.Errorf("schema 属性 %s 定义不合法: %w", key, err)
	}
	prop := flatProp{}
	if def.Pattern != nil {
		re, err := regexp.Compile(*def.Pattern)
		if err != nil {
			return flatProp{}, fmt.Errorf("schema 属性 %s 的 pattern 不是合法正则: %w", key, err)
		}
		prop.pattern = re
		prop.patternRaw = *def.Pattern
	}
	if def.Enum != nil {
		prop.hasEnum = true
		prop.enum = make([]string, 0, len(def.Enum))
		for _, v := range def.Enum {
			prop.enum = append(prop.enum, fmt.Sprint(v))
		}
	}
	return prop, nil
}

// validate 对解析后的 properties 内容（扁平 map，值为字符串）逐键校验。
func (f *flatSchema) validate(parsed any, baseline bool) []Violation {
	m, ok := parsed.(map[string]any)
	if !ok {
		return []Violation{{Path: rootPath, Message: "properties 内容应为键值对"}}
	}
	var out []Violation
	for key, value := range m {
		prop, defined := f.props[key]
		if !defined {
			continue // 未在 schema 定义的键放行（部分校验只管声明过的键）
		}
		out = append(out, prop.check(key, fmt.Sprint(value))...)
	}
	if baseline {
		out = append(out, f.checkRequired(m)...)
	}
	sortViolations(out)
	return out
}

// check 校验单键字符串值的 pattern / enum。
func (p flatProp) check(key, value string) []Violation {
	var out []Violation
	if p.pattern != nil && !p.pattern.MatchString(value) {
		out = append(out, Violation{Path: key, Message: fmt.Sprintf("'%s' does not match pattern '%s'", value, p.patternRaw)})
	}
	if p.hasEnum && !contains(p.enum, value) {
		out = append(out, Violation{Path: key, Message: fmt.Sprintf("value must be one of '%s'", strings.Join(p.enum, "', '"))})
	}
	return out
}

// checkRequired 校验基线层 required 键完整性。
func (f *flatSchema) checkRequired(m map[string]any) []Violation {
	var out []Violation
	for _, key := range f.required {
		if _, ok := m[key]; !ok {
			out = append(out, Violation{Path: rootPath, Message: fmt.Sprintf("missing property '%s'", key)})
		}
	}
	return out
}

// contains 判断字符串切片是否含目标值。
func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// sortViolations 按 (path, message) 稳定排序（扁平校验遍历 map 顺序随机，输出须确定）。
func sortViolations(list []Violation) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Path != list[j].Path {
			return list[i].Path < list[j].Path
		}
		return list[i].Message < list[j].Message
	})
}

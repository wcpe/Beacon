// Package configschema 封装配置中心 V2 的文件级 JSON Schema 校验（FR-161，规格 v2-config-center.md §4.4）。
// 基于 santhosh-tekuri/jsonschema/v6（Draft 2020-12），实现部分校验语义：
//   - 层内容是增量，只校验提交内容中出现的键（library 对 object 天然只校验出现的键）；
//   - required 完整性只对 namespace 基线层强制——非基线层用递归剥除 required 的副本校验；
//   - 显式 null 值键（删键指令）在校验前从内容副本递归剥除，跳过类型校验；
//   - properties 格式按扁平键名对 schema 的 properties 定义逐键校验（值按 string 校验 pattern / enum）。
//
// 全部为无副作用纯函数式封装（编译一次、并发只读），不依赖 model / repository。
package configschema

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/wcpe/Beacon/apps/server/internal/merge"
)

// Violation 是一条 schema 违例（对齐契约 {path, message}；path 为点分键路径，根为 "(root)"）。
type Violation struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// rootPath 是根级违例的路径占位（对齐 devmock validate 端点形状）。
const rootPath = "(root)"

// violationPrinter 渲染 library 违例文案（library 内置文案为英文，路径与关键字语义自明）。
var violationPrinter = message.NewPrinter(language.English)

// Validator 是某配置文件已编译 schema 的校验器（并发只读安全）。
type Validator struct {
	format string
	// full 是完整 schema（namespace 基线层用，含 required）
	full *jsonschema.Schema
	// partial 是递归剥除 required 的部分校验 schema（非基线层用）
	partial *jsonschema.Schema
	// flat 是 properties 格式的扁平键校验定义（其余格式为 nil）
	flat *flatSchema
}

// Compile 编译文件级 schema 文本；schemaJSON 本身非法（非合法 JSON / 不符合 Draft 2020-12 元 schema /
// pattern 非法正则）时返回错误，供保存 schema 时先行拒绝（spec §4.4）。
func Compile(format, schemaJSON string) (*Validator, error) {
	full, err := compileDoc(schemaJSON, nil)
	if err != nil {
		return nil, err
	}
	partial, err := compileDoc(schemaJSON, stripRequired)
	if err != nil {
		return nil, err
	}
	v := &Validator{format: format, full: full, partial: partial}
	if format == merge.FormatProperties {
		flat, err := parseFlatSchema(schemaJSON)
		if err != nil {
			return nil, err
		}
		v.flat = flat
	}
	return v, nil
}

// compileDoc 解析 + （可选变换后）编译 schema 文档；编译内含 Draft 2020-12 元 schema 校验。
func compileDoc(schemaJSON string, transform func(any)) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("schema 不是合法 JSON: %w", err)
	}
	if transform != nil {
		transform(doc)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("beacon-config-schema.json", doc); err != nil {
		return nil, fmt.Errorf("schema 不合法: %w", err)
	}
	schema, err := compiler.Compile("beacon-config-schema.json")
	if err != nil {
		return nil, fmt.Errorf("schema 不是合法 JSON Schema: %w", err)
	}
	return schema, nil
}

// Validate 校验解析后的内容（merge.Parse 产物）；baseline=true 表示 namespace 基线层（强制 required）。
// 返回违例列表，空切片 = 通过。
func (v *Validator) Validate(parsed any, baseline bool) []Violation {
	if parsed == nil {
		return nil // 空层不贡献，无可校验
	}
	if v.flat != nil {
		return v.flat.validate(parsed, baseline)
	}
	instance, err := normalizeInstance(stripNulls(parsed))
	if err != nil {
		return []Violation{{Path: rootPath, Message: err.Error()}}
	}
	schema := v.partial
	if baseline {
		schema = v.full
	}
	verr := schema.Validate(instance)
	if verr == nil {
		return nil
	}
	var out []Violation
	if ve, ok := verr.(*jsonschema.ValidationError); ok {
		collectViolations(ve, &out)
		return out
	}
	return []Violation{{Path: rootPath, Message: verr.Error()}}
}

// normalizeInstance 把解析产物经 JSON 往返归一为 library 的规范实例类型（数字统一 json.Number）。
func normalizeInstance(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("内容无法归一为 JSON 实例: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("内容无法归一为 JSON 实例: %w", err)
	}
	return instance, nil
}

// collectViolations 深度遍历违例树，收集叶子违例为 {path, message} 列表。
func collectViolations(e *jsonschema.ValidationError, out *[]Violation) {
	if len(e.Causes) == 0 {
		path := rootPath
		if len(e.InstanceLocation) > 0 {
			path = strings.Join(e.InstanceLocation, ".")
		}
		*out = append(*out, Violation{Path: path, Message: e.ErrorKind.LocalizedString(violationPrinter)})
		return
	}
	for _, cause := range e.Causes {
		collectViolations(cause, out)
	}
}

// stripRequired 递归剥除 schema 文档中的 required 关键字（部分校验语义，spec §4.4）。
// 只在 schema 位置递归（properties 的值 / items / additionalProperties），
// 不会误伤名为 required 的属性定义（properties 的键不是 schema）。
func stripRequired(doc any) {
	m, ok := doc.(map[string]any)
	if !ok {
		return
	}
	delete(m, "required")
	if props, ok := m["properties"].(map[string]any); ok {
		for _, sub := range props {
			stripRequired(sub)
		}
	}
	stripRequired(m["items"])
	stripRequired(m["additionalProperties"])
}

// stripNulls 深拷贝内容并递归剥除 map 中显式 null 值键（删键指令跳过类型校验，spec §4.4）。
// list 内的 null 是普通元素值，原样保留。
func stripNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = stripNulls(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = stripNulls(item)
		}
		return out
	default:
		return v
	}
}

// Package merge 实现 scope 覆盖链的键级深合并、yaml/json/properties 编解码与 md5 摘要。
// 全部为无副作用纯函数（不依赖 model/repository），便于穷举单测。
package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 配置内容格式常量。
const (
	FormatYAML       = "yaml"
	FormatJSON       = "json"
	FormatProperties = "properties"
)

// IsValidFormat 校验格式是否受支持。
func IsValidFormat(format string) bool {
	switch format {
	case FormatYAML, FormatJSON, FormatProperties:
		return true
	default:
		return false
	}
}

// Parse 把配置文本解析为通用结构（map[string]any / []any / 标量）。
// 空内容返回 (nil, nil)，表示该层不贡献；解析失败返回错误（发布前据此拒绝坏内容）。
func Parse(format, content string) (any, error) {
	switch format {
	case FormatYAML:
		if strings.TrimSpace(content) == "" {
			return nil, nil
		}
		var v any
		if err := yaml.Unmarshal([]byte(content), &v); err != nil {
			return nil, fmt.Errorf("yaml 解析失败: %w", err)
		}
		return v, nil
	case FormatJSON:
		if strings.TrimSpace(content) == "" {
			return nil, nil
		}
		var v any
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return nil, fmt.Errorf("json 解析失败: %w", err)
		}
		return v, nil
	case FormatProperties:
		return parseProperties(content), nil
	default:
		return nil, fmt.Errorf("不支持的配置格式: %s", format)
	}
}

// Serialize 把通用结构序列化为指定格式文本，键序固定（保证相同输入恒得相同 md5）。
func Serialize(format string, data any) (string, error) {
	switch format {
	case FormatYAML:
		if data == nil {
			return "", nil
		}
		out, err := yaml.Marshal(data)
		if err != nil {
			return "", fmt.Errorf("yaml 序列化失败: %w", err)
		}
		return string(out), nil
	case FormatJSON:
		if data == nil {
			return "", nil
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // 配置内容不做 HTML 转义，保持原样
		enc.SetIndent("", "  ")
		// json 对 map 键自动按字典序输出，键序稳定
		if err := enc.Encode(data); err != nil {
			return "", fmt.Errorf("json 序列化失败: %w", err)
		}
		return buf.String(), nil
	case FormatProperties:
		return serializeProperties(data)
	default:
		return "", fmt.Errorf("不支持的配置格式: %s", format)
	}
}

// parseProperties 解析 .properties 文本为扁平键值 map（值为字符串）。
// 忽略空行与 # / ! 注释行；按首个 '=' 切分键值。空内容返回 nil。
func parseProperties(content string) any {
	result := map[string]any{}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			result[line] = "" // 无分隔符的行视作空值键
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}
		result[key] = strings.TrimSpace(line[idx+1:])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// serializeProperties 把扁平键值 map 按键字典序序列化为 key=value 文本。
func serializeProperties(data any) (string, error) {
	if data == nil {
		return "", nil
	}
	m, ok := data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("properties 内容应为键值对，实际为 %T", data)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(fmt.Sprint(m[k]))
		b.WriteString("\n")
	}
	return b.String(), nil
}

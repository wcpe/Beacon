package merge

import (
	"sort"
	"strings"
)

// propEntry 是 properties 的一条 key 记录：原值文本 + 前置注释行（保真用）。
type propEntry struct {
	value    string   // 原始值文本（不解析、不归一化）
	comments []string // 紧贴该 key 上方的连续注释行（含 # / ! 前缀，原样保留）
}

// mergePropertiesLossless 行式无损深合并多层 properties：
// 按 key 覆盖（override 替值）、高层值为 null 删键、键字典序输出、前置注释随键保留。
func mergePropertiesLossless(layers []string) (string, error) {
	merged := map[string]propEntry{}
	for _, content := range layers {
		for k, e := range parsePropertiesEntries(content) {
			if e.value == propNullMarker {
				delete(merged, k) // 高层显式 null = 删键
				continue
			}
			// 高层覆盖值；若高层未带前置注释，沿用低层已有注释（避免覆盖值时丢说明）。
			if len(e.comments) == 0 {
				if prev, ok := merged[k]; ok {
					e.comments = prev.comments
				}
			}
			merged[k] = e
		}
	}
	if len(merged) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		e := merged[k]
		for _, c := range e.comments {
			b.WriteString(c)
			b.WriteString("\n")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(e.value)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// propNullMarker 是 properties 表达「删键」的高层值（与 yaml/json 的显式 null 对齐）。
// properties 值为字符串 "null" 时视作删键（与 MergeDataID 走 map[string]any 经 DeepMerge 的 null 语义无法直接对应，
// 故 properties 删键沿用「值字面量为 null」约定；与有损版语义相等性由交叉测试保证）。
const propNullMarker = "null"

// parsePropertiesEntries 解析单层 properties 为「key → 原值 + 前置注释」的有序无关映射。
// 收集紧贴 key 上方的连续注释行（# / !）作为该 key 的前置注释；空行打断注释归属。
func parsePropertiesEntries(content string) map[string]propEntry {
	result := map[string]propEntry{}
	var pending []string // 暂存的连续注释行，遇到 key 时归属给它
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			pending = nil // 空行打断注释与 key 的贴附关系
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			pending = append(pending, line) // 原样保留注释行（含缩进与前缀）
			continue
		}
		idx := strings.Index(trimmed, "=")
		var key, value string
		if idx < 0 {
			key = trimmed // 无分隔符的行视作空值键
			value = ""
		} else {
			key = strings.TrimSpace(trimmed[:idx])
			value = strings.TrimSpace(trimmed[idx+1:])
		}
		if key == "" {
			pending = nil
			continue
		}
		comments := pending
		pending = nil
		result[key] = propEntry{value: value, comments: comments}
	}
	return result
}

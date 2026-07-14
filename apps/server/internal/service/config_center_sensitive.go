package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
)

// ConfigMaskedPlaceholder 是敏感值读出口的统一占位符（FR-161，spec §4.7）。
// 所有管理面读端点命中敏感路径的叶子值一律替换为它；保存时提交该占位符 = 回填上一版本明文。
const ConfigMaskedPlaceholder = "__BEACON_MASKED__"

// pathSlot 是敏感路径在解析结构中命中的一个槽位（父 map + 键），供脱敏 / 回填原地读写。
type pathSlot struct {
	parent map[string]any
	key    string
}

// sensitivePathSlots 枚举精确键路径在 m 中命中的全部槽位。
// 同时支持两种形态并允许并存（yaml 里 database.password 既可能是嵌套键、也可能是含 '.' 的扁平键）：
//   - 逐段下钻嵌套 map（database → password）；
//   - 长前缀合并的扁平键（"database.password" 直接作键）。
func sensitivePathSlots(m map[string]any, segments []string) []pathSlot {
	var out []pathSlot
	for i := len(segments); i >= 1; i-- {
		key := strings.Join(segments[:i], ".")
		v, ok := m[key]
		if !ok {
			continue
		}
		if i == len(segments) {
			out = append(out, pathSlot{parent: m, key: key})
			continue
		}
		if child, ok := v.(map[string]any); ok {
			out = append(out, sensitivePathSlots(child, segments[i:])...)
		}
	}
	return out
}

// deepCopyValue 深拷贝解析后的配置结构（map / list 递归，标量原样）。
// 脱敏须在拷贝上进行，绝不污染用于哈希 / 落库的明文结构。
func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopyValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}

// maskSensitiveContent 返回把全部敏感路径叶子值替换为占位符的深拷贝（读出口统一脱敏，spec §4.7）。
// 命中值为 map（路径指向子树而非叶子）或显式 null 时不替换。
func maskSensitiveContent(parsed any, paths []string) any {
	root, ok := parsed.(map[string]any)
	if !ok || len(paths) == 0 {
		return parsed
	}
	copied := deepCopyValue(root).(map[string]any)
	for _, path := range paths {
		for _, slot := range sensitivePathSlots(copied, strings.Split(path, ".")) {
			value := slot.parent[slot.key]
			if value == nil {
				continue // 显式 null 是删键指令，无叶子值可脱敏
			}
			if _, isMap := value.(map[string]any); isMap {
				continue // 精确路径只标叶子，命中子树不整体替换
			}
			slot.parent[slot.key] = ConfigMaskedPlaceholder
		}
	}
	return copied
}

// maskContentText 对已归一化的配置文本做敏感脱敏后再序列化输出（版本详情 / 有效预览等读出口）。
// 解析失败时报错而非原样返回——宁可 500 也绝不把可能含明文的内容裸奔出去（fail-closed）。
func maskContentText(format, content string, paths []string) (string, error) {
	if len(paths) == 0 || content == "" {
		return content, nil
	}
	parsed, err := merge.Parse(format, content)
	if err != nil {
		return "", fmt.Errorf("脱敏前解析配置内容失败: %w", err)
	}
	if parsed == nil {
		return content, nil
	}
	return merge.Serialize(format, maskSensitiveContent(parsed, paths))
}

// backfillSensitivePlaceholders 保存前的敏感占位符回填（spec §4.7）：
// 提交内容中敏感键的值等于占位符时，就地回填该链 head 明文中同路径的值；
// 无上一版本可回填（新增键 / 链空 / head 为撤销 / head 同为占位符）→ CONFIG_SENSITIVE_PLACEHOLDER_INVALID。
// submitted 由保存流程独占，允许就地修改；headParsed 为 head 内容解析产物（链空传 nil）。
func backfillSensitivePlaceholders(submitted map[string]any, headParsed any, paths []string) error {
	headRoot, _ := headParsed.(map[string]any)
	for _, path := range paths {
		segments := strings.Split(path, ".")
		for _, slot := range sensitivePathSlots(submitted, segments) {
			if slot.parent[slot.key] != any(ConfigMaskedPlaceholder) {
				continue // 提交了非占位符新值：直接采用
			}
			plaintext, ok := headPlaintextAt(headRoot, segments)
			if !ok {
				return apperr.New(http.StatusBadRequest, "CONFIG_SENSITIVE_PLACEHOLDER_INVALID",
					fmt.Sprintf("敏感键 %s 无上一版本明文可回填，请直接填写新值", path))
			}
			slot.parent[slot.key] = plaintext
		}
	}
	return nil
}

// headPlaintextAt 从 head 解析结构取指定路径的可回填明文；缺失 / null / 子树 / 仍是占位符均视为不可回填。
func headPlaintextAt(headRoot map[string]any, segments []string) (any, bool) {
	if headRoot == nil {
		return nil, false
	}
	for _, slot := range sensitivePathSlots(headRoot, segments) {
		value := slot.parent[slot.key]
		if value == nil || value == any(ConfigMaskedPlaceholder) {
			continue
		}
		if _, isMap := value.(map[string]any); isMap {
			continue
		}
		return value, true
	}
	return nil, false
}

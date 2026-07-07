package merge

import (
	"sort"
	"strings"
)

// 内部路径分隔符：用不可见控制字符，避免与 yaml/json/properties 的键内字符（含 '.'）冲突。
// 对外输出时拆回 []string，由上层按需拼成可读路径。
const provPathSep = "\x1f"

// KeyProvenance 表示有效配置中某个叶子键的来源层（或被某层删除）。
// Path 为嵌套键路径（扁平 properties 即单段，可能含 '.'）；Scope 为 scope 覆盖层 global/group/zone/server。
type KeyProvenance struct {
	Path  []string
	Scope string
}

// ProvLayer 是参与合并的一层（须按覆盖链低→高顺序传入）。
type ProvLayer struct {
	Scope   string
	Content string
}

// MergeDataIDWithProvenance 与 MergeDataID 等价地把多层内容深合并，
// 额外返回逐叶子键的最终来源层（sources）与被显式 null 删除且最终确实不存在的键（deletions）。
// 仅供 admin 只读有效配置预览；不改动 DeepMerge/MergeDataID 这条 agent 热路径。
// 合并结果（content）须与 MergeDataID 逐一致，由 provenance_test 的一致性交叉测试守护，防两份实现漂移。
func MergeDataIDWithProvenance(format string, layers []ProvLayer) (content string, sources []KeyProvenance, deletions []KeyProvenance, err error) {
	type input struct {
		scope string
		value any
	}
	parsed := make([]input, 0, len(layers))
	for _, l := range layers {
		v, perr := Parse(format, l.Content)
		if perr != nil {
			return "", nil, nil, perr
		}
		if v == nil {
			continue // 空层不贡献
		}
		parsed = append(parsed, input{scope: l.Scope, value: v})
	}
	if len(parsed) == 0 {
		return "", nil, nil, nil
	}

	srcMap := map[string]string{} // 编码路径 → 最终来源 scope
	var dels []KeyProvenance      // 删除轨迹（可能含被高层重新添加的，最后按最终态过滤）

	var merged any
	for i, in := range parsed {
		if i == 0 {
			merged = in.value
			recordLeaves(in.value, nil, in.scope, srcMap)
			continue
		}
		merged = mergeProv(merged, in.value, nil, in.scope, srcMap, &dels)
	}

	out, serr := Serialize(format, merged)
	if serr != nil {
		return "", nil, nil, serr
	}

	sources = sortedProvenance(srcMap)

	// deletions：只保留最终态确实不存在的键（被高层重新添加的不算减量），同路径取最后（最高层）那次。
	seen := map[string]string{}
	for _, d := range dels {
		ek := strings.Join(d.Path, provPathSep)
		if pathStillPresent(srcMap, ek) {
			continue // 该路径或其某个子键最终又出现 → 非减量
		}
		seen[ek] = d.Scope // 后写覆盖更高层
	}
	deletions = sortedProvenance(seen)
	return out, sources, deletions, nil
}

// pathStillPresent 判断某删除路径在最终来源里是否仍存在：精确命中，或存在以它为前缀的子键。
// 用于过滤"删整子树后高层又重加部分子键"——此时父路径不应再算作减量删除（否则与 sources 自相矛盾）。
func pathStillPresent(srcMap map[string]string, encPath string) bool {
	if _, ok := srcMap[encPath]; ok {
		return true
	}
	for k := range srcMap {
		if strings.HasPrefix(k, encPath+provPathSep) {
			return true
		}
	}
	return false
}

// mergeProv 是 DeepMerge 的 provenance 记录版：合并规则与 DeepMerge 完全一致，
// 同时在 srcMap 记录叶子来源、在 dels 记录被 null 删除的键。
func mergeProv(base, override any, path []string, scope string, srcMap map[string]string, dels *[]KeyProvenance) any {
	baseMap, baseIsMap := base.(map[string]any)
	overrideMap, overrideIsMap := override.(map[string]any)
	if !baseIsMap || !overrideIsMap {
		// 标量覆盖 / list 整替 / 类型不一致 → 高层整体替换该子树
		removeLeaves(srcMap, path)
		recordLeaves(override, path, scope, srcMap)
		return override
	}

	result := make(map[string]any, len(baseMap))
	for k, v := range baseMap {
		result[k] = v
	}
	for k, ov := range overrideMap {
		child := appendSeg(path, k)
		if ov == nil {
			if _, exists := result[k]; exists {
				delete(result, k) // 显式 null = 删键
				removeLeaves(srcMap, child)
				*dels = append(*dels, KeyProvenance{Path: child, Scope: scope})
			}
			continue
		}
		if bv, exists := result[k]; exists {
			result[k] = mergeProv(bv, ov, child, scope, srcMap, dels)
		} else {
			result[k] = ov
			recordLeaves(ov, child, scope, srcMap)
		}
	}
	return result
}

// recordLeaves 把 v 的每个叶子键路径记成来源 scope（map 递归到底，非 map 即叶子）。
func recordLeaves(v any, path []string, scope string, srcMap map[string]string) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		for k, cv := range m {
			recordLeaves(cv, appendSeg(path, k), scope, srcMap)
		}
		return
	}
	srcMap[strings.Join(path, provPathSep)] = scope
}

// removeLeaves 清除 path 子树下的全部来源记录（子树被替换/删除时调用）。
func removeLeaves(srcMap map[string]string, path []string) {
	prefix := strings.Join(path, provPathSep)
	for k := range srcMap {
		if k == prefix || strings.HasPrefix(k, prefix+provPathSep) {
			delete(srcMap, k)
		}
	}
}

// appendSeg 返回 path + seg 的新切片（避免共享底层数组导致路径串扰）。
func appendSeg(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

// sortedProvenance 把"编码路径→scope"映射按路径字典序拆成稳定有序的 KeyProvenance 列表。
func sortedProvenance(m map[string]string) []KeyProvenance {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KeyProvenance, 0, len(keys))
	for _, k := range keys {
		var segs []string
		if k != "" {
			segs = strings.Split(k, provPathSep)
		}
		out = append(out, KeyProvenance{Path: segs, Scope: m[k]})
	}
	return out
}

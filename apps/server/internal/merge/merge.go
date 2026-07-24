package merge

// DeepMerge 返回 base 被 override 覆盖后的结果（不修改入参，纯函数）。
// 规则（高层 override 覆盖低层 base）：
//   - 双方都是 map → 逐键深合并；
//   - override 中值为 nil 的键 = 删除该键（高层显式 null 抹掉低层默认）；
//   - 其余情形（标量覆盖、list 整体替换、类型不一致）→ override 整体替换 base。
func DeepMerge(base, override any) any {
	baseMap, baseIsMap := base.(map[string]any)
	overrideMap, overrideIsMap := override.(map[string]any)
	if !baseIsMap || !overrideIsMap {
		// 标量覆盖 / list 整替 / 类型不一致 → 高层胜
		return override
	}

	result := make(map[string]any, len(baseMap))
	for k, v := range baseMap {
		result[k] = v
	}
	for k, ov := range overrideMap {
		if ov == nil {
			delete(result, k) // 显式 null = 删键
			continue
		}
		if bv, exists := result[k]; exists {
			result[k] = DeepMerge(bv, ov)
		} else {
			result[k] = ov
		}
	}
	return result
}

// MergeDataID 把同一 dataId 的多层内容（按优先级低→高排列）解析、深合并并序列化为有效文本。
// 空层（解析为 nil）不贡献；全部为空时返回空串。
func MergeDataID(format string, layeredLowToHigh []string) (string, error) {
	var merged any
	started := false
	for _, content := range layeredLowToHigh {
		parsed, err := Parse(format, content)
		if err != nil {
			return "", err
		}
		if parsed == nil {
			continue
		}
		if !started {
			merged = parsed
			started = true
			continue
		}
		merged = DeepMerge(merged, parsed)
	}
	if !started {
		return "", nil
	}
	return Serialize(format, merged)
}

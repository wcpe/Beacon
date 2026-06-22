package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergeDataIDLossless 把同一文件的多层内容（按优先级低→高排列）无损深合并为渲染文本。
//
// 与 MergeDataID 的唯一差别是「保真度」：合并语义完全一致（标量覆盖 / map 深合并 /
// list 整体替换 / 高层显式 null 删键 / 确定性键序），但**不归一化叶子标量值、保留注释**：
//   - YAML 走 yaml.Node 节点级合并，叶子保留原文 token 与 Tag/Style，注释随节点搬。
//   - JSON 走 json.Number 解析，大整数 / 浮点按原文 emit、不失精度。
//   - properties 行式保留每个 key 的前置注释与原值文本。
//
// 供 internal/filetree（通道B）调用；配置中心（通道A）仍用 MergeDataID（见 ADR-0034）。
// 空层（解析为空贡献）不参与；全部为空时返回空串。
func MergeDataIDLossless(format string, layeredLowToHigh []string) (string, error) {
	switch format {
	case FormatYAML:
		return mergeYAMLLossless(layeredLowToHigh)
	case FormatJSON:
		return mergeJSONLossless(layeredLowToHigh)
	case FormatProperties:
		return mergePropertiesLossless(layeredLowToHigh)
	default:
		return "", fmt.Errorf("不支持的配置格式: %s", format)
	}
}

// ---- YAML：yaml.Node 节点级无损深合并 ----

// mergeYAMLLossless 用 yaml.Node 递归深合并多层 yaml，保叶子原文 token 与注释。
func mergeYAMLLossless(layers []string) (string, error) {
	var merged *yaml.Node // 内容根节点（MappingNode/ScalarNode/SequenceNode），不含 DocumentNode 包裹
	for _, content := range layers {
		node, err := parseYAMLContentNode(content)
		if err != nil {
			return "", err
		}
		if node == nil {
			continue // 空 / 纯注释 / 纯空白层不贡献
		}
		if merged == nil {
			merged = node
			continue
		}
		merged = mergeYAMLNode(merged, node)
	}
	if merged == nil {
		return "", nil
	}
	// 即便只有单层贡献（未走合并），也递归规整键序，保证 md5 幂等与确定性输出。
	return marshalYAMLNode(canonicalizeYAMLNode(merged))
}

// canonicalizeYAMLNode 递归把 MappingNode 的 key/value 对按 key 字典序重排（注释随节点搬），
// 保证相同语义内容恒得相同序列化（md5 幂等）；非 Mapping 节点原样返回（保叶子 token 与注释）。
func canonicalizeYAMLNode(node *yaml.Node) *yaml.Node {
	switch node.Kind {
	case yaml.MappingNode:
		pairs := mappingPairs(node)
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].key.Value < pairs[j].key.Value })
		out := &yaml.Node{Kind: yaml.MappingNode, Tag: node.Tag, Style: node.Style,
			HeadComment: node.HeadComment, LineComment: node.LineComment, FootComment: node.FootComment}
		for _, p := range pairs {
			out.Content = append(out.Content, p.key, canonicalizeYAMLNode(p.val))
		}
		return out
	case yaml.SequenceNode:
		// 序列整体替换语义：元素顺序是数据，不排序；但递归规整元素内的 map 键序。
		out := &yaml.Node{Kind: yaml.SequenceNode, Tag: node.Tag, Style: node.Style,
			HeadComment: node.HeadComment, LineComment: node.LineComment, FootComment: node.FootComment}
		for _, c := range node.Content {
			out.Content = append(out.Content, canonicalizeYAMLNode(c))
		}
		return out
	default:
		return node
	}
}

// parseYAMLContentNode 解析单层 yaml 为内容根节点；空 / 纯注释 / 纯空白返回 nil（不贡献）。
// 剥掉 DocumentNode 外壳，返回其内容节点，便于递归合并。
func parseYAMLContentNode(content string) (*yaml.Node, error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("yaml 解析失败: %w", err)
	}
	// 纯注释文档解析为零 Kind 节点（无内容），视作不贡献。
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	return doc.Content[0], nil
}

// mergeYAMLNode 节点级深合并：双方都是 MappingNode → 按 key 合并；否则 override 整替。
// 返回的节点用于序列化，故对 MappingNode 的 key/value 对按 key 排序（确定性键序）。
func mergeYAMLNode(base, override *yaml.Node) *yaml.Node {
	if base.Kind != yaml.MappingNode || override.Kind != yaml.MappingNode {
		// 标量覆盖 / list 整替 / 类型不一致 → 高层整替（保留 override 节点原文与注释）。
		return override
	}
	return mergeYAMLMapping(base, override)
}

// mergeYAMLMapping 合并两个 MappingNode：键级深合并 + 高层 null 删键 + 确定性键序。
func mergeYAMLMapping(base, override *yaml.Node) *yaml.Node {
	// 收集 base 键序对到有序映射（保留首次出现顺序仅作中间态，最终按 key 排序输出）。
	type pair struct {
		key *yaml.Node
		val *yaml.Node
	}
	merged := map[string]pair{}
	keys := []string{}
	add := func(k *yaml.Node, v *yaml.Node) {
		if _, ok := merged[k.Value]; !ok {
			keys = append(keys, k.Value)
		}
		merged[k.Value] = pair{key: k, val: v}
	}
	for _, kv := range mappingPairs(base) {
		add(kv.key, kv.val)
	}
	for _, kv := range mappingPairs(override) {
		if isNullNode(kv.val) {
			// 高层显式 null = 删该键。
			if _, ok := merged[kv.key.Value]; ok {
				delete(merged, kv.key.Value)
				keys = removeStr(keys, kv.key.Value)
			}
			continue
		}
		if existing, ok := merged[kv.key.Value]; ok {
			// 同 key：递归深合并（其内部决定深合并还是整替），key 节点沿用 override 的（注释随高层）。
			add(kv.key, mergeYAMLNode(existing.val, kv.val))
		} else {
			add(kv.key, kv.val)
		}
	}

	// 确定性键序：按 key 字典序输出（注释随 key/value 节点搬）。
	sort.Strings(keys)
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range keys {
		p := merged[k]
		out.Content = append(out.Content, p.key, p.val)
	}
	return out
}

// yamlPair 是 MappingNode 的一个 key/value 对。
type yamlPair struct {
	key *yaml.Node
	val *yaml.Node
}

// mappingPairs 把 MappingNode 的扁平 Content（key,value,key,value…）拆成 key/value 对。
func mappingPairs(m *yaml.Node) []yamlPair {
	pairs := make([]yamlPair, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		pairs = append(pairs, yamlPair{key: m.Content[i], val: m.Content[i+1]})
	}
	return pairs
}

// isNullNode 判断节点是否为显式 yaml null（!!null，含 ~ / null / 空标量）。
func isNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// removeStr 从切片中删除首个等于 s 的元素（保序）。
func removeStr(ss []string, s string) []string {
	for i, v := range ss {
		if v == s {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
}

// marshalYAMLNode 把内容节点序列化为文本：缩进 2 空格、确定性、保 token 与注释。
func marshalYAMLNode(node *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", fmt.Errorf("yaml 序列化失败: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("yaml 序列化关闭失败: %w", err)
	}
	return buf.String(), nil
}

// ---- JSON：json.Number 无损深合并 ----

// mergeJSONLossless 用 json.Number 解析多层 json 深合并，大整数 / 浮点按原文 emit、不失精度。
func mergeJSONLossless(layers []string) (string, error) {
	var merged any
	started := false
	for _, content := range layers {
		v, contributes, err := parseJSONNumberAware(content)
		if err != nil {
			return "", err
		}
		if !contributes {
			continue
		}
		if !started {
			merged = v
			started = true
			continue
		}
		merged = DeepMerge(merged, v)
	}
	if !started {
		return "", nil
	}
	return marshalJSONStable(merged)
}

// parseJSONNumberAware 解析单层 json，数字解析为 json.Number（保原文、不失精度）。
// 空内容不贡献（contributes=false）。
func parseJSONNumberAware(content string) (value any, contributes bool, err error) {
	if strings.TrimSpace(content) == "" {
		return nil, false, nil
	}
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	var v any
	if e := dec.Decode(&v); e != nil {
		return nil, false, fmt.Errorf("json 解析失败: %w", e)
	}
	return v, true, nil
}

// marshalJSONStable 把合并结果序列化为 json：map 键自动按字典序、json.Number 按原文 emit。
func marshalJSONStable(data any) (string, error) {
	if data == nil {
		return "", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 配置内容不做 HTML 转义，保持原样
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return "", fmt.Errorf("json 序列化失败: %w", err)
	}
	return buf.String(), nil
}

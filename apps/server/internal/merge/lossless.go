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
//
// 安全回退：若任一贡献层含锚点 / 别名 / `<<` 合并键，则不深合并（手写展开易产出悬空别名 / !!merge
// 等不可解析的坏文件），整文件回退到最高层贡献层（winner）原文（复用「坏内容回退取最高层」同一语义，
// 见 ADR-0034）。这是罕见场景，确定性优先、绝不产坏文件。
func mergeYAMLLossless(layers []string) (string, error) {
	var merged *yaml.Node // 内容根节点（MappingNode/ScalarNode/SequenceNode），不含 DocumentNode 包裹
	winner := ""          // 最高层贡献层原文（用于锚点 / 别名 / 合并键回退）
	hasAnchorOrAlias := false
	for _, content := range layers {
		node, err := parseYAMLContentNode(content)
		if err != nil {
			return "", err
		}
		if node == nil {
			continue // 空 / 纯注释 / 纯空白 / 顶层 null 层不贡献
		}
		winner = content // 低→高遍历，最后一个贡献层即最高层 winner
		if nodeHasAnchorAliasOrMergeKey(node) {
			hasAnchorOrAlias = true
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
	if hasAnchorOrAlias {
		// 回退整文件取 winner 原文（其本身是合法单层、可被重新解析）。
		return winner, nil
	}
	// 即便只有单层贡献（未走合并），也递归规整键序，保证 md5 幂等与确定性输出。
	return marshalYAMLNode(canonicalizeYAMLNode(merged))
}

// nodeHasAnchorAliasOrMergeKey 递归检测节点树是否含锚点（Anchor != ""）/ 别名（AliasNode）/ `<<` 合并键。
// 命中任一即返回 true，供 mergeYAMLLossless 决定是否回退整文件（避免产出不可解析的坏文件）。
func nodeHasAnchorAliasOrMergeKey(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	// MappingNode 的键若为 `<<`（合并键，Tag !!merge 或值为 "<<"）也命中。
	if n.Kind == yaml.MappingNode {
		for _, kv := range mappingPairs(n) {
			if kv.key.Tag == "!!merge" || kv.key.Value == "<<" {
				return true
			}
		}
	}
	for _, c := range n.Content {
		if nodeHasAnchorAliasOrMergeKey(c) {
			return true
		}
	}
	return false
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
	root := doc.Content[0]
	// 顶层整层为 null（!!null，如 "null" / "~"）视作不贡献，保留低层——对齐有损 Parse("null")=nil。
	// 注意：这是顶层整层为 null；map 内某键值为 null 是删键（在 mergeYAMLMapping 处理），不在此误伤。
	if isNullNode(root) {
		return nil, nil
	}
	return root, nil
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
			// 同 key：递归深合并（其内部决定深合并还是整替）。key 节点用 override 的（注释随高层），
			// 但 override key 无注释时回退低层 key 的注释——否则被深合并触碰的子 map 的键上区块注释会丢失。
			add(mergeKeyNodeComments(kv.key, existing.key), mergeYAMLNode(existing.val, kv.val))
		} else {
			add(kv.key, kv.val)
		}
	}

	// 确定性键序：按 key 字典序输出（注释随 key/value 节点搬）。
	sort.Strings(keys)
	// 合并后的 MappingNode 继承注释与 Style：优先取 override（高层），其字段为空则回退 base，
	// 否则被深合并触碰的中间层 map 的区块注释会丢失（与 canonicalizeYAMLNode 的保注释处理一致）。
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: pickStyle(override.Style, base.Style),
		HeadComment: pickStr(override.HeadComment, base.HeadComment),
		LineComment: pickStr(override.LineComment, base.LineComment),
		FootComment: pickStr(override.FootComment, base.FootComment)}
	for _, k := range keys {
		p := merged[k]
		out.Content = append(out.Content, p.key, p.val)
	}
	return out
}

// pickStr 优先返回 hi（高层），为空则返回 lo（低层），用于合并节点的注释字段继承。
func pickStr(hi, lo string) string {
	if hi != "" {
		return hi
	}
	return lo
}

// pickStyle 优先返回 hi（高层）样式，为 0（未设）则返回 lo（低层）。
func pickStyle(hi, lo yaml.Style) yaml.Style {
	if hi != 0 {
		return hi
	}
	return lo
}

// mergeKeyNodeComments 同 key 在多层都出现时，key 节点用高层 hi，但其任一注释字段为空则回退低层 lo 的注释。
// 避免被深合并触碰的子 map 的「键上区块注释」（挂在 key 节点上）随低层 key 节点一起被丢弃。
// 返回 hi 的浅拷贝（不改原节点），仅补注释字段。
func mergeKeyNodeComments(hi, lo *yaml.Node) *yaml.Node {
	if lo == nil || (hi.HeadComment != "" && hi.LineComment != "" && hi.FootComment != "") {
		return hi
	}
	out := *hi // 浅拷贝：值/Tag/Style 不变，仅补注释
	out.HeadComment = pickStr(hi.HeadComment, lo.HeadComment)
	out.LineComment = pickStr(hi.LineComment, lo.LineComment)
	out.FootComment = pickStr(hi.FootComment, lo.FootComment)
	return &out
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

// removeStr 返回删除首个等于 s 的元素后的新切片（保序）。
// 分配新切片、不原地修改传入切片底层数组（避免污染调用方持有的同一底层数组）。
func removeStr(ss []string, s string) []string {
	for i, v := range ss {
		if v == s {
			out := make([]string, 0, len(ss)-1)
			out = append(out, ss[:i]...)
			out = append(out, ss[i+1:]...)
			return out
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

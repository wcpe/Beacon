package merge

import (
	"fmt"
	"testing"
)

// 本文件是 merge 包纯逻辑热路径的微基准（不连 DB、不起服务，只压无副作用纯函数）：
// 覆盖链合并（DeepMerge / MergeDataID / MergeDataIDLossless）与 digest（MD5Hex / OverallMD5）——
// 二者是 .claude/rules/testing-and-quality.md §2 点名的必测高风险区。单测锁语义，本文件锁量级：
// 任何无意引入的额外拷贝、排序或反射，都会直接在 ns/op / B/op / allocs/op 上暴露。
//
// 计时约定：全部用 `for b.Loop()`。它在首次调用时自动重置计时器（循环前的输入构造与序列化不计入
// 测量），并保证循环体内函数调用的入参与返回值存活 —— 循环体不会被编译器整体消除，因此既不需要
// 包级 sink 变量，也不需要手工 b.ResetTimer()。带字节吞吐意义的基准另用 b.SetBytes 表达。
// 另注：本文件的用例耗时跨度大（数百 ns ~ 数十 ms），`go test` 默认并行跑多个包的测试，跨包争用
// 会让 ns/op 整体偏高（实测约 1.5~2 倍），要横向比较数据时请逐包跑或加 `-p 1`。

// benchScales 是输入规模档位（顶层键数 / dataId 条数），供各基准按规模分子基准。
var benchScales = []struct {
	名称 string
	规模 int
}{
	{"小规模10", 10},
	{"中规模100", 100},
	{"大规模1000", 1000},
}

// benchFormats 是受支持的三格式（与 IsValidFormat 一致），供端到端基准按格式分子基准。
var benchFormats = []string{FormatYAML, FormatJSON, FormatProperties}

// benchLayerStrides 是四层覆盖链（global < group < zone < server）的抽样步长：第 k 层只写
// i%stride==0 的键，步长逐层变大（1/3/5/7）—— 模拟「低层提供全量默认、高层只写零星差异」的真实形态。
// 三个高层步长都取与 4 互质的奇数：只有这样每层才能取到 i%4 的全部四种覆盖语义（标量覆盖 / 深合并 /
// 数组整替 / null 删键），不会出现「某层只会标量覆盖、链路上从不发生深合并或删键」的退化输入。
var benchLayerStrides = []int{1, 3, 5, 7}

// benchConfigKeys 生成规模为 n 的配置键池，形如 server.world-settings-007.max-players。
// 与仓库真实样例（server.max-players / server.motd 等）同构：带点分层、段名可读、带实例编号。
func benchConfigKeys(n int) []string {
	prefixes := []string{"server", "game", "network", "console"}
	groups := []string{"world-settings", "game-mechanics", "player-settings", "chunk-loading", "commands"}
	leaves := []string{
		"max-entity-collisions", "view-distance", "simulation-distance", "max-players", "motd",
		"difficulty", "spawn-limits", "autosave-interval",
	}
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, fmt.Sprintf("%s.%s-%03d.%s",
			prefixes[i%len(prefixes)], groups[i%len(groups)], i, leaves[i%len(leaves)]))
	}
	return keys
}

// benchBaseValue 返回键 i 在最低层（基线）的取值：标量 + 嵌套 map + 数组混排，贴近真实内容。
func benchBaseValue(i int) any {
	return map[string]any{
		"motd":        fmt.Sprintf("Beacon 生存服 zone-%03d（低层默认值）", i),
		"max-players": 100 + i,
		"zones":       []any{"zone-global", "zone-a", "zone-b"},
		"tick":        map[string]any{"autosave-interval": 6000, "keep-spawn-loaded": true},
	}
}

// benchHighValue 返回键 i 在高层上的差异项，按 i%4 轮换覆盖链的四种真实语义：
// 标量覆盖（整值替换低层 map）、子 map 深合并、数组整体替换、显式 null 删键。
func benchHighValue(i int) any {
	switch i % 4 {
	case 0:
		return 200 + i
	case 1:
		return map[string]any{"max-players": 500 + i, "tick": map[string]any{"autosave-interval": 1200}}
	case 2:
		return []any{"zone-c", "zone-d"}
	default:
		return nil
	}
}

// benchFlatBaseValue 返回键 i 在 properties（扁平格式、值恒为字符串）低层的取值。
func benchFlatBaseValue(i int) any {
	values := []string{"true", "20", "Beacon 生存服", "6000", "jdbc:mysql://db-01:3306/beacon"}
	return values[i%len(values)]
}

// benchFlatHighValue 返回键 i 在 properties 高层的取值：properties 无嵌套，只有「改值」与
// 「给空值」两种语义（空串即最高层给出的空值，properties 不存在 null 删键）。
func benchFlatHighValue(i int) any {
	if i%4 == 3 {
		return ""
	}
	return fmt.Sprintf("override-%03d", i)
}

// benchDeepMergeInput 构造规模为 n 的一对 base / override（两侧各 n 个顶层键）。
// override 只含它要覆盖的键 —— 与真实覆盖链一致：高层通常只写差异项，而不是全量重写一份。
func benchDeepMergeInput(n int) (base, override any) {
	keys := benchConfigKeys(n)
	baseMap := make(map[string]any, n)
	overrideMap := make(map[string]any, n)
	for i, key := range keys {
		baseMap[key] = benchBaseValue(i)
		overrideMap[key] = benchHighValue(i)
	}
	return baseMap, overrideMap
}

// benchDeepNestedInput 构造 5 层嵌套的一对 base / override（server → world-settings → default →
// spawn-limits → 叶子），覆盖最深递归路径：叶子标量覆盖、叶子显式 null 删键、子 map 深合并、
// 整段子 map 新增（低层完全没有 game.rules）。
func benchDeepNestedInput() (base, override any) {
	base = map[string]any{
		"server": map[string]any{
			"motd":           "Beacon 生存服",
			"game-mechanics": map[string]any{"difficulty": "hard", "enable-command-block": false},
			"world-settings": map[string]any{
				"default": map[string]any{
					"view-distance":     10,
					"keep-spawn-loaded": true,
					"spawn-limits": map[string]any{
						"monsters": 70, "animals": 10, "water-animals": 5, "ambient": 15,
					},
				},
				"world_nether": map[string]any{"view-distance": 6},
			},
		},
		"game": map[string]any{"mechanics": map[string]any{"difficulty": "normal", "weather-cycle": true}},
	}
	override = map[string]any{
		"server": map[string]any{
			"world-settings": map[string]any{
				"default": map[string]any{
					"view-distance":     4,                                              // 第 4 层叶子标量覆盖
					"keep-spawn-loaded": nil,                                            // 第 4 层叶子显式 null 删键
					"spawn-limits":      map[string]any{"monsters": 30, "ambient": nil}, // 第 5 层深合并 + 删键
				},
			},
		},
		"game": map[string]any{"rules": map[string]any{"keep-inventory": true}}, // 低层没有的整段子 map 新增
	}
	return base, override
}

// benchConfigLayers 构造四层覆盖链的配置文本，供端到端基准（解析 → 合并 → 序列化）复用。
// 最低层提供全量默认，其上三层按 benchLayerStrides 抽样写差异项；properties 是扁平格式（无嵌套
// 结构），故其取值走 benchFlat* 分支，yaml / json 共用同一份嵌套取值构造。
func benchConfigLayers(format string, n int) ([]string, error) {
	keys := benchConfigKeys(n)
	baseValue, highValue := benchBaseValue, benchHighValue
	if format == FormatProperties {
		baseValue, highValue = benchFlatBaseValue, benchFlatHighValue
	}
	layers := make([]string, 0, len(benchLayerStrides))
	for layer, stride := range benchLayerStrides {
		layerMap := make(map[string]any, n/stride+1)
		for i, key := range keys {
			if i%stride != 0 {
				continue // 高层只写抽样出来的差异项
			}
			if layer == 0 {
				layerMap[key] = baseValue(i)
				continue
			}
			layerMap[key] = highValue(i)
		}
		text, err := Serialize(format, layerMap)
		if err != nil {
			return nil, err
		}
		layers = append(layers, text)
	}
	return layers, nil
}

// benchLayersBytes 返回各层原文的总字节数，供 SetBytes 表达合并链路的字节吞吐。
func benchLayersBytes(layers []string) int64 {
	total := 0
	for _, layer := range layers {
		total += len(layer)
	}
	return int64(total)
}

// benchDataIDDigests 构造 n 条 dataId → 单层 md5 的映射（dataId 名形如真实配置文件名）。
func benchDataIDDigests(n int) map[string]string {
	names := []string{
		"bukkit.yml", "spigot.yml", "paper-global.yml", "paper-world-defaults.yml",
		"server.properties", "redis.conf", "app.json",
	}
	digests := make(map[string]string, n)
	for i := range n {
		dataID := fmt.Sprintf("zone-%03d/%s", i, names[i%len(names)])
		digests[dataID] = MD5Hex(fmt.Sprintf("%s 的合并结果", dataID))
	}
	return digests
}

// BenchmarkDeepMerge 压覆盖链合并内核 DeepMerge（纯函数、递归、零 IO / 锁 / 全局态）。
// 按输入规模分小 / 中 / 大三档（base / override 两侧顶层键数各 ≈ 档位值），并单列一档 5 层嵌套，
// 观察递归深度的成本。输入只构造一次：DeepMerge 不修改入参，跨轮复用不影响测量。
func BenchmarkDeepMerge(b *testing.B) {
	for _, scale := range benchScales {
		base, override := benchDeepMergeInput(scale.规模)
		b.Run(scale.名称, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = DeepMerge(base, override)
			}
		})
	}
	b.Run("深嵌套5层", func(b *testing.B) {
		base, override := benchDeepNestedInput()
		b.ReportAllocs()
		for b.Loop() {
			_ = DeepMerge(base, override)
		}
	})
}

// BenchmarkMergeDataID 压端到端合并链路 MergeDataID：逐层解析 → DeepMerge 合并 → 序列化。
// 按 format（yaml / json / properties）× 规模分档（层数固定四层）；SetBytes 用各层原文总字节数
// 表达吞吐 —— 解析与序列化占大头，MB/s 比裸 ns/op 更容易看出三种编解码路径的成本差。
func BenchmarkMergeDataID(b *testing.B) {
	for _, format := range benchFormats {
		for _, scale := range benchScales {
			layers, err := benchConfigLayers(format, scale.规模)
			if err != nil {
				b.Fatalf("构造 %s 基准输入失败: %v", format, err)
			}
			b.Run(format+"/"+scale.名称, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(benchLayersBytes(layers))
				for b.Loop() {
					if _, err := MergeDataID(format, layers); err != nil {
						b.Fatalf("合并失败: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkMergeDataIDLossless 压无损合并链路 MergeDataIDLossless（filetree 通道B 用）。
// 三条路径成本结构差别明显：yaml 走 yaml.Node 节点级合并 + 键序规整 + 原文 token / 注释保留，
// json 走 json.Number 保精度，properties 走行式保留；故同样按 format × 规模分档，SetBytes 同上。
func BenchmarkMergeDataIDLossless(b *testing.B) {
	for _, format := range benchFormats {
		for _, scale := range benchScales {
			layers, err := benchConfigLayers(format, scale.规模)
			if err != nil {
				b.Fatalf("构造 %s 基准输入失败: %v", format, err)
			}
			b.Run(format+"/"+scale.名称, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(benchLayersBytes(layers))
				for b.Loop() {
					if _, err := MergeDataIDLossless(format, layers); err != nil {
						b.Fatalf("无损合并失败: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkMD5Hex 压 MD5Hex：每次发布都要对每层原文与合并结果各算一次 md5，是发布链路的高频调用。
// SetBytes 表达 md5 吞吐（MB/s）；输入取真实规模的序列化配置文本，而不是等长的数字串。
func BenchmarkMD5Hex(b *testing.B) {
	for _, scale := range benchScales {
		layers, err := benchConfigLayers(FormatYAML, scale.规模)
		if err != nil {
			b.Fatalf("构造基准输入失败: %v", err)
		}
		content := layers[0] // 最低层全量文本，规模随档位线性增长
		b.Run(scale.名称, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(content)))
			for b.Loop() {
				_ = MD5Hex(content)
			}
		})
	}
}

// BenchmarkOverallMD5 压整体 md5（ADR-0008 公式：按 dataId 字典序拼接 id + 单 md5 后取 md5）。
// 按 dataId 条数分档：小规模时排序与 map 遍历是主项，大规模时字符串拼接逐渐成为主项。
func BenchmarkOverallMD5(b *testing.B) {
	for _, scale := range benchScales {
		digests := benchDataIDDigests(scale.规模)
		b.Run(scale.名称, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = OverallMD5(digests)
			}
		})
	}
}

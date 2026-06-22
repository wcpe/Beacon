package filetree

import (
	"sort"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// EffectiveFileProvenance 是某 path 解析后的有效文件 + 逐文件/逐键来源（admin 只读预览用，FR-45）。
// 结构化非豁免文件：Sources 为逐叶子键最终来源层、Deletions 为被高层 null 减量删除且最终不存在的键、WholeFile=false；
// 非结构化/豁免/坏内容文件：整文件覆盖，Sources 为单条空路径来源（= winner 层 scope）、Deletions 空、WholeFile=true。
type EffectiveFileProvenance struct {
	Path      string
	MD5       string
	Content   string
	WholeFile bool                  // 是否整文件覆盖模式（非结构化 / 豁免 / 坏内容回退）
	Sources   []merge.KeyProvenance // 逐叶子键来源（整文件模式为单条空路径 = winner 层）
	Deletions []merge.KeyProvenance // 被减量删除且最终不存在的键（整文件模式恒空）
}

// ResolveWithProvenance 把候选文件按覆盖链解析为有效文件树并附逐文件/逐键来源（admin 只读预览，见 ADR-0013 模式扩展到 ADR-0029 文件树）。
// 与 Resolve 共用同一套 per-path 分桶 + winner + 分流判定，仅额外产出来源；
// provenance 经 merge 平行纯函数（MergeDataIDWithProvenance）计算，不改 agent 下发热路径（Resolve）。
// 对任意候选集，每个 path 的 Content/MD5 必须与 Resolve 逐一致（由 provenance_test 交叉测试守护，防双实现漂移）。
func ResolveWithProvenance(candidates []model.FileObject) []EffectiveFileProvenance {
	groups := make(map[string][]model.FileObject)
	for _, c := range candidates {
		if scopePriority(c.ScopeLevel) < 0 {
			continue // 非法层不参与
		}
		groups[c.Path] = append(groups[c.Path], c)
	}

	files := make([]EffectiveFileProvenance, 0, len(groups))
	for p, layers := range groups {
		files = append(files, resolveOneWithProvenance(p, layers))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// resolveOneWithProvenance 解析单个 path 的有效文件并附来源，分流判定与 resolveOne 完全一致。
func resolveOneWithProvenance(p string, layers []model.FileObject) EffectiveFileProvenance {
	// winner = 覆盖链层级最高那份（同层后者胜，与 resolveOne 一致）；并扫描任一层是否标豁免（path 级）。
	winner := layers[0]
	anyWholeFile := winner.WholeFileOverride
	for _, c := range layers[1:] {
		if scopePriority(c.ScopeLevel) >= scopePriority(winner.ScopeLevel) {
			winner = c
		}
		if c.WholeFileOverride {
			anyWholeFile = true
		}
	}
	whole := wholeFileProvenance(p, winner)

	// 整文件模式（与 resolveOne 同口径）：① 单层贡献（字节原样、不重渲染）；② 非结构化；③ 任一层豁免。
	format, structured := FormatFromPath(p)
	if len(layers) == 1 || !structured || anyWholeFile {
		return whole
	}

	// 结构化深合并：按层级低→高组 ProvLayer，复用 merge 逐键来源。
	sorted := make([]model.FileObject, len(layers))
	copy(sorted, layers)
	sort.SliceStable(sorted, func(i, j int) bool {
		return scopePriority(sorted[i].ScopeLevel) < scopePriority(sorted[j].ScopeLevel)
	})
	provLayers := make([]merge.ProvLayer, 0, len(sorted))
	for _, c := range sorted {
		provLayers = append(provLayers, merge.ProvLayer{Scope: c.ScopeLevel, Content: c.Content})
	}
	content, sources, deletions, err := merge.MergeDataIDWithProvenance(format, provLayers)
	if err != nil {
		return whole // 坏结构化内容 → 回退整文件取 winner（ADR-0029 决策5）
	}
	return EffectiveFileProvenance{
		Path: p, MD5: md5Hex(content), Content: content, WholeFile: false,
		Sources: sources, Deletions: deletions,
	}
}

// wholeFileProvenance 构造整文件模式的来源结果：取 winner 整文件，来源为单条空路径 = winner 层 scope。
func wholeFileProvenance(p string, winner model.FileObject) EffectiveFileProvenance {
	return EffectiveFileProvenance{
		Path: p, MD5: winner.ContentMD5, Content: winner.Content, WholeFile: true,
		Sources: []merge.KeyProvenance{{Path: nil, Scope: winner.ScopeLevel}},
	}
}

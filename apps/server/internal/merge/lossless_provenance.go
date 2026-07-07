package merge

// MergeDataIDLosslessWithProvenance 与 MergeDataIDWithProvenance 等价地产出逐叶子键来源（sources）
// 与被减量删除的键（deletions），但 content 用**无损渲染**（保叶子原文 token 与注释）。
//
// 来源 / 删除是「哪层拥有该键」的**语义**问题，与文本表示无关——故直接复用类型模型版
// MergeDataIDWithProvenance 算 sources/deletions（与有损版逐一致，由交叉测试守护），
// 只把 content 换成 MergeDataIDLossless 的无损渲染。供 internal/filetree 的有效预览路径调用。
func MergeDataIDLosslessWithProvenance(format string, layers []ProvLayer) (content string, sources []KeyProvenance, deletions []KeyProvenance, err error) {
	// 复用类型模型版求逐键来源与减量删除（语义，不依赖表示）。
	_, sources, deletions, err = MergeDataIDWithProvenance(format, layers)
	if err != nil {
		return "", nil, nil, err
	}
	// content 用无损渲染（与 MergeDataIDLossless 逐一致）。
	contents := make([]string, len(layers))
	for i, l := range layers {
		contents[i] = l.Content
	}
	content, err = MergeDataIDLossless(format, contents)
	if err != nil {
		return "", nil, nil, err
	}
	return content, sources, deletions, nil
}

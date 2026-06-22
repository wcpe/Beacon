// Package filetree 实现文件树托管（通道B）的纯解析逻辑：结构化文件跨层深合并 / 非结构化整文件覆盖、manifest 与 fileTreeMd5。
// 全为无副作用纯函数，便于穷举单测。结构化文件（yml/json/properties）复用 merge 包按键深合并，
// 非结构化或标豁免的文件取覆盖链最高层整文件覆盖（见 ADR-0029，取代 ADR-0010 决策1）。
package filetree

import (
	"crypto/md5"
	"encoding/hex"
	"path"
	"sort"
	"strings"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
)

// EffectiveFile 是某 path 按覆盖链解析后的有效文件。
type EffectiveFile struct {
	Path    string
	MD5     string
	Content string
}

// scopePriority 覆盖层优先级（低→高，高覆盖低）；未知层返回 -1（不参与）。
func scopePriority(level string) int {
	switch level {
	case model.ScopeGlobal:
		return 0
	case model.ScopeGroup:
		return 1
	case model.ScopeZone:
		return 2
	case model.ScopeServer:
		return 3
	default:
		return -1
	}
}

// Resolve 把某 agent 身份的四层候选文件按覆盖链解析为有效文件树：
// 按 path 分桶；结构化文件（yml/json/properties）跨层按键深合并（复用 merge 包），
// 非结构化或标 WholeFileOverride 的文件取覆盖链**层级最高**那一整份（整文件覆盖）。
// 同层同 path 不应出现（唯一键保证），若出现以最后一个为准。
// 结果按 path 字典序稳定排序，保证下游 manifest 与 md5 幂等（见 ADR-0029）。
func Resolve(candidates []model.FileObject) []EffectiveFile {
	groups := make(map[string][]model.FileObject)
	for _, c := range candidates {
		if scopePriority(c.ScopeLevel) < 0 {
			continue // 非法层不参与
		}
		groups[c.Path] = append(groups[c.Path], c)
	}

	files := make([]EffectiveFile, 0, len(groups))
	for p, layers := range groups {
		files = append(files, resolveOne(p, layers))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// resolveOne 解析单个 path 的有效文件：结构化深合并 / 整文件覆盖兜底（含豁免与坏内容降级）。
func resolveOne(p string, layers []model.FileObject) EffectiveFile {
	// winner = 覆盖链层级最高那份（同层后者胜，沿用旧语义）；整文件模式取它，深合并坏内容也回退它。
	winner := layers[0]
	anyWholeFile := winner.WholeFileOverride
	for _, c := range layers[1:] {
		if scopePriority(c.ScopeLevel) >= scopePriority(winner.ScopeLevel) {
			winner = c
		}
		if c.WholeFileOverride {
			anyWholeFile = true // 豁免是 path 级：任一层标即整文件覆盖
		}
	}
	wholeFile := EffectiveFile{Path: p, MD5: winner.ContentMD5, Content: winner.Content}

	// 整文件覆盖（字节原样取最高层，不 parse/reserialize）的三种情形：
	//   ① 单层贡献——无需合并，原样透传杜绝有损往返（007→7、1.10→1.1、日期→时间戳、纯注释→空、JSON 大整数精度丢失）；
	//   ② 非结构化后缀；③ 任一层标 WholeFileOverride 豁免（path 级）。
	format, structured := FormatFromPath(p)
	if len(layers) == 1 || !structured || anyWholeFile {
		return wholeFile
	}

	// 结构化深合并：按层级低→高取内容，复用 merge 无损按键合并（保叶子原文 token 与注释，见 ADR-0034）。
	sorted := make([]model.FileObject, len(layers))
	copy(sorted, layers)
	sort.SliceStable(sorted, func(i, j int) bool {
		return scopePriority(sorted[i].ScopeLevel) < scopePriority(sorted[j].ScopeLevel)
	})
	contents := make([]string, 0, len(sorted))
	for _, c := range sorted {
		contents = append(contents, c.Content)
	}
	merged, err := merge.MergeDataIDLossless(format, contents)
	if err != nil {
		return wholeFile // 坏结构化内容 → 回退整文件取 winner（ADR-0029 决策5）
	}
	return EffectiveFile{Path: p, MD5: md5Hex(merged), Content: merged}
}

// FormatFromPath 按文件后缀判定结构化格式（yaml/json/properties）；非结构化返回 ("", false)。
// 导出供发布期校验复用（与解析同口径判定哪些文件按结构化处理）。
func FormatFromPath(p string) (string, bool) {
	switch strings.ToLower(path.Ext(p)) {
	case ".yml", ".yaml":
		return merge.FormatYAML, true
	case ".json":
		return merge.FormatJSON, true
	case ".properties":
		return merge.FormatProperties, true
	default:
		return "", false
	}
}

// Manifest 由有效文件树算出 path→md5 清单（agent 比对增量同步用）。
func Manifest(files []EffectiveFile) map[string]string {
	m := make(map[string]string, len(files))
	for _, f := range files {
		m[f.Path] = f.MD5
	}
	return m
}

// FileTreeMD5 由 path→md5 清单算出整棵文件树的指纹。
// 公式：md5( 按 path 字典序拼接 (path + ":" + 单文件md5 + "\n") )。
// 把 path 名纳入哈希，避免集合 {a:x} 与 {b:x} 碰撞误判（沿用 ADR-0008 思路）；
// 与配置 md5 相互独立，各自长轮询唤醒集合分开。
func FileTreeMD5(pathToMD5 map[string]string) string {
	paths := make([]string, 0, len(pathToMD5))
	for p := range pathToMD5 {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p)
		b.WriteString(":")
		b.WriteString(pathToMD5[p])
		b.WriteString("\n")
	}
	return md5Hex(b.String())
}

// ContentMD5 返回单个文件内容的小写十六进制 md5（供 service 计算行上冗余 md5）。
func ContentMD5(content string) string {
	return md5Hex(content)
}

// md5Hex 返回字符串内容的小写十六进制 md5。
func md5Hex(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

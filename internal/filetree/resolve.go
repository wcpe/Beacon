// Package filetree 实现文件树托管（通道B）的纯解析逻辑：scope 整文件覆盖、manifest 与 fileTreeMd5。
// 全为无副作用纯函数，便于穷举单测；与配置中心 merge 包语义不同——文件按 path 整文件覆盖，绝不深合并（见 ADR-0010）。
package filetree

import (
	"crypto/md5"
	"encoding/hex"
	"sort"
	"strings"

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
// 按 path 分桶，每个 path 取覆盖链上**层级最高**的那一份（整文件覆盖，不深合并）；
// 同层同 path 不应出现（唯一键保证），若出现以最后一个为准。
// 结果按 path 字典序稳定排序，保证下游 manifest 与 md5 幂等。
func Resolve(candidates []model.FileObject) []EffectiveFile {
	winner := make(map[string]model.FileObject, len(candidates))
	for _, c := range candidates {
		p := scopePriority(c.ScopeLevel)
		if p < 0 {
			continue // 非法层不参与
		}
		if cur, ok := winner[c.Path]; !ok || p >= scopePriority(cur.ScopeLevel) {
			winner[c.Path] = c
		}
	}

	files := make([]EffectiveFile, 0, len(winner))
	for _, w := range winner {
		files = append(files, EffectiveFile{Path: w.Path, MD5: w.ContentMD5, Content: w.Content})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
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

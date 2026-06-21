package filetree

import (
	"sort"
	"strings"

	"github.com/wcpe/Beacon/internal/model"
)

// OverrideMember 覆盖集成员的解析视图：相对目标根的 path + 内容指纹。
// ContentMD5 为成员当前内容的小写 hex md5（按字节算，复用 file_object 行上冗余，见 ADR-0011 决策 9）；
// 仅供 overrideMd5 指纹计算，不投递给 agent（agent 仍按 path 经 override-sets/content 取整文件内容）。
type OverrideMember struct {
	// 成员文件 path（相对目标根）
	Path string
	// 成员当前内容的 md5（小写 hex，按字节算）
	ContentMD5 string
}

// EffectiveOverrideSet 是某 server 适用的一个覆盖集（按覆盖链解析后）：
// 承载目标根 + 受限重载命令 + 成员文件 path 清单（成员内容仍走通道B 的 files/content 取）。
type EffectiveOverrideSet struct {
	// 覆盖集名称（同一覆盖链内唯一标识，如目标插件名）
	Name string
	// 目标插件根目录（相对 plugins，如 plugins/AllinCore），agent 落盘根
	TargetRoot string
	// 一条受限重载命令（可空表示不下发命令）
	ReloadCommand string
	// 成员文件 path 清单（相对目标根，按字典序），内容由 agent 经 files/content 取
	MemberPaths []string
	// 成员内容指纹（path→内容 md5）：仅参与 overrideMd5 计算、不投递给 agent。
	// 纳入哈希使成员「内容改了但 path 没变」也改变 overrideMd5（FR-15 内容热更），沿用 FileTreeMD5 的 path:md5 思路。
	MemberMD5s map[string]string
}

// ResolveOverrideSets 把某 server 的四层候选覆盖集按覆盖链解析为适用覆盖集列表：
// 按 Name 分桶，每个 Name 取覆盖链上层级最高的那一份（整集覆盖，不合并成员）；
// 仅取 enabled 的集。结果按 Name 字典序稳定排序，保证下游 overrideMd5 幂等。
//
// membersOf 给定覆盖集 ID 返回其成员（path + 内容指纹，已按 path 字典序、已早校验）；
// 由调用方注入（service 用 repo 实现），便于纯函数穷举单测。
func ResolveOverrideSets(candidates []model.FileOverrideSet, membersOf func(setID uint) []OverrideMember) []EffectiveOverrideSet {
	winner := make(map[string]model.FileOverrideSet, len(candidates))
	for _, c := range candidates {
		if !c.Enabled {
			continue // 下线的集不参与
		}
		p := scopePriority(c.ScopeLevel)
		if p < 0 {
			continue // 非法层不参与
		}
		if cur, ok := winner[c.Name]; !ok || p >= scopePriority(cur.ScopeLevel) {
			winner[c.Name] = c
		}
	}

	sets := make([]EffectiveOverrideSet, 0, len(winner))
	for _, w := range winner {
		members := membersOf(w.ID)
		paths := make([]string, 0, len(members))
		md5s := make(map[string]string, len(members))
		for _, m := range members {
			paths = append(paths, m.Path)
			md5s[m.Path] = m.ContentMD5
		}
		sets = append(sets, EffectiveOverrideSet{
			Name:          w.Name,
			TargetRoot:    w.TargetRoot,
			ReloadCommand: w.ReloadCommand,
			MemberPaths:   paths,
			MemberMD5s:    md5s,
		})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	return sets
}

// OverrideMD5 由适用覆盖集列表算出整体指纹（agent 长轮询比对用）。
// 公式：md5( 按 Name 字典序拼接 (name + "|" + targetRoot + "|" + reloadCommand + "|" +
// 成员按 path 字典序逐个 "path:内容md5," 连接 + "\n") )。
// 把目标根 / 命令 / 成员清单 + **成员内容指纹**全纳入哈希——任一变更（含命令改动、成员增删、
// **成员内容只改不变 path**）都触发 agent 重取落盘（FR-15 内容热更）；成员内容 md5 复用 file_object
// 行上冗余、按字节算（ADR-0011 决策 9），与配置 md5、fileTreeMd5 相互独立。
func OverrideMD5(sets []EffectiveOverrideSet) string {
	ordered := make([]EffectiveOverrideSet, len(sets))
	copy(ordered, sets)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var b strings.Builder
	for _, s := range ordered {
		b.WriteString(s.Name)
		b.WriteString("|")
		b.WriteString(s.TargetRoot)
		b.WriteString("|")
		b.WriteString(s.ReloadCommand)
		b.WriteString("|")
		// 成员 path 已按字典序；逐个拼 "path:内容md5,"，把内容指纹纳入整体哈希。
		for _, p := range s.MemberPaths {
			b.WriteString(p)
			b.WriteString(":")
			b.WriteString(s.MemberMD5s[p])
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	return md5Hex(b.String())
}

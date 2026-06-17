package filetree

import (
	"sort"
	"strings"

	"beacon/internal/model"
)

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
}

// ResolveOverrideSets 把某 server 的四层候选覆盖集按覆盖链解析为适用覆盖集列表：
// 按 Name 分桶，每个 Name 取覆盖链上层级最高的那一份（整集覆盖，不合并成员）；
// 仅取 enabled 的集。结果按 Name 字典序稳定排序，保证下游 overrideMd5 幂等。
//
// memberPathsOf 给定覆盖集 ID 返回其成员 path 清单（已按字典序、已早校验）；
// 由调用方注入（service 用 repo 实现），便于纯函数穷举单测。
func ResolveOverrideSets(candidates []model.FileOverrideSet, memberPathsOf func(setID uint) []string) []EffectiveOverrideSet {
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
		sets = append(sets, EffectiveOverrideSet{
			Name:          w.Name,
			TargetRoot:    w.TargetRoot,
			ReloadCommand: w.ReloadCommand,
			MemberPaths:   memberPathsOf(w.ID),
		})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	return sets
}

// OverrideMD5 由适用覆盖集列表算出整体指纹（agent 长轮询比对用）。
// 公式：md5( 按 Name 字典序拼接 (name + "|" + targetRoot + "|" + reloadCommand + "|" + 成员path用","连接 + "\n") )。
// 把目标根 / 命令 / 成员清单全纳入哈希——任一变更（含命令改动、成员增删）都触发 agent 重取，
// 与配置 md5、fileTreeMd5 相互独立。
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
		b.WriteString(strings.Join(s.MemberPaths, ","))
		b.WriteString("\n")
	}
	return md5Hex(b.String())
}

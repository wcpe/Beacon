package filetree

import (
	"testing"

	"github.com/wcpe/Beacon/internal/model"
)

// os 构造一个测试用 FileOverrideSet（仅填解析关注的字段）。
func os(id uint, name, level, targetRoot, cmd string) model.FileOverrideSet {
	return model.FileOverrideSet{
		ID:            id,
		Name:          name,
		ScopeLevel:    level,
		TargetRoot:    targetRoot,
		ReloadCommand: cmd,
		Enabled:       true,
	}
}

// findSet 在适用覆盖集里按 name 取一份，不存在返回 (EffectiveOverrideSet{}, false)。
func findSet(sets []EffectiveOverrideSet, name string) (EffectiveOverrideSet, bool) {
	for _, s := range sets {
		if s.Name == name {
			return s, true
		}
	}
	return EffectiveOverrideSet{}, false
}

// noMembers 是不返回成员的桩（不关注成员时用）。
func noMembers(uint) []OverrideMember { return nil }

// TestResolveOverrideSetsHigherLayerWins 同名覆盖集多层：取层级最高那份（整集覆盖）。
func TestResolveOverrideSetsHigherLayerWins(t *testing.T) {
	sets := ResolveOverrideSets([]model.FileOverrideSet{
		os(1, "AllinCore", model.ScopeGlobal, "plugins/AllinCore", "allin reload"),
		os(2, "AllinCore", model.ScopeServer, "plugins/AllinCore", "allin reload all"),
	}, noMembers)
	if len(sets) != 1 {
		t.Fatalf("同名集应只留一份，得 %d", len(sets))
	}
	s, _ := findSet(sets, "AllinCore")
	if s.ReloadCommand != "allin reload all" {
		t.Fatalf("应取 server 层那份命令，得 %q", s.ReloadCommand)
	}
}

// TestResolveOverrideSetsDisabledSkipped 下线的集不参与解析。
func TestResolveOverrideSetsDisabledSkipped(t *testing.T) {
	disabled := os(1, "AllinCore", model.ScopeGlobal, "plugins/AllinCore", "allin reload")
	disabled.Enabled = false
	sets := ResolveOverrideSets([]model.FileOverrideSet{disabled}, noMembers)
	if len(sets) != 0 {
		t.Fatalf("下线集不应参与，得 %d", len(sets))
	}
}

// TestResolveOverrideSetsMembersAttached 成员 path 经注入函数挂到对应集。
func TestResolveOverrideSetsMembersAttached(t *testing.T) {
	sets := ResolveOverrideSets([]model.FileOverrideSet{
		os(7, "AllinCore", model.ScopeGlobal, "plugins/AllinCore", "allin reload"),
	}, func(id uint) []OverrideMember {
		if id == 7 {
			return []OverrideMember{{Path: "config.yml", ContentMD5: "m1"}, {Path: "scripts/hello.js", ContentMD5: "m2"}}
		}
		return nil
	})
	s, ok := findSet(sets, "AllinCore")
	if !ok || len(s.MemberPaths) != 2 {
		t.Fatalf("成员应挂上 2 个，得 %+v", s)
	}
	// 成员内容指纹应一并挂上（供 overrideMd5 计算，不投递给 agent）。
	if s.MemberMD5s["config.yml"] != "m1" || s.MemberMD5s["scripts/hello.js"] != "m2" {
		t.Fatalf("成员内容指纹未挂上：%+v", s.MemberMD5s)
	}
}

// TestResolveOverrideSetsSortedByName 多集按 Name 字典序稳定排序。
func TestResolveOverrideSetsSortedByName(t *testing.T) {
	sets := ResolveOverrideSets([]model.FileOverrideSet{
		os(1, "Zeta", model.ScopeGlobal, "plugins/Zeta", ""),
		os(2, "Alpha", model.ScopeGlobal, "plugins/Alpha", ""),
	}, noMembers)
	if len(sets) != 2 || sets[0].Name != "Alpha" || sets[1].Name != "Zeta" {
		t.Fatalf("应按 Name 字典序，得 %+v", sets)
	}
}

// TestOverrideMD5IdempotentAndSensitive overrideMd5 幂等 + 对命令/成员变更敏感。
func TestOverrideMD5Idempotent(t *testing.T) {
	base := []EffectiveOverrideSet{
		{Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload", MemberPaths: []string{"a.yml", "b.yml"}},
	}
	if got := OverrideMD5(base); got != OverrideMD5(base) {
		t.Fatal("同输入 md5 应幂等")
	}
	// 命令变更应改 md5。
	changedCmd := []EffectiveOverrideSet{
		{Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload all", MemberPaths: []string{"a.yml", "b.yml"}},
	}
	if OverrideMD5(base) == OverrideMD5(changedCmd) {
		t.Fatal("命令变更应改 md5")
	}
	// 成员增删应改 md5。
	changedMembers := []EffectiveOverrideSet{
		{Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload", MemberPaths: []string{"a.yml"}},
	}
	if OverrideMD5(base) == OverrideMD5(changedMembers) {
		t.Fatal("成员变更应改 md5")
	}
}

// TestOverrideMD5SensitiveToMemberContent FR-15 内容热更缺口回归：
// 成员「内容只改不变 path」（path/targetRoot/命令/成员清单全不变，仅 ContentMD5 变）overrideMd5 必须变；
// 内容不变则 md5 幂等（不无谓重推）。
func TestOverrideMD5SensitiveToMemberContent(t *testing.T) {
	base := []EffectiveOverrideSet{
		{
			Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload",
			MemberPaths: []string{"a.yml", "b.yml"},
			MemberMD5s:  map[string]string{"a.yml": "h-a-1", "b.yml": "h-b-1"},
		},
	}
	// 内容指纹完全相同 → md5 幂等（内容没变不重推）。
	same := []EffectiveOverrideSet{
		{
			Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload",
			MemberPaths: []string{"a.yml", "b.yml"},
			MemberMD5s:  map[string]string{"a.yml": "h-a-1", "b.yml": "h-b-1"},
		},
	}
	if OverrideMD5(base) != OverrideMD5(same) {
		t.Fatal("成员内容指纹相同时 md5 应幂等")
	}
	// 仅某成员内容指纹改变（path 不变）→ md5 必须变（这是修复前漏掉的触发点）。
	contentChanged := []EffectiveOverrideSet{
		{
			Name: "AllinCore", TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload",
			MemberPaths: []string{"a.yml", "b.yml"},
			MemberMD5s:  map[string]string{"a.yml": "h-a-2", "b.yml": "h-b-1"},
		},
	}
	if OverrideMD5(base) == OverrideMD5(contentChanged) {
		t.Fatal("成员内容改变（path 不变）应改 md5")
	}
}

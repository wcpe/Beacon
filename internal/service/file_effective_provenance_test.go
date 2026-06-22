//go:build integration

package service_test

import (
	"reflect"
	"testing"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/service"
)

// scopeOfFileProv 在文件来源列表里按键路径查来源 scope（空路径以 nil 表达）。
func scopeOfFileProv(list []merge.KeyProvenance, path ...string) (string, bool) {
	want := path
	if len(path) == 0 {
		want = nil
	}
	for _, p := range list {
		got := p.Path
		if len(got) == 0 {
			got = nil
		}
		if reflect.DeepEqual(got, want) {
			return p.Scope, true
		}
	}
	return "", false
}

// findFileProv 在带来源的有效文件树里按 path 取一份。
func findFileProv(files []service.EffectiveFileWithProvenance, path string) (service.EffectiveFileWithProvenance, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return service.EffectiveFileWithProvenance{}, false
}

// TestFileResolveWithProvenance 集成验证 FR-45：admin 文件树有效预览的合并结果与 Resolve 一致，
// 结构化文件逐键来源 / 减量正确，整文件/豁免文件标整文件来源。
func TestFileResolveWithProvenance(t *testing.T) {
	s := newFileStack(t)

	// 指派 s1 → area1/zoneA，使 server 层覆盖参与解析
	if _, err := s.assign.Upsert("prod", "s1", "area1", "zoneA", ""); err != nil {
		t.Fatalf("建指派失败: %v", err)
	}

	// 经 FileService 建多层结构化文件（同一 path app.yml）：global 基线 + zone 增量 + server 增量/减量
	mk := func(group, scope, target, content string, whole bool) {
		if _, err := s.files.Create(service.CreateFileParams{
			Namespace: "prod", Group: group, Path: "app.yml",
			ScopeLevel: scope, ScopeTarget: target,
			Content: content, Operator: "alice", WholeFileOverride: whole,
		}); err != nil {
			t.Fatalf("建 %s 层失败: %v", scope, err)
		}
	}
	mk(model.GlobalGroupCode, model.ScopeGlobal, "", "a: 1\nb:\n  x: 1\n", false)
	mk("area1", model.ScopeZone, "zoneA", "b:\n  y: 2\n", false)
	mk("area1", model.ScopeServer, "s1", "a: null\nc: 3\n", false)

	// 另建一个非结构化文件，验证整文件来源
	mk2 := func(group, scope, target, content string) {
		if _, err := s.files.Create(service.CreateFileParams{
			Namespace: "prod", Group: group, Path: "boot.allin",
			ScopeLevel: scope, ScopeTarget: target,
			Content: content, Operator: "alice",
		}); err != nil {
			t.Fatalf("建非结构化 %s 层失败: %v", scope, err)
		}
	}
	mk2(model.GlobalGroupCode, model.ScopeGlobal, "", "g\n")
	mk2("area1", model.ScopeServer, "s1", "s\n")

	prov, err := s.fileEff.ResolveWithProvenance("prod", "s1", "", "")
	if err != nil {
		t.Fatalf("provenance 解析失败: %v", err)
	}

	// 合并结果与 Resolve（下发口径）逐一致
	base, err := s.fileEff.Resolve("prod", "s1", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if prov.FileTreeMD5 != base.FileTreeMD5 {
		t.Fatalf("fileTreeMd5 与 Resolve 不一致：%s != %s", prov.FileTreeMD5, base.FileTreeMD5)
	}
	if len(prov.Files) != len(base.Files) {
		t.Fatalf("文件数不一致：prov=%d base=%d", len(prov.Files), len(base.Files))
	}
	for _, bf := range base.Files {
		pf, ok := findFileProv(prov.Files, bf.Path)
		if !ok {
			t.Fatalf("path %q 在 provenance 版缺失", bf.Path)
		}
		if pf.Content != bf.Content || pf.MD5 != bf.MD5 {
			t.Fatalf("path %q 内容/ md5 与 Resolve 漂移", bf.Path)
		}
	}

	// 结构化 app.yml：逐键来源 + 减量
	app, ok := findFileProv(prov.Files, "app.yml")
	if !ok {
		t.Fatal("缺 app.yml")
	}
	if app.WholeFile {
		t.Fatal("app.yml 应为深合并模式")
	}
	if sc, _ := scopeOfFileProv(app.Sources, "b", "x"); sc != model.ScopeGlobal {
		t.Errorf("b.x 来源应为 global，实际 %q", sc)
	}
	if sc, _ := scopeOfFileProv(app.Sources, "b", "y"); sc != model.ScopeZone {
		t.Errorf("b.y 来源应为 zone，实际 %q", sc)
	}
	if sc, _ := scopeOfFileProv(app.Sources, "c"); sc != model.ScopeServer {
		t.Errorf("c 来源应为 server，实际 %q", sc)
	}
	if sc, ok := scopeOfFileProv(app.Deletions, "a"); !ok || sc != model.ScopeServer {
		t.Errorf("a 应被 server 减量删除，实际 ok=%v scope=%q", ok, sc)
	}

	// 非结构化 boot.allin：整文件来源（单条空路径 = winner=server）
	boot, ok := findFileProv(prov.Files, "boot.allin")
	if !ok {
		t.Fatal("缺 boot.allin")
	}
	if !boot.WholeFile {
		t.Error("boot.allin 应为整文件模式")
	}
	if sc, ok := scopeOfFileProv(boot.Sources); !ok || sc != model.ScopeServer {
		t.Errorf("boot.allin 整文件来源应为 server，实际 ok=%v scope=%q", ok, sc)
	}
}

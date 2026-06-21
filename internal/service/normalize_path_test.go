package service

import (
	"testing"

	"beacon/internal/apperr"
)

// TestNormalizePathRejectsReservedAgentSelfDirs 锁定：控制面 normalizePath 把"agent 自身 plugin 目录"
// （BeaconAgent / BeaconAgentProxy）顶段视为受保护前缀，入库前直接拒绝——杜绝运维误把 BeaconAgent/config.yml
// 之类文件经 FR-14 文件树或 FR-38 导入塞进有效树，进而被下游 agent 覆写到自身 dataFolder（污染身份/快照）。
//
// 详见 docs/specs/file-tree-hosting.md 「受保护路径」与 [docs/adr/0010](../../docs/adr/0010-file-tree-hosting-blob-channel.md)。
func TestNormalizePathRejectsReservedAgentSelfDirs(t *testing.T) {
	// 合法 path 仍放行（确保改动未破坏正路径）
	if _, err := normalizePath("LuckPerms/config.yml"); err != nil {
		t.Fatalf("合法路径应放行：%v", err)
	}
	if _, err := normalizePath("a.yml"); err != nil {
		t.Fatalf("合法路径应放行：%v", err)
	}

	// 受保护：BeaconAgent 顶段（bukkit agent 自身 plugin 名）
	for _, p := range []string{
		"BeaconAgent/config.yml",
		"BeaconAgent/effective-config.snapshot.json",
		"BeaconAgent/sub/dir/file.json",
	} {
		if _, err := normalizePath(p); err != apperr.ErrInvalidPath {
			t.Errorf("path %q 应被拒为 ErrInvalidPath（agent 自身目录），实际 %v", p, err)
		}
	}

	// 受保护：BeaconAgentProxy 顶段（bungee agent 自身 plugin 名）
	for _, p := range []string{
		"BeaconAgentProxy/config.yml",
		"BeaconAgentProxy/state/x.json",
	} {
		if _, err := normalizePath(p); err != apperr.ErrInvalidPath {
			t.Errorf("path %q 应被拒为 ErrInvalidPath（agent 自身目录），实际 %v", p, err)
		}
	}

	// 严格顶段相等：BeaconAgentX 不属于 BeaconAgent 子树，不应被误拦
	if _, err := normalizePath("BeaconAgentX/config.yml"); err != nil {
		t.Errorf("BeaconAgentX 顶段非保护项，应放行，实际 %v", err)
	}

	// 已被既有规则拒绝的项目仍被拒（覆盖回归）
	if _, err := normalizePath(""); err != apperr.ErrInvalidPath {
		t.Errorf("空路径应仍为 ErrInvalidPath，实际 %v", err)
	}
	if _, err := normalizePath("../escape.yml"); err != apperr.ErrInvalidPath {
		t.Errorf("穿越路径应仍为 ErrInvalidPath，实际 %v", err)
	}
	if _, err := normalizePath("a\\b"); err != apperr.ErrInvalidPath {
		t.Errorf("反斜杠路径应仍为 ErrInvalidPath，实际 %v", err)
	}
}

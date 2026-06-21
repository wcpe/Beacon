package service

import (
	"testing"

	"github.com/wcpe/Beacon/internal/apperr"
)

// TestNormalizePathAllowsAgentSelfDirs 锁定方案 D（FR-38/FR-39 归真）：控制面 normalizePath 不再把
// "agent 自身 plugin 目录"（BeaconAgent / BeaconAgentProxy）顶段当作受保护前缀拒绝——放开后，上传导入
// 与在线实例反向抓取可托管自身目录（FR-41 env 注入已使 config.yml 非身份真源，托管自身目录无身份污染），
// 自我保护改由 agent 侧 FileTreeApplier 的 observe-only 守卫兜底（只观测不写回）。
//
// 穿越 / 绝对 / 反斜杠 / 空仍硬拒（安全不退化）。详见 docs/specs/file-tree-hosting.md 「受保护路径」
// 与 [docs/adr/0028](../../docs/adr/0028-allow-hosting-agent-self-dir.md)。
func TestNormalizePathAllowsAgentSelfDirs(t *testing.T) {
	// 合法 path 仍放行（确保改动未破坏正路径）
	if _, err := normalizePath("LuckPerms/config.yml"); err != nil {
		t.Fatalf("合法路径应放行：%v", err)
	}
	if _, err := normalizePath("a.yml"); err != nil {
		t.Fatalf("合法路径应放行：%v", err)
	}

	// 放开：BeaconAgent 顶段（bukkit agent 自身 plugin 名）现可托管
	for _, p := range []string{
		"BeaconAgent/config.yml",
		"BeaconAgent/effective-config.snapshot.json",
		"BeaconAgent/sub/dir/file.json",
	} {
		clean, err := normalizePath(p)
		if err != nil {
			t.Errorf("path %q 应被放行（方案 D 放开自身目录），实际 %v", p, err)
		}
		if clean != p {
			t.Errorf("path %q 规整后应不变，实际 %q", p, clean)
		}
	}

	// 放开：BeaconAgentProxy 顶段（bungee agent 自身 plugin 名）现可托管
	for _, p := range []string{
		"BeaconAgentProxy/config.yml",
		"BeaconAgentProxy/state/x.json",
	} {
		if _, err := normalizePath(p); err != nil {
			t.Errorf("path %q 应被放行（方案 D 放开自身目录），实际 %v", p, err)
		}
	}

	// BeaconAgentX 顶段一向放行（非精确顶段，从不属于保护项）
	if _, err := normalizePath("BeaconAgentX/config.yml"); err != nil {
		t.Errorf("BeaconAgentX 顶段应放行，实际 %v", err)
	}

	// 安全不退化：空 / 穿越 / 绝对 / 反斜杠仍被拒
	if _, err := normalizePath(""); err != apperr.ErrInvalidPath {
		t.Errorf("空路径应仍为 ErrInvalidPath，实际 %v", err)
	}
	if _, err := normalizePath("../escape.yml"); err != apperr.ErrInvalidPath {
		t.Errorf("穿越路径应仍为 ErrInvalidPath，实际 %v", err)
	}
	if _, err := normalizePath("/etc/passwd"); err != apperr.ErrInvalidPath {
		t.Errorf("绝对路径应仍为 ErrInvalidPath，实际 %v", err)
	}
	if _, err := normalizePath("a\\b"); err != apperr.ErrInvalidPath {
		t.Errorf("反斜杠路径应仍为 ErrInvalidPath，实际 %v", err)
	}
}

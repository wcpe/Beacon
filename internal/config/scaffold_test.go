package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"beacon"
)

// TestEnsureConfigFileReleasesWithRandomCredentials 验证首启释放 config.yml 时就地填入随机强凭据、
// 不再自动生成 .env，且释放的 config.yml 可直接通过校验（开箱即跑、config.yml 即真源）。
func TestEnsureConfigFileReleasesWithRandomCredentials(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	released, err := EnsureConfigFile(p, beacon.ConfigExampleYAML)
	if err != nil || !released {
		t.Fatalf("config.yml 不存在时应释放并返回 true：released=%v err=%v", released, err)
	}
	// 首启不应自动生成 .env——否则 .env 会静默盖掉 config.yml（本次修复的根因）
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("首启不应自动生成 .env，却发现 .env（或检查出错）：err=%v", err)
	}
	// 释放的 config.yml 应能直接通过校验（口令/密钥已填随机强值），无需任何 env 注入
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("释放的 config.yml 应开箱通过校验，却失败: %v", err)
	}
	if strings.TrimSpace(cfg.Auth.Password) == "" {
		t.Fatal("释放的 config.yml 中 auth.password 应为非空随机值")
	}
	if strings.TrimSpace(cfg.Auth.Secret) == "" {
		t.Fatal("释放的 config.yml 中 auth.secret 应为非空随机值")
	}
}

// TestEnsureConfigFileSkipsWhenPresent 验证 config.yml 已存在时跳过、不覆盖、返回 false。
func TestEnsureConfigFileSkipsWhenPresent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte("http-addr: \":9999\"\n"), 0o600); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}
	released, err := EnsureConfigFile(p, beacon.ConfigExampleYAML)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if released {
		t.Fatal("config.yml 已存在时不应释放（应返回 false）")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "http-addr: \":9999\"\n" {
		t.Fatalf("已存在文件不应被覆盖，实际 %q", b)
	}
}

// TestInjectCredentialsFillsEmptyAuthFields 验证把模板里留空的 password/secret 就地替换为随机强值、不残留占位。
func TestInjectCredentialsFillsEmptyAuthFields(t *testing.T) {
	tmpl := []byte("auth:\n  password: \"\"\n  secret: \"\"\n")
	out := string(injectCredentials(tmpl, "PWD123", "SECRET456"))
	if !strings.Contains(out, `password: "PWD123"`) {
		t.Fatalf("password 未被填入随机值: %q", out)
	}
	if !strings.Contains(out, `secret: "SECRET456"`) {
		t.Fatalf("secret 未被填入随机值: %q", out)
	}
	if strings.Contains(out, `password: ""`) || strings.Contains(out, `secret: ""`) {
		t.Fatalf("不应残留空凭据占位: %q", out)
	}
}

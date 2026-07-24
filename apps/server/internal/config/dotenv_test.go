package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeEnvFile 在临时目录写一个 .env，返回其路径。
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写 .env 失败: %v", err)
	}
	return p
}

// TestLoadDotEnvInjectsUnsetKeys 验证 .env 注入当前未设置的环境变量。
func TestLoadDotEnvInjectsUnsetKeys(t *testing.T) {
	const key = "BEACON_TEST_DOTENV_A"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })

	p := writeEnvFile(t, key+"=hello\n")
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("加载 .env 应成功: %v", err)
	}
	if got := os.Getenv(key); got != "hello" {
		t.Fatalf(".env 未注入未设置的键，实际 %q", got)
	}
}

// TestLoadDotEnvRealEnvWins 验证真实环境变量优先，不被 .env 覆盖。
func TestLoadDotEnvRealEnvWins(t *testing.T) {
	const key = "BEACON_TEST_DOTENV_B"
	t.Setenv(key, "real")

	p := writeEnvFile(t, key+"=from-dotenv\n")
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("加载 .env 应成功: %v", err)
	}
	if got := os.Getenv(key); got != "real" {
		t.Fatalf("真实环境变量应优先、不被 .env 覆盖，实际 %q", got)
	}
}

// TestLoadDotEnvMissingFileOK 验证缺 .env 文件视为正常、不报错。
func TestLoadDotEnvMissingFileOK(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("缺 .env 文件应视为正常，却报错: %v", err)
	}
}

// TestLoadDotEnvStripsQuotesAndSkipsComments 验证去引号、trim、跳过注释与空行。
func TestLoadDotEnvStripsQuotesAndSkipsComments(t *testing.T) {
	const k1, k2 = "BEACON_TEST_DOTENV_C", "BEACON_TEST_DOTENV_D"
	os.Unsetenv(k1)
	os.Unsetenv(k2)
	t.Cleanup(func() { os.Unsetenv(k1); os.Unsetenv(k2) })

	content := "# 这是注释\n\n" + k1 + " = \"quoted value\"\n" + k2 + "='single'\n"
	p := writeEnvFile(t, content)
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("加载 .env 应成功: %v", err)
	}
	if got := os.Getenv(k1); got != "quoted value" {
		t.Fatalf("应去双引号并 trim 键值首尾空白，实际 %q", got)
	}
	if got := os.Getenv(k2); got != "single" {
		t.Fatalf("应去单引号，实际 %q", got)
	}
}

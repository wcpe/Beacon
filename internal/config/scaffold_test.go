package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureFileReleasesWhenAbsent 验证目标不存在时释放内容并返回 true。
func TestEnsureFileReleasesWhenAbsent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	released, err := EnsureFile(p, []byte("hello: world\n"))
	if err != nil {
		t.Fatalf("释放应成功: %v", err)
	}
	if !released {
		t.Fatal("文件不存在时应释放并返回 true")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello: world\n" {
		t.Fatalf("释放内容不对: %q", b)
	}
}

// TestEnsureFileSkipsWhenPresent 验证目标已存在时跳过、不覆盖、返回 false。
func TestEnsureFileSkipsWhenPresent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}
	released, err := EnsureFile(p, []byte("new\n"))
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if released {
		t.Fatal("文件已存在时不应释放（应返回 false）")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "original\n" {
		t.Fatalf("已存在文件不应被覆盖，实际 %q", b)
	}
}

// envValue 取生成的 .env 文本里某键的值（到行尾），用于断言非空。
func envValue(s, key string) string {
	idx := strings.Index(s, key)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(key):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}

// TestEnsureBootstrapEnvGeneratesRunnableEnv 验证首启生成的 .env 含非空随机鉴权凭据、可直接通过校验。
func TestEnsureBootstrapEnvGeneratesRunnableEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	generated, err := EnsureBootstrapEnv(p)
	if err != nil || !generated {
		t.Fatalf(".env 不存在时应生成并返回 true：generated=%v err=%v", generated, err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, k := range []string{"BEACON_ADMIN_PASSWORD=", "BEACON_AUTH_SECRET=", "BEACON_BOOTSTRAP_TOKEN="} {
		if envValue(s, k) == "" {
			t.Fatalf("生成的 .env 中 %s 的值不应为空", k)
		}
	}
}

// TestEnsureBootstrapEnvSkipsWhenPresent 验证 .env 已存在时不再生成、不覆盖用户文件。
func TestEnsureBootstrapEnvSkipsWhenPresent(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte("BEACON_ADMIN_PASSWORD=mine\n"), 0o600); err != nil {
		t.Fatalf("预置 .env 失败: %v", err)
	}
	generated, err := EnsureBootstrapEnv(p)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if generated {
		t.Fatal(".env 已存在时不应再生成（应返回 false）")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "BEACON_ADMIN_PASSWORD=mine\n" {
		t.Fatalf("已存在 .env 不应被覆盖，实际 %q", b)
	}
}

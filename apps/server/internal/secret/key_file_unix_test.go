//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secret

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLoadOrCreateCipherRejectsUnixPermissiveKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if _, err := LoadOrCreateCipher(path); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("包含其他用户读取权限的密钥文件应拒绝启动")
	}
}

func TestLoadOrCreateCipherRejectsUnixPermissiveKeyDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if _, err := LoadOrCreateCipher(path); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("包含其他用户访问权限的密钥目录应拒绝启动")
	}
}

func TestLoadOrCreateCipherRejectsUnixNonRegularKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("非普通密钥文件应拒绝启动")
	}
}

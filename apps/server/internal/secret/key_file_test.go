package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateCipherPersistsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	first, err := LoadOrCreateCipher(path)
	if err != nil {
		t.Fatalf("首次加载密钥失败: %v", err)
	}
	ciphertext, err := first.Encrypt("内容")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	second, err := LoadOrCreateCipher(path)
	if err != nil {
		t.Fatalf("再次加载密钥失败: %v", err)
	}
	plaintext, err := second.Decrypt(ciphertext)
	if err != nil || plaintext != "内容" {
		t.Fatalf("持久密钥未能解密: plaintext=%q err=%v", plaintext, err)
	}
}

func TestLoadOrCreateCipherRejectsInvalidKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "bad.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("无效密钥文件应拒绝启动")
	}
}

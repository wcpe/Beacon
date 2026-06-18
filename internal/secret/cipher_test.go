package secret

import (
	"encoding/base64"
	"strings"
	"testing"
)

// testKey 返回一个合法的 base64(32 字节) 测试密钥（仅测试用，非生产凭据）。
func testKey() string {
	raw := make([]byte, KeyBytes)
	for i := range raw {
		raw[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// 加解密往返一致：明文 → Encrypt → Decrypt 得回原文。
func TestCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(testKey())
	if err != nil {
		t.Fatalf("构造 cipher 失败: %v", err)
	}
	plain := "redis-password: s3cr3t-内网口令"
	token, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if token == plain {
		t.Fatal("密文不应等于明文")
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		t.Fatalf("密文应带自描述前缀 %q，实际 %q", tokenPrefix, token)
	}
	got, err := c.Decrypt(token)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != plain {
		t.Fatalf("往返不一致：期望 %q，得到 %q", plain, got)
	}
}

// 相同明文两次加密应得不同密文（随机 nonce），但都能解回原文。
func TestCipher_NonceRandomized(t *testing.T) {
	c, _ := NewCipher(testKey())
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("相同明文两次加密应因随机 nonce 得到不同密文")
	}
	for _, tok := range []string{a, b} {
		if got, err := c.Decrypt(tok); err != nil || got != "same" {
			t.Fatalf("解密应得回原文，得 %q err=%v", got, err)
		}
	}
}

// 空明文也应可往返（不因空串特判出错）。
func TestCipher_EmptyPlaintext(t *testing.T) {
	c, _ := NewCipher(testKey())
	token, err := c.Encrypt("")
	if err != nil {
		t.Fatalf("空明文加密失败: %v", err)
	}
	got, err := c.Decrypt(token)
	if err != nil || got != "" {
		t.Fatalf("空明文往返失败：got=%q err=%v", got, err)
	}
}

// 错误密钥解密：另一把密钥应使 GCM 认证失败，返回错误而非脏明文。
func TestCipher_WrongKeyFails(t *testing.T) {
	c1, _ := NewCipher(testKey())
	token, _ := c1.Encrypt("内网口令")

	other := make([]byte, KeyBytes)
	for i := range other {
		other[i] = byte(255 - i)
	}
	c2, _ := NewCipher(base64.StdEncoding.EncodeToString(other))

	got, err := c2.Decrypt(token)
	if err == nil {
		t.Fatalf("错误密钥应解密失败，却得到 %q", got)
	}
}

// 篡改密文：改一个字节应使 GCM 认证失败。
func TestCipher_TamperedFails(t *testing.T) {
	c, _ := NewCipher(testKey())
	token, _ := c.Encrypt("内网口令")
	// 翻转最后一个字符（仍是合法 base64 字符集内的扰动由 Decrypt 内部捕获）
	body := strings.TrimPrefix(token, tokenPrefix)
	tampered := tokenPrefix + flipLastBase64Char(body)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
}

// 无密钥（空串）构造的 cipher：Encrypt/Decrypt 均返回 ErrKeyMissing，IsEnabled 为 false。
func TestCipher_KeyMissing(t *testing.T) {
	c, err := NewCipher("")
	if err != nil {
		t.Fatalf("空密钥应构造出未启用 cipher 而非报错，得 err=%v", err)
	}
	if c.IsEnabled() {
		t.Fatal("空密钥应得到未启用 cipher")
	}
	if _, err := c.Encrypt("x"); err == nil {
		t.Fatal("未配置密钥时加密应返回错误")
	}
	if _, err := c.Decrypt(tokenPrefix + "abc"); err == nil {
		t.Fatal("未配置密钥时解密应返回错误")
	}
}

// 非法密钥长度：base64 解出非 32 字节应构造失败。
func TestCipher_BadKeyLength(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := NewCipher(short); err == nil {
		t.Fatal("非 32 字节密钥应构造失败")
	}
}

// 非 base64 密钥应构造失败。
func TestCipher_NonBase64Key(t *testing.T) {
	if _, err := NewCipher("这不是合法 base64!!!"); err == nil {
		t.Fatal("非法 base64 密钥应构造失败")
	}
}

// IsEncrypted 仅对带前缀的密文为真，明文为假。
func TestIsEncrypted(t *testing.T) {
	c, _ := NewCipher(testKey())
	token, _ := c.Encrypt("x")
	if !IsEncrypted(token) {
		t.Fatal("密文应被识别为已加密")
	}
	if IsEncrypted("plain text 明文") {
		t.Fatal("明文不应被识别为已加密")
	}
}

// flipLastBase64Char 翻转 base64 串最后一个字符，制造篡改。
func flipLastBase64Char(s string) string {
	if s == "" {
		return "A"
	}
	last := s[len(s)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return s[:len(s)-1] + string(repl)
}

package apikey

import (
	"strings"
	"testing"
)

// TestGenerateShape 校验生成的明文/摘要/展示片段的结构与一致性。
func TestGenerateShape(t *testing.T) {
	plaintext, hash, prefix, err := Generate()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if !strings.HasPrefix(plaintext, Prefix) {
		t.Fatalf("明文应带前缀 %q，实际 %q", Prefix, plaintext)
	}
	if !HasPrefix(plaintext) {
		t.Fatalf("HasPrefix 应识别明文 %q", plaintext)
	}
	// SHA-256 十六进制摘要恒为 64 字符
	if len(hash) != 64 {
		t.Fatalf("摘要应为 64 hex，实际 %d：%s", len(hash), hash)
	}
	// 展示片段是明文的前缀子串、非机密、不含完整随机串
	if prefix != plaintext[:displayPrefixLen] {
		t.Fatalf("展示片段应为明文前 %d 字符，实际 %q", displayPrefixLen, prefix)
	}
	if len(prefix) >= len(plaintext) {
		t.Fatal("展示片段不应等于/超过完整明文")
	}
}

// TestHashStableAndMatches 校验 Hash 幂等且与 Generate 返回的摘要一致。
func TestHashStableAndMatches(t *testing.T) {
	plaintext, hash, _, err := Generate()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if Hash(plaintext) != hash {
		t.Fatal("Hash(明文) 应与 Generate 返回的摘要一致")
	}
	if Hash(plaintext) != Hash(plaintext) {
		t.Fatal("同一明文的 Hash 应幂等")
	}
	if Hash("bk_other") == hash {
		t.Fatal("不同明文的摘要不应相同")
	}
}

// TestGenerateUnique 校验两次生成的明文与摘要互不相同（随机性）。
func TestGenerateUnique(t *testing.T) {
	p1, h1, _, err := Generate()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	p2, h2, _, err := Generate()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if p1 == p2 || h1 == h2 {
		t.Fatal("两次生成的明文/摘要不应相同")
	}
}

// TestHasPrefixRejectsLoginToken 校验登录令牌形态（base64.base64）不被误判为密钥。
func TestHasPrefixRejectsLoginToken(t *testing.T) {
	if HasPrefix("YWRtaW58MTIzNDU2.c2lnbmF0dXJl") {
		t.Fatal("登录令牌不应被识别为 API 密钥")
	}
}

// Package apikey 提供管理面 API 密钥的明文生成与哈希（FR-42，见 ADR-0026）。
// 它是叶子包（仅依赖标准库），供 service 签发/校验密钥时复用。
// 明文为全熵随机串、带可识别前缀；库内只存其 SHA-256 摘要——选 SHA-256 而非 bcrypt，
// 因密钥是 256-bit 随机（非弱口令），无需加盐慢哈希，且定长摘要可建唯一索引做 O(1) 查库。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Prefix 是明文密钥的可识别前缀；中间件据此把"密钥"与"登录令牌"区分开（登录令牌不以此开头）。
const Prefix = "bk_"

// randomBytes 是明文随机部分的字节数（256-bit 熵，足以抵御暴力枚举）。
const randomBytes = 32

// displayPrefixLen 是落库展示片段的长度（含 Prefix），非机密、不能反推完整密钥。
const displayPrefixLen = 9

// Generate 生成一把新密钥，返回明文、明文的 SHA-256 摘要、用于展示的前缀片段。
// 明文仅在此刻可得（调用方一次性返回给用户后即丢弃），库内只存 hash 与 displayPrefix。
func Generate() (plaintext, hash, displayPrefix string, err error) {
	buf := make([]byte, randomBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("生成 API 密钥随机串失败: %w", err)
	}
	plaintext = Prefix + base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, Hash(plaintext), plaintext[:displayPrefixLen], nil
}

// Hash 返回明文密钥的 SHA-256 十六进制摘要（64 hex），与落库 key_hash 对齐。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// HasPrefix 判断字符串是否带 API 密钥前缀（供中间件区分密钥与登录令牌）。
func HasPrefix(s string) bool {
	return strings.HasPrefix(s, Prefix)
}

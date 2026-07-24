// Package secret 提供敏感配置 at-rest 加密的纯加解密原语（AES-256-GCM）。
// 它是叶子包，无外部依赖；密钥由调用方从环境变量注入，本包不读 env、不打日志、不持久化密钥。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeyBytes 是 AES-256 密钥的字节长度（32 字节）。
const KeyBytes = 32

// tokenPrefix 是密文的自描述前缀：标识"已加密 + 算法版本 v1"，便于识别与未来演进。
const tokenPrefix = "enc:v1:"

// ErrKeyMissing 表示在未配置加密密钥的情况下被要求加密 / 解密敏感内容。
// 由 NewCipher("") 产生的未启用 cipher 在加解密时返回，供上层 fail-fast。
var ErrKeyMissing = errors.New("未配置配置加密密钥（BEACON_CONFIG_ENCRYPTION_KEY），无法加解密敏感配置")

// ErrNotEncrypted 表示待解密文本不带合法密文前缀（非本包产出的密文）。
var ErrNotEncrypted = errors.New("待解密文本不是合法密文")

// Cipher 封装 AES-256-GCM 加解密。零值不可用，必须经 NewCipher 构造。
// 当以空密钥构造时为"未启用"状态（gcm 为 nil），加解密一律返回 ErrKeyMissing。
type Cipher struct {
	// AEAD 实例；nil 表示未配置密钥（未启用）
	gcm cipher.AEAD
}

// NewCipher 用 base64 编码的 32 字节密钥构造 Cipher。
// keyB64 为空串时返回"未启用"cipher（不报错，由上层决定是否 fail-fast）；
// 非空但解码失败或长度非 32 字节时返回错误。
func NewCipher(keyB64 string) (*Cipher, error) {
	if strings.TrimSpace(keyB64) == "" {
		return &Cipher{gcm: nil}, nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return nil, fmt.Errorf("配置加密密钥不是合法 base64: %w", err)
	}
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("配置加密密钥须为 %d 字节（base64 解码后），实际 %d 字节", KeyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("构造 AES 分组失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM 失败: %w", err)
	}
	return &Cipher{gcm: gcm}, nil
}

// IsEnabled 报告是否已配置密钥（可执行真正的加解密）。
func (c *Cipher) IsEnabled() bool { return c.gcm != nil }

// Encrypt 加密明文，输出 "enc:v1:" 前缀 + base64(nonce‖密文‖GCM tag)。
// 每次加密使用随机 nonce，故相同明文得到不同密文。未配置密钥返回 ErrKeyMissing。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c.gcm == nil {
		return "", ErrKeyMissing
	}
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	// Seal 把 nonce 作为前缀，密文与认证 tag 追加其后
	sealed := c.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return tokenPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密由 Encrypt 产出的密文。错误密钥 / 密文被篡改时 GCM 认证失败，返回错误而非脏明文。
// 文本不带合法前缀返回 ErrNotEncrypted；未配置密钥返回 ErrKeyMissing。
func (c *Cipher) Decrypt(token string) (string, error) {
	if c.gcm == nil {
		return "", ErrKeyMissing
	}
	if !IsEncrypted(token) {
		return "", ErrNotEncrypted
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, tokenPrefix))
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	nonceSize := c.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("密文长度不足，无法分离 nonce")
	}
	nonce, body := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := c.gcm.Open(nil, nonce, body, nil)
	if err != nil {
		// 错误密钥或密文被篡改：GCM 认证失败，绝不返回脏数据
		return "", fmt.Errorf("解密失败（密钥错误或密文被篡改）: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted 报告文本是否为本包产出的密文（带自描述前缀）。
func IsEncrypted(token string) bool {
	return strings.HasPrefix(token, tokenPrefix)
}

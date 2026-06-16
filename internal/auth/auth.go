// Package auth 提供管理面操作者认证：凭据校验与无状态签名令牌的签发/校验。
// 它是叶子包（仅依赖标准库），供 server 中间件与 handler 共用。
// 令牌用 HMAC-SHA256 签名、不落库、不引第三方件，遵"简单优先"架构不变量。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 认证相关错误（叶子错误，由上层映射为 401）。
var (
	// ErrBadCredentials 用户名或口令不匹配。
	ErrBadCredentials = errors.New("用户名或口令错误")
	// ErrInvalidToken 令牌缺失、结构非法或签名不符。
	ErrInvalidToken = errors.New("令牌非法")
	// ErrTokenExpired 令牌已过有效期。
	ErrTokenExpired = errors.New("令牌已过期")
)

// tokenSep 是令牌"载荷.签名"分隔符。
const tokenSep = "."

// payloadSep 是载荷内"操作者|过期时间"分隔符。
const payloadSep = "|"

// Authenticator 持有单操作者凭据与签名密钥，签发/校验无状态令牌。
// 凭据与密钥由配置注入（口令/密钥走 env），不在代码中硬编码。
type Authenticator struct {
	username string
	password string
	secret   []byte
	tokenTTL time.Duration
}

// New 构造认证器；用户名/口令/密钥任一为空即构造失败（fail-fast，禁空凭据空跑）。
func New(username, password, secret string, tokenTTL time.Duration) (*Authenticator, error) {
	if username == "" {
		return nil, fmt.Errorf("鉴权配置无效: 操作者用户名不能为空")
	}
	if password == "" {
		return nil, fmt.Errorf("鉴权配置无效: 操作者口令不能为空")
	}
	if secret == "" {
		return nil, fmt.Errorf("鉴权配置无效: 令牌签名密钥不能为空")
	}
	return &Authenticator{
		username: username,
		password: password,
		secret:   []byte(secret),
		tokenTTL: tokenTTL,
	}, nil
}

// Login 校验凭据并签发令牌；凭据不匹配返回 ErrBadCredentials。
// 用恒定时间比较避免按字符短路的计时侧信道。
func (a *Authenticator) Login(username, password string) (string, error) {
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
	if !userOK || !passOK {
		return "", ErrBadCredentials
	}
	return a.issue(a.username, time.Now().UTC().Add(a.tokenTTL)), nil
}

// Verify 校验令牌签名与有效期，通过则返回操作者身份。
func (a *Authenticator) Verify(token string) (string, error) {
	payload, sig, ok := splitToken(token)
	if !ok {
		return "", ErrInvalidToken
	}
	if !hmac.Equal(sig, a.sign(payload)) {
		return "", ErrInvalidToken
	}
	operator, expiresUnix, ok := parsePayload(payload)
	if !ok {
		return "", ErrInvalidToken
	}
	if time.Now().UTC().Unix() > expiresUnix {
		return "", ErrTokenExpired
	}
	return operator, nil
}

// issue 组装 "base64(载荷).base64(签名)" 令牌。
func (a *Authenticator) issue(operator string, expires time.Time) string {
	payload := operator + payloadSep + strconv.FormatInt(expires.Unix(), 10)
	sig := a.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + tokenSep +
		base64.RawURLEncoding.EncodeToString(sig)
}

// sign 用密钥对载荷做 HMAC-SHA256。
func (a *Authenticator) sign(payload string) []byte {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// splitToken 拆出载荷明文与签名字节；结构非法返回 ok=false。
func splitToken(token string) (payload string, sig []byte, ok bool) {
	if token == "" {
		return "", nil, false
	}
	parts := strings.Split(token, tokenSep)
	if len(parts) != 2 {
		return "", nil, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", nil, false
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, false
	}
	return string(payloadBytes), sig, true
}

// parsePayload 拆出操作者与过期 Unix 秒；结构非法返回 ok=false。
func parsePayload(payload string) (operator string, expiresUnix int64, ok bool) {
	idx := strings.LastIndex(payload, payloadSep)
	if idx <= 0 {
		return "", 0, false
	}
	operator = payload[:idx]
	expiresUnix, err := strconv.ParseInt(payload[idx+len(payloadSep):], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return operator, expiresUnix, true
}

package auth

import (
	"errors"
	"testing"
	"time"
)

// newTestAuthenticator 构造一个固定凭据/密钥的认证器，令牌有效期 1 小时。
func newTestAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New("admin", "s3cret", "signing-key", time.Hour)
	if err != nil {
		t.Fatalf("构造认证器失败: %v", err)
	}
	return a
}

// TestLoginSuccess 正确凭据登录得到可校验令牌，校验后还原操作者身份。
func TestLoginSuccess(t *testing.T) {
	a := newTestAuthenticator(t)
	token, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("正确凭据登录应成功，实际: %v", err)
	}
	if token == "" {
		t.Fatal("登录应返回非空令牌")
	}
	operator, err := a.Verify(token)
	if err != nil {
		t.Fatalf("有效令牌校验应通过，实际: %v", err)
	}
	if operator != "admin" {
		t.Fatalf("令牌身份应为 admin，实际 %q", operator)
	}
}

// TestLoginWrongPassword 口令错误返回凭据错误。
func TestLoginWrongPassword(t *testing.T) {
	a := newTestAuthenticator(t)
	if _, err := a.Login("admin", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("错误口令应返回 ErrBadCredentials，实际 %v", err)
	}
}

// TestLoginWrongUsername 用户名错误返回凭据错误。
func TestLoginWrongUsername(t *testing.T) {
	a := newTestAuthenticator(t)
	if _, err := a.Login("root", "s3cret"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("错误用户名应返回 ErrBadCredentials，实际 %v", err)
	}
}

// TestVerifyEmptyToken 空令牌校验失败。
func TestVerifyEmptyToken(t *testing.T) {
	a := newTestAuthenticator(t)
	if _, err := a.Verify(""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("空令牌应返回 ErrInvalidToken，实际 %v", err)
	}
}

// TestVerifyTamperedToken 篡改令牌（破坏签名）校验失败。
func TestVerifyTamperedToken(t *testing.T) {
	a := newTestAuthenticator(t)
	token, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	tampered := token + "x"
	if _, err := a.Verify(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("篡改令牌应返回 ErrInvalidToken，实际 %v", err)
	}
}

// TestVerifyWrongSecret 用不同签名密钥签发的令牌无法被本认证器校验。
func TestVerifyWrongSecret(t *testing.T) {
	issuer, err := New("admin", "s3cret", "other-key", time.Hour)
	if err != nil {
		t.Fatalf("构造签发认证器失败: %v", err)
	}
	token, err := issuer.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	verifier := newTestAuthenticator(t)
	if _, err := verifier.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("异密钥令牌应校验失败，实际 %v", err)
	}
}

// TestVerifyExpiredToken 过期令牌校验失败。
func TestVerifyExpiredToken(t *testing.T) {
	a, err := New("admin", "s3cret", "signing-key", -time.Minute)
	if err != nil {
		t.Fatalf("构造认证器失败: %v", err)
	}
	token, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if _, err := a.Verify(token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("过期令牌应返回 ErrTokenExpired，实际 %v", err)
	}
}

// TestNewRejectsEmptyConfig 缺凭据/密钥时构造失败（fail-fast，禁空口令空跑）。
func TestNewRejectsEmptyConfig(t *testing.T) {
	cases := []struct {
		name               string
		user, pass, secret string
	}{
		{"空用户名", "", "p", "s"},
		{"空口令", "u", "", "s"},
		{"空密钥", "u", "p", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.user, c.pass, c.secret, time.Hour); err == nil {
				t.Fatalf("%s 应构造失败", c.name)
			}
		})
	}
}

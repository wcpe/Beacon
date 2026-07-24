package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// EnsureConfigFile 在 path 不存在时把配置模板 template 释放为 config.yml，并把模板里留空的鉴权口令 /
// 签名密钥就地填入 crypto/rand 随机强值（开箱即跑、config.yml 即真源、无固定弱默认口令），文件权限收紧到
// 0600（含随机凭据）；已存在则跳过、绝不覆盖用户文件，返回 false。鉴权凭据不再走自动生成的 .env——
// 避免 .env 静默盖掉 config.yml（生效优先级 真实 env > .env > config.yml）。
func EnsureConfigFile(path string, template []byte) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查配置文件 %s 失败: %w", path, err)
	}
	pwd, err := randString(12)
	if err != nil {
		return false, fmt.Errorf("生成管理员口令失败: %w", err)
	}
	secret, err := randString(32)
	if err != nil {
		return false, fmt.Errorf("生成签名密钥失败: %w", err)
	}
	content := injectCredentials(template, pwd, secret)
	// 释放的 config.yml 含随机口令 / 密钥，权限收紧到 0600
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return false, fmt.Errorf("释放配置文件 %s 失败: %w", path, err)
	}
	return true, nil
}

// injectCredentials 把模板里留空的 auth.password / auth.secret 就地替换为随机强值（保留模板注释与其余内容）。
// 模板这两项固定写作 `password: ""` / `secret: ""`，各仅一处；随机值经 base64url 编码（仅含 A-Za-z0-9-_、
// 不含引号 / 反斜杠），故按 YAML 双引号标量安全嵌入。
func injectCredentials(template []byte, pwd, secret string) []byte {
	s := string(template)
	s = strings.Replace(s, `password: ""`, fmt.Sprintf("password: %q", pwd), 1)
	s = strings.Replace(s, `secret: ""`, fmt.Sprintf("secret: %q", secret), 1)
	return []byte(s)
}

// randString 用 crypto/rand 生成 nBytes 字节随机量，按 base64url（无填充）编码为可读字符串（无 modulo 偏置）。
func randString(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

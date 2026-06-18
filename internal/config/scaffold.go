package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

// EnsureFile 在 path 不存在时把 content 写入（0644）并返回 true；已存在则跳过、绝不覆盖用户文件，返回 false。
// 用于首次启动把内置配置模板（config.yml）释放到当前目录。
func EnsureFile(path string, content []byte) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查文件 %s 失败: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, fmt.Errorf("释放文件 %s 失败: %w", path, err)
	}
	return true, nil
}

// DefaultAgentToken 是 agent 端共享令牌的默认值。它仅防误连、非安全边界（ADR-0009），
// 故用固定默认值（而非随机）——让控制面与 agent 样例配置开箱即匹配，无需逐机同步。
const DefaultAgentToken = "beacon-bootstrap-token"

// bootstrapEnvTemplate 是首启自动生成的 .env 模板；两处 %s 依次填随机口令 / 签名密钥（令牌用固定默认）。
const bootstrapEnvTemplate = `# Beacon 首次启动自动生成的环境变量（口令/密钥随机，sqlite 开箱即跑）。
# 本文件已被 .gitignore 排除，切勿提交 / 外传；改完重启 beacon 生效。

# 管理台操作者用户名
BEACON_ADMIN_USERNAME=admin
# 管理台操作者口令（首启随机生成，登录管理台用；请妥善保存或改为自定义）
BEACON_ADMIN_PASSWORD=%s
# 登录令牌 HMAC 签名密钥（首启随机生成，勿外传）
BEACON_AUTH_SECRET=%s
# agent 端共享令牌（仅防误连、非安全边界；各 agent 的 bootstrap-token 需与此一致，可按需改）
BEACON_BOOTSTRAP_TOKEN=` + DefaultAgentToken + `
# API 与管理台监听端口（二者同端口）
BEACON_HTTP_ADDR=:8848
`

// EnsureBootstrapEnv 在 .env 不存在时生成一份含随机强鉴权凭据的 .env（0600）并返回 true，使首次启动开箱即跑、
// 不再 fail-fast；已存在则跳过、绝不覆盖用户文件，返回 false。随机凭据保证非空且强——不弱化 ADR-0009 的鉴权
// 要求（仍是「强凭据 + env 注入」），只是把"缺凭据 fail-fast 等运维补"改为"首启自助引导生成、运维按需改"。
func EnsureBootstrapEnv(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查 .env 文件 %s 失败: %w", path, err)
	}
	pwd, err := randString(12)
	if err != nil {
		return false, fmt.Errorf("生成管理员口令失败: %w", err)
	}
	secret, err := randString(32)
	if err != nil {
		return false, fmt.Errorf("生成签名密钥失败: %w", err)
	}
	content := fmt.Sprintf(bootstrapEnvTemplate, pwd, secret)
	// .env 含密钥，权限收紧到 0600
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, fmt.Errorf("写入 .env 文件 %s 失败: %w", path, err)
	}
	return true, nil
}

// randString 用 crypto/rand 生成 nBytes 字节随机量，按 base64url（无填充）编码为可读字符串（无 modulo 偏置）。
func randString(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

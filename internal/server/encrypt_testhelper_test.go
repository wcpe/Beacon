//go:build integration

package server_test

import "beacon/internal/secret"

// noEncryptCipher 返回一把未启用的 cipher：本包集成测试不涉及敏感项加密，
// 用未启用 cipher 保持与历史明文行为一致。
func noEncryptCipher() *secret.Cipher {
	c, _ := secret.NewCipher("")
	return c
}

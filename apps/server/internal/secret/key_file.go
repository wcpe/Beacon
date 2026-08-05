package secret

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateCipher 从持久密钥文件加载 AES 密钥；首次启动安全生成并以仅所有者可读写权限保存。
func LoadOrCreateCipher(path string) (*Cipher, error) {
	key, err := loadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	return NewCipher(base64.StdEncoding.EncodeToString(key))
}

func loadOrCreateKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("加密密钥文件路径不能为空")
	}
	if raw, err := os.ReadFile(path); err == nil {
		return decodeKeyFile(path, raw)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取加密密钥文件失败 %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建密钥目录失败 %s: %w", filepath.Dir(path), err)
	}
	key := make([]byte, KeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成加密密钥失败: %w", err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key) + "\n")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, writeErr := file.Write(encoded)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, fmt.Errorf("写入加密密钥文件失败 %s: %w", path, writeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("关闭加密密钥文件失败 %s: %w", path, closeErr)
		}
		return key, nil
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("创建加密密钥文件失败 %s: %w", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取并发创建的加密密钥文件失败 %s: %w", path, err)
	}
	return decodeKeyFile(path, raw)
}

func decodeKeyFile(path string, raw []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != KeyBytes {
		return nil, fmt.Errorf("加密密钥文件无效 %s", path)
	}
	return key, nil
}

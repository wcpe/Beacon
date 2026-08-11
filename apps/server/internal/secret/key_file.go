package secret

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
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
	file, created, err := openPrivateKeyFile(path)
	if err != nil {
		return nil, err
	}
	if created {
		return writeNewKeyFile(path, file)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 1024))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("读取加密密钥文件失败 %s: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("关闭加密密钥文件失败 %s: %w", path, closeErr)
	}
	return decodeKeyFile(path, raw)
}

func writeNewKeyFile(path string, file *os.File) ([]byte, error) {
	key := make([]byte, KeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("生成加密密钥失败: %w", err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key) + "\n")
	_, writeErr := file.Write(encoded)
	syncErr := error(nil)
	if writeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return nil, fmt.Errorf("写入加密密钥文件失败 %s: %w", path, writeErr)
	}
	if syncErr != nil {
		return nil, fmt.Errorf("同步加密密钥文件失败 %s: %w", path, syncErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("关闭加密密钥文件失败 %s: %w", path, closeErr)
	}
	return key, nil
}

func decodeKeyFile(path string, raw []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != KeyBytes {
		return nil, fmt.Errorf("加密密钥文件无效 %s", path)
	}
	return key, nil
}

package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadDotEnv 读取 path 指向的 .env 文件，把其中 KEY=VALUE 注入进程环境变量。
// 仅注入当前未设置的键（真实环境变量优先，不覆盖已显式设置的值），供既有 applyEnv 覆盖链消费。
// 文件不存在视为正常（返回 nil）。仅支持最小语法：跳过空行与 # 整行注释、按首个 = 切分、
// 去除键值首尾空白与值两端成对引号；不支持变量插值 / 多行值 / 行尾内联注释。
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 .env 文件 %s 失败: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue // 真实环境变量优先，不覆盖
		}
		if err := os.Setenv(key, dequote(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("注入环境变量 %s 失败: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("解析 .env 文件 %s 失败: %w", path, err)
	}
	return nil
}

// dequote 去除值两端成对的单 / 双引号（最小处理，不解转义）。
func dequote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

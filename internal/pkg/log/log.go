// Package log 提供全局中文分级日志的初始化（基于标准库 slog）。
package log

import (
	"log/slog"
	"os"
	"strings"
)

// Setup 按配置级别初始化全局 slog 默认日志器（文本格式，输出到 stderr）。
// 级别取值：ERROR / WARN / INFO / DEBUG，大小写不敏感，未知值按 INFO 处理。
func Setup(level string) {
	var lv slog.Level
	switch strings.ToUpper(level) {
	case "DEBUG":
		lv = slog.LevelDebug
	case "WARN":
		lv = slog.LevelWarn
	case "ERROR":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(handler))
}

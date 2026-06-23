// Package log 提供全局中文分级日志的初始化（基于标准库 slog）。
package log

import (
	"log/slog"
	"os"
	"strings"
)

// levelVar 是全局可原子改的日志级别（slog.LevelVar 内部以原子值持级别）。
// Setup 时绑定到 handler，运行期 SetLevel 原子改之即热生效——不重建 logger、不每条日志读 DB（FR-61，见 ADR-0038）。
var levelVar = new(slog.LevelVar)

// Setup 按配置级别初始化全局 slog 默认日志器（文本格式，输出到 stderr）。
// 级别取值：ERROR / WARN / INFO / DEBUG，大小写不敏感，未知值按 INFO 处理。
// handler 的 Level 取 levelVar（动态级别），后续 SetLevel 即时改变过滤阈值，不破坏既有中文文本格式。
func Setup(level string) {
	levelVar.Set(parseLevel(level))
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: levelVar})
	slog.SetDefault(slog.New(handler))
}

// SetLevel 原子改变全局日志级别（运维热改 log.level 时调用，FR-61）。
// 未知值按 INFO 处理（与 Setup 同口径）；即时生效、对后续每条日志的过滤生效。
func SetLevel(level string) {
	levelVar.Set(parseLevel(level))
}

// parseLevel 把级别字符串解析为 slog.Level（大小写不敏感，未知按 INFO）。
func parseLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

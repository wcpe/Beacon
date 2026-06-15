package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormSlogLogger 把 GORM 的日志桥接到项目的中文 slog 输出，避免英文 SQL 日志。
type gormSlogLogger struct {
	level gormlogger.LogLevel
}

// newGormLogger 返回默认级别为 Warn 的 GORM 日志桥接器。
func newGormLogger() gormlogger.Interface {
	return &gormSlogLogger{level: gormlogger.Warn}
}

// LogMode 返回指定级别的副本（GORM 接口要求）。
func (l *gormSlogLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.level = level
	return &clone
}

func (l *gormSlogLogger) Info(_ context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Info {
		slog.Info("数据库", "信息", format(msg, data))
	}
}

func (l *gormSlogLogger) Warn(_ context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Warn {
		slog.Warn("数据库", "告警", format(msg, data))
	}
}

func (l *gormSlogLogger) Error(_ context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Error {
		slog.Error("数据库", "错误", format(msg, data))
	}
}

// Trace 记录单条 SQL 执行：出错按 ERROR、其余按 DEBUG（记录未启用 Info 时不打）。
func (l *gormSlogLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fc()
	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		slog.Error("数据库执行出错", "耗时", elapsed.String(), "影响行数", rows, "SQL", sql, "错误", err)
	case l.level >= gormlogger.Info:
		slog.Debug("数据库执行", "耗时", elapsed.String(), "影响行数", rows, "SQL", sql)
	}
}

// format 按需对 GORM 传入的格式化参数做拼接。
func format(msg string, data []any) string {
	if len(data) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, data...)
}

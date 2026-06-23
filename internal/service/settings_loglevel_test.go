package service

import (
	"log/slog"
	"testing"

	"github.com/wcpe/Beacon/internal/pkg/log"
)

// TestUpdateLogLevelHotApplies 守护 FR-61：Update("log.level") 经原子级别 setter 即时改变日志过滤阈值（不重启）。
// 用 slog.Default().Enabled 反映当前阈值：INFO 下 DEBUG 被滤 → Update 为 DEBUG 后 DEBUG 可见 → 降 WARN 后 INFO 被滤。
func TestUpdateLogLevelHotApplies(t *testing.T) {
	// log.Setup 建立 INFO 级别并把 atomic levelVar 绑定到默认 logger（生产同一路径）。
	log.Setup("INFO")
	if slog.Default().Enabled(nil, slog.LevelDebug) {
		t.Fatal("INFO 级别下 DEBUG 应被过滤（Enabled(DEBUG)=false）")
	}

	svc, _ := newTestSettingsService(t)
	if err := svc.Update(SettingLogLevel, "DEBUG", "admin", ""); err != nil {
		t.Fatalf("更新 log.level 为 DEBUG 失败: %v", err)
	}
	if !slog.Default().Enabled(nil, slog.LevelDebug) {
		t.Fatal("Update log.level=DEBUG 后 DEBUG 应可见（Enabled(DEBUG)=true）—— 原子级别未热生效")
	}

	if err := svc.Update(SettingLogLevel, "WARN", "admin", ""); err != nil {
		t.Fatalf("更新 log.level 为 WARN 失败: %v", err)
	}
	if slog.Default().Enabled(nil, slog.LevelInfo) {
		t.Fatal("Update log.level=WARN 后 INFO 应被过滤（Enabled(INFO)=false）")
	}
}

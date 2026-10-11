package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/pkg/lockguard"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/log"
)

// TestUpdateLogLevelHotApplies 守护 FR-61：Update("log.level") 经原子级别 setter 即时改变日志过滤阈值（不重启）。
// 用 slog.Default().Enabled 反映当前阈值：INFO 下 DEBUG 被滤 → Update 为 DEBUG 后 DEBUG 可见 → 降 WARN 后 INFO 被滤。
func TestUpdateLogLevelHotApplies(t *testing.T) {
	// log.Setup 建立 INFO 级别并把 atomic levelVar 绑定到默认 logger（生产同一路径）。
	log.Setup("INFO")
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("INFO 级别下 DEBUG 应被过滤（Enabled(DEBUG)=false）")
	}

	svc, _ := newTestSettingsService(t)
	if err := svc.Update(SettingLogLevel, "DEBUG", "admin", ""); err != nil {
		t.Fatalf("更新 log.level 为 DEBUG 失败: %v", err)
	}
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Update log.level=DEBUG 后 DEBUG 应可见（Enabled(DEBUG)=true）—— 原子级别未热生效")
	}

	if err := svc.Update(SettingLogLevel, "WARN", "admin", ""); err != nil {
		t.Fatalf("更新 log.level 为 WARN 失败: %v", err)
	}
	if slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Update log.level=WARN 后 INFO 应被过滤（Enabled(INFO)=false）")
	}
}

// TestUpdateLockDBGuardAcceptsParseBoolForms 守护：守卫开关的热改解析必须与校验端同口径。
//
// 回归背景（code review P1）：该键**不在** dangerousSettingKeys 中，故它走的是 `Update`
// 这条活路径（危险键才走 applyDangerousInTx 的 afterCommit）。一度只把 afterCommit 那处
// 改成 parseBoolSetting、Update 处仍留 `value == "true"`——而校验端
// validateSettingValue 对 bool 用 strconv.ParseBool（接受 "TRUE"/"1"/"t"）。
// 于是提交 "TRUE" 会「校验通过、落库成功、设置页显示已开，而守卫实际没开」，正是
// 要消灭的静默失效。本用例锁定四类 ParseBool 可接受写法都能真正打开守卫。
//
// 判别力：把 Update 里的 parseBoolSetting 改回 `value == "true"` → 本用例的 "TRUE"/"1"/"t"
// 三条立即报「守卫未按同口径开启」。
func TestUpdateLockDBGuardAcceptsParseBoolForms(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	t.Cleanup(func() { lockguard.SetEnabled(false) })

	// strconv.ParseBool 接受的全部真值写法，校验端均放行，消费端必须同样认。
	for _, v := range []string{"true", "TRUE", "True", "1", "t", "T"} {
		lockguard.SetEnabled(false) // 先置否，确保观测到的是本次写入的效果
		if err := svc.Update(SettingLockDBGuardEnabled, v, "admin", ""); err != nil {
			t.Fatalf("提交 %q 应被校验端放行，实际 %v", v, err)
		}
		if !lockguard.Enabled() {
			t.Fatalf("提交 %q 后守卫应开启（与 strconv.ParseBool 同口径），实际未开启——热改静默失效", v)
		}
	}

	// 假值写法必须真正关闭，不能被当成真。
	for _, v := range []string{"false", "FALSE", "0", "f"} {
		lockguard.SetEnabled(true)
		if err := svc.Update(SettingLockDBGuardEnabled, v, "admin", ""); err != nil {
			t.Fatalf("提交 %q 应被校验端放行，实际 %v", v, err)
		}
		if lockguard.Enabled() {
			t.Fatalf("提交 %q 后守卫应关闭，实际仍开启", v)
		}
	}
}

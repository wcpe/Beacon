package service

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// TestUpdateRejectsNonWhitelistKey 白名单外 key 写被拒（启动 / 安全项绝不进 store，FR-61）。
func TestUpdateRejectsNonWhitelistKey(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	for _, key := range []string{"http-addr", "auth.password", "agent-token", "database.dsn", "不存在的key"} {
		if err := svc.Update(key, "x", "admin", "127.0.0.1"); err != apperr.ErrSettingKeyNotAllowed {
			t.Fatalf("非白名单 key %q 应被拒 ErrSettingKeyNotAllowed，实际 %v", key, err)
		}
	}
}

// TestUpdateValidatesValue Update 校验类型 / 范围 / 枚举：非法值拒、合法值过。
func TestUpdateValidatesValue(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	bad := []struct {
		key, value string
	}{
		{SettingHealthTTLSec, "abc"},           // int 解析失败
		{SettingHealthTTLSec, "0"},             // 低于下界
		{SettingHealthTTLSec, "999999999"},     // 高于上界
		{SettingMetricEnabled, "yesno"},        // bool 解析失败
		{SettingLogLevel, "TRACE"},             // 枚举外
		{SettingReverseFetchMaxFileBytes, "0"}, // 低于下界（1KB）
	}
	for _, c := range bad {
		if err := svc.Update(c.key, c.value, "admin", "127.0.0.1"); err != apperr.ErrSettingValueInvalid {
			t.Fatalf("%s=%q 应被拒 ErrSettingValueInvalid，实际 %v", c.key, c.value, err)
		}
	}
	good := []struct{ key, value string }{
		{SettingHealthTTLSec, "45"},
		{SettingMetricEnabled, "false"},
		{SettingLogLevel, "DEBUG"},
		{SettingAlertWebhookURL, ""}, // URL 允许空（动态停用 webhook）
	}
	for _, c := range good {
		if err := svc.Update(c.key, c.value, "admin", "127.0.0.1"); err != nil {
			t.Fatalf("%s=%q 应通过，实际 %v", c.key, c.value, err)
		}
	}
}

// TestUpdateWritesAudit Update 入审计 settings.update，detail 仅 key + 新值（不含密钥）。
func TestUpdateWritesAudit(t *testing.T) {
	svc, db := newTestSettingsService(t)
	if err := svc.Update(SettingHealthTTLSec, "45", "alice", "203.0.113.1"); err != nil {
		t.Fatalf("更新应成功，实际 %v", err)
	}
	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionSettingsUpdate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条 settings.update 审计，实际 %d", len(logs))
	}
	got := logs[0]
	if got.Operator != "alice" || got.TargetType != model.TargetTypeSettings || got.TargetRef != SettingHealthTTLSec {
		t.Fatalf("审计 operator/target 不符：%s %s/%s", got.Operator, got.TargetType, got.TargetRef)
	}
	if got.ClientIP != "203.0.113.1" || got.Result != model.ResultOK {
		t.Fatalf("审计 clientIp/result 不符：%s/%s", got.ClientIP, got.Result)
	}
	if !strings.Contains(got.Detail, "45") || !strings.Contains(got.Detail, SettingHealthTTLSec) {
		t.Fatalf("审计 detail 应含 key + 新值，实际 %q", got.Detail)
	}
}

// TestCacheReadAndRefresh 缓存读取：缺则取默认，Update 后即刷新缓存（不重读 DB）。
func TestCacheReadAndRefresh(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	// 空 store：读走白名单默认（与 config 默认一致）。
	if got := svc.GetInt(SettingHealthTTLSec); got != config.Default().Health.TTLSec {
		t.Fatalf("空 store 应取默认 ttl=%d，实际 %d", config.Default().Health.TTLSec, got)
	}
	// Update 后缓存即刷新。
	if err := svc.Update(SettingHealthTTLSec, "77", "admin", ""); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if got := svc.GetInt(SettingHealthTTLSec); got != 77 {
		t.Fatalf("Update 后缓存应刷新为 77，实际 %d", got)
	}
	// bool / string 同理。
	if err := svc.Update(SettingMetricEnabled, "false", "admin", ""); err != nil {
		t.Fatalf("更新 metric.enabled 失败: %v", err)
	}
	if svc.GetBool(SettingMetricEnabled) {
		t.Fatal("Update metric.enabled=false 后 GetBool 应为 false")
	}
}

// TestSeedFromConfigOnlyFillsMissing 首启种子：store 缺该 key 才用 config 值填，已有不覆盖。
func TestSeedFromConfigOnlyFillsMissing(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	// 预先把 ttl 改为 99（模拟运维已改过）。
	if err := svc.Update(SettingHealthTTLSec, "99", "admin", ""); err != nil {
		t.Fatalf("预设失败: %v", err)
	}
	// 用一个 ttl=30 的 config 跑种子：已有 99 不应被覆盖。
	cfg := config.Default()
	cfg.Health.TTLSec = 30
	cfg.Health.DegradedAfterSec = 11
	if err := svc.SeedFromConfig(cfg); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if got := svc.GetInt(SettingHealthTTLSec); got != 99 {
		t.Fatalf("已有 key 不应被 config 覆盖，应保持 99，实际 %d", got)
	}
	// 缺的 key（degraded-after）应被 config 值填充。
	if got := svc.GetInt(SettingHealthDegradedAfterSec); got != 11 {
		t.Fatalf("缺的 key 应被 config 值 11 填充，实际 %d", got)
	}
}

// TestListCoversAllHotKeys List 列出全部热改项，isStartup 恒 false、带默认 + 说明。
func TestListCoversAllHotKeys(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	views := svc.List()
	if len(views) != len(settingsWhitelist) {
		t.Fatalf("List 应覆盖全部 %d 个热改项，实际 %d", len(settingsWhitelist), len(views))
	}
	// 13 项 = ADR-0038 的 12 项 + FR-98 新增 update.proxy-url。
	if len(views) != 13 {
		t.Fatalf("热改白名单应为 13 项，实际 %d", len(views))
	}
	for _, v := range views {
		if v.IsStartup {
			t.Fatalf("热改项 %s 的 isStartup 应恒 false", v.Key)
		}
		if v.Desc == "" || v.ValueType == "" {
			t.Fatalf("热改项 %s 应带类型与说明", v.Key)
		}
	}
}

// TestLogLevelUpdatePersists log.level 更新落库 + 缓存（SetLevel 调用见 settings_loglevel_test）。
func TestLogLevelUpdatePersists(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	if err := svc.Update(SettingLogLevel, "WARN", "admin", ""); err != nil {
		t.Fatalf("更新 log.level 失败: %v", err)
	}
	if got := svc.GetString(SettingLogLevel); got != "WARN" {
		t.Fatalf("log.level 缓存应为 WARN，实际 %q", got)
	}
}

// TestSeedThenConfigChangeIgnored 已 seed 后改 config 不影响运行值（store 为热改项真源，FR-61）。
func TestSeedThenConfigChangeIgnored(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	cfg := config.Default()
	cfg.Health.TTLSec = 30
	if err := svc.SeedFromConfig(cfg); err != nil {
		t.Fatalf("首次种子失败: %v", err)
	}
	if got := svc.GetInt(SettingHealthTTLSec); got != 30 {
		t.Fatalf("首启种子后应为 30，实际 %d", got)
	}
	// 模拟运维改了 config.yml 的热改项后再次启动（store 已有 → 以 store 为准）。
	cfg.Health.TTLSec = 60
	if err := svc.SeedFromConfig(cfg); err != nil {
		t.Fatalf("二次种子失败: %v", err)
	}
	if got := svc.GetInt(SettingHealthTTLSec); got != 30 {
		t.Fatalf("已 seed 后改 config 不应影响运行值，应仍为 30，实际 %d", got)
	}
}

// TestUpsertBumpsVersion 仓库 Upsert 乐观锁：每次更新 version+1。
func TestUpsertBumpsVersion(t *testing.T) {
	_, db := newTestSettingsService(t)
	repo := repository.NewSettingRepository(db)
	first, err := repo.Upsert(SettingHealthTTLSec, "30", model.SettingValueTypeInt)
	if err != nil {
		t.Fatalf("首次 Upsert 失败: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("首次插入 version 应为 1，实际 %d", first.Version)
	}
	second, err := repo.Upsert(SettingHealthTTLSec, "45", model.SettingValueTypeInt)
	if err != nil {
		t.Fatalf("二次 Upsert 失败: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("更新后 version 应为 2，实际 %d", second.Version)
	}
	if second.Value != "45" {
		t.Fatalf("更新后值应为 45，实际 %q", second.Value)
	}
}

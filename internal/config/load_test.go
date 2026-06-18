package config

import "testing"

// setAuthEnv 注入合法的鉴权环境变量，避免校验因缺鉴权凭据而失败（鉴权前移后口令/密钥必填）。
func setAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEACON_ADMIN_PASSWORD", "test-pass")
	t.Setenv("BEACON_AUTH_SECRET", "test-secret")
}

// TestLoadDefaults 验证无文件、仅注入必填鉴权凭据时返回内置默认且校验通过。
func TestLoadDefaults(t *testing.T) {
	setAuthEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载默认配置应成功，却报错: %v", err)
	}
	if cfg.HTTPAddr != ":8848" {
		t.Errorf("默认监听地址应为 :8848，实际 %q", cfg.HTTPAddr)
	}
	if cfg.Database.MaxOpenConns != 1 {
		t.Errorf("默认最大连接数应为 1（SQLite 模式），实际 %d", cfg.Database.MaxOpenConns)
	}
}

// TestEnvOverride 验证环境变量覆盖优先级最高。
func TestEnvOverride(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_HTTP_ADDR", ":9090")
	t.Setenv("BEACON_DB_DSN", "user:pwd@tcp(db:3306)/beacon?parseTime=true&loc=UTC")
	t.Setenv("BEACON_LOG_LEVEL", "DEBUG")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置应成功，却报错: %v", err)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("环境变量未覆盖监听地址，实际 %q", cfg.HTTPAddr)
	}
	if cfg.Log.Level != "DEBUG" {
		t.Errorf("环境变量未覆盖日志级别，实际 %q", cfg.Log.Level)
	}
}

// TestAuthEnvOverride 验证鉴权凭据走环境变量覆盖。
func TestAuthEnvOverride(t *testing.T) {
	t.Setenv("BEACON_ADMIN_USERNAME", "ops")
	t.Setenv("BEACON_ADMIN_PASSWORD", "p@ss")
	t.Setenv("BEACON_AUTH_SECRET", "sig-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载配置应成功，却报错: %v", err)
	}
	if cfg.Auth.Username != "ops" {
		t.Errorf("环境变量未覆盖操作者用户名，实际 %q", cfg.Auth.Username)
	}
	if cfg.Auth.Password != "p@ss" {
		t.Errorf("环境变量未覆盖操作者口令，实际 %q", cfg.Auth.Password)
	}
	if cfg.Auth.Secret != "sig-key" {
		t.Errorf("环境变量未覆盖签名密钥，实际 %q", cfg.Auth.Secret)
	}
}

// TestValidateRejectsMissingAuthPassword 验证缺操作者口令时校验失败（禁空凭据空跑）。
func TestValidateRejectsMissingAuthPassword(t *testing.T) {
	t.Setenv("BEACON_AUTH_SECRET", "sig-key")
	if _, err := Load(""); err == nil {
		t.Fatal("缺操作者口令应导致校验失败，却通过了")
	}
}

// TestValidateRejectsMissingAuthSecret 验证缺签名密钥时校验失败。
func TestValidateRejectsMissingAuthSecret(t *testing.T) {
	t.Setenv("BEACON_ADMIN_PASSWORD", "test-pass")
	if _, err := Load(""); err == nil {
		t.Fatal("缺签名密钥应导致校验失败，却通过了")
	}
}

// TestValidateRejectsUnknownLogLevel 验证未知日志级别被拒绝。
func TestValidateRejectsUnknownLogLevel(t *testing.T) {
	setAuthEnv(t)
	t.Setenv("BEACON_LOG_LEVEL", "VERBOSE")
	if _, err := Load(""); err == nil {
		t.Fatal("未知日志级别应导致校验失败，却通过了")
	}
}

// TestDefaultHealthThresholdsValid 默认健康阈值满足 degraded<ttl<offline 序关系（FR-28）。
func TestDefaultHealthThresholdsValid(t *testing.T) {
	setAuthEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("默认配置应通过校验，却报错: %v", err)
	}
	h := cfg.Health
	if !(h.DegradedAfterSec < h.TTLSec && h.TTLSec < h.OfflineGraceSec) {
		t.Fatalf("默认阈值序关系不满足：degraded=%d ttl=%d offline=%d", h.DegradedAfterSec, h.TTLSec, h.OfflineGraceSec)
	}
	if cfg.Alert.InboxCapacity <= 0 {
		t.Fatalf("默认站内信容量应为正，实际 %d", cfg.Alert.InboxCapacity)
	}
}

// TestValidateRejectsBadHealthThresholdOrder 阈值序关系错误（degraded>=ttl）应被拒（FR-28）。
func TestValidateRejectsBadHealthThresholdOrder(t *testing.T) {
	setAuthEnv(t)
	cfg := Default()
	cfg.Auth.Password = "p"
	cfg.Auth.Secret = "s"
	cfg.Health.DegradedAfterSec = 40 // 大于 ttl(30)，非法
	if err := cfg.validate(); err == nil {
		t.Fatal("degraded-after-sec 不小于 ttl-sec 应导致校验失败，却通过了")
	}
}

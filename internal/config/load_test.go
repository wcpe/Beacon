package config

import "testing"

// TestLoadDefaults 验证无文件无环境变量时返回内置默认且校验通过。
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载默认配置应成功，却报错: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("默认监听地址应为 :8080，实际 %q", cfg.HTTPAddr)
	}
	if cfg.Database.MaxOpenConns != 20 {
		t.Errorf("默认最大连接数应为 20，实际 %d", cfg.Database.MaxOpenConns)
	}
}

// TestEnvOverride 验证环境变量覆盖优先级最高。
func TestEnvOverride(t *testing.T) {
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

// TestValidateRejectsUnknownLogLevel 验证未知日志级别被拒绝。
func TestValidateRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("BEACON_LOG_LEVEL", "VERBOSE")
	if _, err := Load(""); err == nil {
		t.Fatal("未知日志级别应导致校验失败，却通过了")
	}
}

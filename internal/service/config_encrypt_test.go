package service_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"beacon/internal/config"
	"beacon/internal/merge"
	"beacon/internal/repository"
	"beacon/internal/runtime/longpoll"
	"beacon/internal/secret"
	"beacon/internal/service"
	"beacon/internal/store"
)

// enabledCipher 构造一把启用的测试 cipher（仅测试用）。
func enabledCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	raw := make([]byte, secret.KeyBytes)
	for i := range raw {
		raw[i] = byte(i*3 + 1)
	}
	c, err := secret.NewCipher(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("构造 cipher 失败: %v", err)
	}
	return c
}

// encryptSqliteStack 用内存 sqlite + 启用 cipher 装配 ConfigService + EffectiveService。
func encryptSqliteStack(t *testing.T) (*service.ConfigService, *service.EffectiveService) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })

	cipher := enabledCipher(t)
	configRepo := repository.NewConfigItemRepository(db, cipher)
	revRepo := repository.NewConfigRevisionRepository(db, cipher)
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)

	cfgSvc := service.NewConfigService(db, configRepo, revRepo, auditRepo)
	effSvc := service.NewEffectiveService(configRepo, assignRepo, longpoll.NewHub())
	return cfgSvc, effSvc
}

// 敏感项经发布（加密落库）后，读取详情得回明文，且 md5 = 明文 md5（解密后再算）。
func TestConfigService_SensitiveRoundTripAndMD5(t *testing.T) {
	cfgSvc, _ := encryptSqliteStack(t)
	plain := "redis:\n  host: 127.0.0.1\n  password: s3cr3t\n"
	wantMD5 := merge.MD5Hex(plain)

	it, err := cfgSvc.Create(service.CreateConfigParams{
		Namespace: "prod", DataID: "redis.yml", ScopeLevel: "global",
		Format: "yaml", Content: plain, Operator: "tester", Sensitive: true,
	})
	if err != nil {
		t.Fatalf("创建敏感项失败: %v", err)
	}
	if it.ContentMD5 != wantMD5 {
		t.Fatalf("敏感项 md5 应基于明文，期望 %s 得 %s", wantMD5, it.ContentMD5)
	}

	// 读详情：得回明文
	got, err := cfgSvc.Get(it.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Content != plain {
		t.Fatalf("读详情应得回明文，期望 %q 得 %q", plain, got.Content)
	}
	if !got.Sensitive {
		t.Fatal("应保留 Sensitive 标记")
	}

	// 历史版本快照也应解密回明文
	rev, err := cfgSvc.GetRevision(it.ID, 1)
	if err != nil {
		t.Fatalf("读版本失败: %v", err)
	}
	if rev.Content != plain {
		t.Fatalf("版本快照应解密回明文，期望 %q 得 %q", plain, rev.Content)
	}
}

// 有效配置解析对敏感项：下发链路拿到明文，整体 md5 与明文等价。
func TestConfigService_SensitiveEffectiveResolveMD5(t *testing.T) {
	cfgSvc, effSvc := encryptSqliteStack(t)
	plain := "redis:\n  password: s3cr3t\n"

	if _, err := cfgSvc.Create(service.CreateConfigParams{
		Namespace: "prod", DataID: "redis.yml", ScopeLevel: "global",
		Format: "yaml", Content: plain, Operator: "tester", Sensitive: true,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	eff, err := effSvc.Resolve("prod", "srv-1", "groupA")
	if err != nil {
		t.Fatalf("有效配置解析失败: %v", err)
	}
	if len(eff.Items) != 1 {
		t.Fatalf("应解析出 1 个 dataId，实际 %d", len(eff.Items))
	}
	// 加密对有效配置解析透明：下发内容与同样明文走非敏感链路（合并再序列化）等价。
	// Resolve 路径会对内容做规整重序列化，故期望值取 merge 的规范化结果而非原始字节。
	wantContent, err := merge.MergeDataID("yaml", []string{plain})
	if err != nil {
		t.Fatalf("计算期望合并内容失败: %v", err)
	}
	if eff.Items[0].Content != wantContent {
		t.Fatalf("下发内容应为解密后的明文合并结果，期望 %q 得 %q", wantContent, eff.Items[0].Content)
	}
	// 下发内容里必须含明文口令（agent 不持密钥，拿到的是明文）
	if !strings.Contains(eff.Items[0].Content, "s3cr3t") {
		t.Fatalf("下发内容应含明文口令，得 %q", eff.Items[0].Content)
	}
	// 整体 md5 与明文解析等价（与非敏感项行为一致）
	wantItemMD5 := merge.MD5Hex(wantContent)
	if eff.Items[0].MD5 != wantItemMD5 {
		t.Fatalf("dataId md5 应基于解密后明文，期望 %s 得 %s", wantItemMD5, eff.Items[0].MD5)
	}
}

// 发布新版本后敏感项仍正确加解密，md5 跟随明文变化。
func TestConfigService_SensitivePublishNewVersion(t *testing.T) {
	cfgSvc, _ := encryptSqliteStack(t)
	v1 := "redis:\n  password: old\n"
	it, err := cfgSvc.Create(service.CreateConfigParams{
		Namespace: "prod", DataID: "redis.yml", ScopeLevel: "global",
		Format: "yaml", Content: v1, Operator: "tester", Sensitive: true,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	v2 := "redis:\n  password: new\n"
	updated, err := cfgSvc.Publish(it.ID, v2, "tester", "轮换口令", "")
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if updated.ContentMD5 != merge.MD5Hex(v2) {
		t.Fatalf("新版本 md5 应基于新明文，期望 %s 得 %s", merge.MD5Hex(v2), updated.ContentMD5)
	}
	got, _ := cfgSvc.Get(it.ID)
	if got.Content != v2 {
		t.Fatalf("当前内容应为新明文，期望 %q 得 %q", v2, got.Content)
	}
	// 旧版本快照仍能解回旧明文
	rev1, _ := cfgSvc.GetRevision(it.ID, 1)
	if rev1.Content != v1 {
		t.Fatalf("v1 快照应解回旧明文，期望 %q 得 %q", v1, rev1.Content)
	}
}

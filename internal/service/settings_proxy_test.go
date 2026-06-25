package service

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
)

// TestProxyURLValidation update.proxy-url 校验：空串=直连合法、合法 http(s) 过、非法拒（FR-98，见 ADR-0047）。
func TestProxyURLValidation(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	good := []string{
		"",                               // 空=直连
		"http://proxy.example.com:8080",  // 无凭据
		"https://proxy.example.com:3128", // https
		"http://user:pass@proxy:8080",    // 含凭据
	}
	for _, v := range good {
		if err := svc.Update(SettingUpdateProxyURL, v, "admin", ""); err != nil {
			t.Fatalf("proxy-url=%q 应通过，实际 %v", v, err)
		}
	}
	bad := []string{
		"ftp://proxy:21",      // 非 http(s)
		"socks5://proxy:1080", // 不支持 socks5
		"http://proxy",        // 缺端口
		"://nohost",           // 缺 scheme
		"not a url",           // 解析不出 host
	}
	for _, v := range bad {
		if err := svc.Update(SettingUpdateProxyURL, v, "admin", ""); err != apperr.ErrSettingValueInvalid {
			t.Fatalf("proxy-url=%q 应被拒 ErrSettingValueInvalid，实际 %v", v, err)
		}
	}
}

// TestProxyURLAuditRedacted 含凭据 proxy-url 的审计 detail 走脱敏：不含原口令、含掩码（FR-98）。
func TestProxyURLAuditRedacted(t *testing.T) {
	svc, db := newTestSettingsService(t)
	if err := svc.Update(SettingUpdateProxyURL, "http://user:topsecret@proxy:8080", "alice", ""); err != nil {
		t.Fatalf("更新 proxy-url 失败: %v", err)
	}
	var logs []model.AuditLog
	if err := db.Where("action = ? AND target_ref = ?", model.ActionSettingsUpdate, SettingUpdateProxyURL).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条 proxy-url 审计，实际 %d", len(logs))
	}
	detail := logs[0].Detail
	if strings.Contains(detail, "topsecret") || strings.Contains(detail, "user:") {
		t.Fatalf("审计 detail 不应含原凭据，实际 %q", detail)
	}
	if !strings.Contains(detail, "***") {
		t.Fatalf("审计 detail 应含脱敏掩码 ***，实际 %q", detail)
	}
	// store 落原值供运行：缓存读出的仍是真实凭据。
	if got := svc.GetString(SettingUpdateProxyURL); got != "http://user:topsecret@proxy:8080" {
		t.Fatalf("store 应存原值供运行，实际 %q", got)
	}
}

// TestProxyURLListRedacted GET（List）回显含凭据 proxy-url 时脱敏，不泄露明文口令（FR-98）。
func TestProxyURLListRedacted(t *testing.T) {
	svc := settingsWith(t, map[string]string{SettingUpdateProxyURL: "http://user:hunter2@proxy:8080"})
	for _, v := range svc.List() {
		if v.Key != SettingUpdateProxyURL {
			continue
		}
		if strings.Contains(v.Value, "hunter2") {
			t.Fatalf("List 回显不应含明文口令，实际 %q", v.Value)
		}
		if v.Value != "http://***:***@proxy:8080" {
			t.Fatalf("List 回显应为脱敏值，实际 %q", v.Value)
		}
		return
	}
	t.Fatal("List 未包含 update.proxy-url")
}

// TestProxyURLUnchangedPlaceholderPreservesOriginal 「未改密码」语义：提交脱敏占位则保留原值不覆盖（FR-98）。
func TestProxyURLUnchangedPlaceholderPreservesOriginal(t *testing.T) {
	svc := settingsWith(t, map[string]string{SettingUpdateProxyURL: "http://user:s3cr3t@proxy:8080"})
	// 用户原样提交脱敏占位（没改密码）→ 保留原值。
	if err := svc.Update(SettingUpdateProxyURL, "http://***:***@proxy:8080", "admin", ""); err != nil {
		t.Fatalf("提交脱敏占位不应报错，实际 %v", err)
	}
	if got := svc.GetString(SettingUpdateProxyURL); got != "http://user:s3cr3t@proxy:8080" {
		t.Fatalf("提交脱敏占位应保留原值，实际 %q", got)
	}
	// 改成真正的新值 → 正常覆盖。
	if err := svc.Update(SettingUpdateProxyURL, "http://user:newpass@proxy:8080", "admin", ""); err != nil {
		t.Fatalf("更新新值失败: %v", err)
	}
	if got := svc.GetString(SettingUpdateProxyURL); got != "http://user:newpass@proxy:8080" {
		t.Fatalf("应覆盖为新值，实际 %q", got)
	}
}

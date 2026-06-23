package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// webhook 相关热改设置 key（与 service.SettingsService 同字面值，FR-61）。
// 在 alert 包本地声明避免 alert→service 反向依赖（守循环依赖红线）。
const (
	keyAlertWebhookURL       = "alert.webhook-url"
	keyAlertWebhookTimeoutMs = "alert.webhook-timeout-ms"
)

// WebhookSettings 是 webhook 通道对运维设置的窄读依赖（由 service.SettingsService 实现，FR-61）。
// 每次分发读最新 url / timeout 即热生效：url 改了立刻指向新地址、置空则停发（见 ADR-0038）。
type WebhookSettings interface {
	GetString(key string) string
	GetInt(key string) int
}

// WebhookAlerter 是 webhook 告警通道：把告警 JSON POST 到运维预设 URL，带超时。
// url / timeout 不再启动期固定：每次 Notify 从设置 store 读最新值热生效（url 空则跳过、不发），
// 故本通道恒挂载、靠 url 空与否动态启停（FR-61，见 ADR-0038）。
// HTTP IO 由调用方（健康扫描循环）在任何注册表锁之外触发，不阻塞主路径（见 ADR-0019）。
type WebhookAlerter struct {
	settings WebhookSettings
}

// NewWebhookAlerter 构造 webhook 通道（settings 提供热改的 url / timeout）。
func NewWebhookAlerter(settings WebhookSettings) *WebhookAlerter {
	return &WebhookAlerter{settings: settings}
}

// Name 返回通道名。
func (w *WebhookAlerter) Name() string { return "webhook" }

// Notify 以 application/json POST 告警；非 2xx 视为失败由 Dispatcher 兜错。
// url 空表示未启用 webhook 通道：直接返回 nil（不发、不报错，等同无该通道）。
func (w *WebhookAlerter) Notify(ctx context.Context, a Alert) error {
	url := w.settings.GetString(keyAlertWebhookURL)
	if url == "" {
		return nil // webhook 未配置 url，跳过（动态停用）
	}
	timeout := time.Duration(w.settings.GetInt(keyAlertWebhookTimeoutMs)) * time.Millisecond
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回非 2xx 状态码: %d", resp.StatusCode)
	}
	return nil
}

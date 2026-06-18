package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookAlerter 是 webhook 告警通道：把告警 JSON POST 到运维预设 URL，带超时。
// HTTP IO 由调用方（健康扫描循环）在任何注册表锁之外触发，不阻塞主路径（见 ADR-0019）。
type WebhookAlerter struct {
	url    string
	client *http.Client
}

// NewWebhookAlerter 构造 webhook 通道（timeout 为单次请求超时）。
func NewWebhookAlerter(url string, timeout time.Duration) *WebhookAlerter {
	return &WebhookAlerter{
		url:    url,
		client: &http.Client{Timeout: timeout},
	}
}

// Name 返回通道名。
func (w *WebhookAlerter) Name() string { return "webhook" }

// Notify 以 application/json POST 告警；非 2xx 视为失败由 Dispatcher 兜错。
func (w *WebhookAlerter) Notify(ctx context.Context, a Alert) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回非 2xx 状态码: %d", resp.StatusCode)
	}
	return nil
}

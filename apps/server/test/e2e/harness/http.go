//go:build e2e

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Login 用管理员口令换登录令牌（FR-11）；失败返回错误而非 fatal，由调用方决定如何处理。
func Login(baseURL, user, pass string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(strings.TrimRight(baseURL, "/")+"/admin/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("登录请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("登录失败：HTTP %d %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		return "", fmt.Errorf("登录响应无 token：%v", err)
	}
	return out.Token, nil
}

// WaitInstanceOnline 轮询 /admin/v1/instances?namespace=<ns> 直到目标 serverID 状态为 online。
// 超时返回错误（调用方据此 t.Fatalf）。首跑含下载/构建服务端时调用方应给足超时。
func WaitInstanceOnline(baseURL, token, namespace, serverID string, timeout time.Duration) error {
	url := strings.TrimRight(baseURL, "/") + "/admin/v1/instances?namespace=" + namespace
	ok := waitReady(timeout, func() bool {
		var resp struct {
			Items []struct {
				ServerID string `json:"serverId"`
				Status   string `json:"status"`
			} `json:"items"`
		}
		if !tryAdminGet(url, token, &resp) {
			return false
		}
		for _, it := range resp.Items {
			if it.ServerID == serverID && it.Status == "online" {
				return true
			}
		}
		return false
	})
	if !ok {
		return fmt.Errorf("等待 agent 实例 %s online 超时", serverID)
	}
	return nil
}

// OfflineInstance 经 admin API 强制把某实例标记下线（POST /admin/v1/instances/{serverId}/offline）。
// 用途：服务端重启相位前，先把上一相位的残留实例标记下线，消除"陈旧 online"竞态——
// 否则控制面健康 TTL 未过期仍显示 online，会让随后的 WaitInstanceOnline 提前返回。
func OfflineInstance(baseURL, token, namespace, serverID string) error {
	url := strings.TrimRight(baseURL, "/") + "/admin/v1/instances/" + serverID + "/offline?namespace=" + namespace
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("构造下线请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("下线请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("下线失败：HTTP %d %s", resp.StatusCode, string(raw))
	}
	return nil
}

// CancelOfflineInstance 经 admin API 取消某实例的主动下线标记（DELETE /admin/v1/instances/{serverId}/offline）。
// 用途：FR-49 后「下线」是粘性拒绝态——强制下线清掉陈旧 online 后须随即取消，否则后续全新注册会被 403 拒、永不 online。
func CancelOfflineInstance(baseURL, token, namespace, serverID string) error {
	url := strings.TrimRight(baseURL, "/") + "/admin/v1/instances/" + serverID + "/offline?namespace=" + namespace
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("构造取消下线请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("取消下线请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("取消下线失败：HTTP %d %s", resp.StatusCode, string(raw))
	}
	return nil
}

// tryAdminGet 发一个带 Bearer 的 admin GET，仅在 200 且能解析时返回 true（用于轮询，不报错）。
func tryAdminGet(url, token string, out any) bool {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(raw, out) == nil
}

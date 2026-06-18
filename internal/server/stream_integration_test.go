//go:build integration

package server_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sseEvent 是从流上解析出的一条 SSE 事件（event 类型 + data 行原文）。
type sseEvent struct {
	typ  string
	data string
}

// openStream 打开 SSE 推送流并返回事件通道 + 取消函数；读取在后台 goroutine，按帧（空行分隔）切分。
// 注释行（: 开头，保活心跳）被跳过，不投递为事件。
func openStream(t *testing.T, baseURL, query string) (<-chan sseEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/beacon/v1/agent/stream?"+query, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("打开 SSE 流失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("SSE 流应 200，实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("SSE 流 Content-Type 应为 text/event-stream，实际 %q", ct)
	}

	events := make(chan sseEvent, 16)
	go func() {
		defer resp.Body.Close()
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				// 帧结束：投递已积累的事件（注释行心跳无 event 类型，跳过）。
				if cur.typ != "" {
					select {
					case events <- cur:
					case <-ctx.Done():
						return
					}
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				cur.typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return events, cancel
}

// nextEvent 在期限内取下一条事件；超时则失败。
func nextEvent(t *testing.T, events <-chan sseEvent, timeout time.Duration) sseEvent {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("SSE 流已关闭，未取到事件")
		}
		return e
	case <-time.After(timeout):
		t.Fatal("等待 SSE 事件超时")
		return sseEvent{}
	}
}

// registerAgent 注册一个 agent 实例（SSE 流要求先注册）。
func registerAgent(t *testing.T, baseURL, serverID, group string) {
	t.Helper()
	code, _ := doJSON(t, http.MethodPost, baseURL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": serverID, "role": "bukkit",
		"groupHint": group, "address": "10.0.0.1:25565",
	})
	if code != http.StatusOK {
		t.Fatalf("注册 %s 应 200，实际 %d", serverID, code)
	}
}

// TestStreamNotRegistered 未注册即开流 → 404 NOT_REGISTERED（与长轮询一致）。
func TestStreamNotRegistered(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/beacon/v1/agent/stream?namespace=prod&serverId=ghost")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未注册开流应 404，实际 %d", resp.StatusCode)
	}
}

// TestStreamConnectReconcile 连接即对账：agent 上报空 md5、服务端已有配置 → 立即补发 config-changed，再发 ready。
func TestStreamConnectReconcile(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerAgent(t, ts.URL, "lobby-1", "area1")

	// 建一个 global 配置（服务端有内容，agent 上报空 md5 → 落后）
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "app.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "admin",
	}); code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}

	events, cancel := openStream(t, ts.URL, "namespace=prod&serverId=lobby-1")
	defer cancel()

	// 首事件应为 config-changed（对账补发落下的配置增量）。
	first := nextEvent(t, events, 3*time.Second)
	if first.typ != "config-changed" {
		t.Fatalf("连接即对账应先补发 config-changed，实际 %q", first.typ)
	}
	if !strings.Contains(first.data, "md5") {
		t.Fatalf("config-changed 应携带新 md5，实际 %q", first.data)
	}
	// 对账补发完应发 ready。
	ready := nextEvent(t, events, 3*time.Second)
	if ready.typ != "ready" {
		t.Fatalf("对账后应发 ready，实际 %q", ready.typ)
	}
}

// TestStreamLivePushOnPublish 转直播后发布配置 → 受影响 agent 经 SSE 收到 config-changed。
func TestStreamLivePushOnPublish(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerAgent(t, ts.URL, "lobby-1", "area1")

	// 先建并发布一版，取当前 md5 作为 agent 已对齐的起点（开流时上报它 → 对账无补发，直接 ready）。
	code, created := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "app.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "admin",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}
	id := int(created["id"].(float64))

	// agent 已对齐：先开一条流读到当前 config md5，再以该 md5 重开（对账无补发、直接 ready）。
	// 简化：第一条流直接拿对账补发事件里的 md5。
	probe, probeCancel := openStream(t, ts.URL, "namespace=prod&serverId=lobby-1")
	cfgEvent := nextEvent(t, probe, 3*time.Second) // config-changed
	_ = nextEvent(t, probe, 3*time.Second)         // ready
	probeCancel()
	curMD5 := extractMD5(cfgEvent.data)
	if curMD5 == "" {
		t.Fatalf("应能从 config-changed 取到 md5，实际 data=%q", cfgEvent.data)
	}

	// 用已对齐 md5 重开流：对账无补发，直接 ready，随后转直播。
	events, cancel := openStream(t, ts.URL, "namespace=prod&serverId=lobby-1&configMd5="+curMD5)
	defer cancel()
	ready := nextEvent(t, events, 3*time.Second)
	if ready.typ != "ready" {
		t.Fatalf("已对齐应直接 ready（无补发），实际先收到 %q", ready.typ)
	}

	// 直播阶段发布新内容 → 应近实时收到 config-changed。
	if code, _ := doJSON(t, http.MethodPut, ts.URL+"/admin/v1/configs/"+itoa(id), map[string]any{
		"content": "k: 2\n", "operator": "admin",
	}); code != http.StatusOK {
		t.Fatalf("发布应 200，实际 %d", code)
	}
	live := nextEvent(t, events, 3*time.Second)
	if live.typ != "config-changed" {
		t.Fatalf("直播阶段发布应推 config-changed，实际 %q", live.typ)
	}
	if extractMD5(live.data) == curMD5 {
		t.Fatalf("直播事件应携带变更后的新 md5，实际仍为旧 md5 %q", curMD5)
	}
}

// TestStreamOnlyAffected 他组发布不推给未受影响 agent：发布 area2 不应让 area1 的 s1 收到事件。
func TestStreamOnlyAffected(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerAgent(t, ts.URL, "s1", "area1")

	mkGroup := func(group, content string) {
		if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
			"namespace": "prod", "group": group, "dataId": "app.yml",
			"scopeLevel": "group", "format": "yaml", "content": content, "operator": "admin",
		}); code != http.StatusCreated {
			t.Fatalf("建 %s 组配置应 201，实际 %d", group, code)
		}
	}
	mkGroup("area1", "v: 1\n")
	mkGroup("area2", "v: 1\n")

	// s1 先对齐 area1 当前 md5（拿对账补发的 config md5 后重开）。
	probe, probeCancel := openStream(t, ts.URL, "namespace=prod&serverId=s1")
	cfgEvent := nextEvent(t, probe, 3*time.Second)
	_ = nextEvent(t, probe, 3*time.Second) // ready
	probeCancel()
	curMD5 := extractMD5(cfgEvent.data)

	events, cancel := openStream(t, ts.URL, "namespace=prod&serverId=s1&configMd5="+curMD5)
	defer cancel()
	if r := nextEvent(t, events, 3*time.Second); r.typ != "ready" {
		t.Fatalf("已对齐应直接 ready，实际 %q", r.typ)
	}

	// 改 area2 组配置（s1 在 area1，不受影响）：取 area2 配置 id 并发布。
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/configs?namespace=prod&group=area2", nil)
	if code != http.StatusOK {
		t.Fatalf("查 area2 配置应 200，实际 %d", code)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("area2 应有 1 个配置，实际 %d", len(items))
	}
	area2ID := int(items[0].(map[string]any)["id"].(float64))
	if code, _ := doJSON(t, http.MethodPut, ts.URL+"/admin/v1/configs/"+itoa(area2ID), map[string]any{
		"content": "v: 99\n", "operator": "admin",
	}); code != http.StatusOK {
		t.Fatalf("发布 area2 应 200，实际 %d", code)
	}

	// s1 不应收到任何事件（短窗口内）。
	select {
	case e := <-events:
		t.Fatalf("他组发布不应推给 s1，却收到 %q", e.typ)
	case <-time.After(700 * time.Millisecond):
		// 期望：无事件。
	}
}

// extractMD5 从 data 行 JSON（{"md5":"x"}）粗取 md5 值（测试用，不引 json 解析）。
func extractMD5(data string) string {
	const k = `"md5":"`
	i := strings.Index(data, k)
	if i < 0 {
		return ""
	}
	rest := data[i+len(k):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

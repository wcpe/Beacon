//go:build integration

package server_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 集成测试共享的身份夹具缓存：按 (服务地址, 环境, serverId) 记住 namespace token 与鉴权头。
// 同一用例内多次取头必须复用同一身份——重复 v2 注册会撞 pending / 冲突态。
var agentFixtureCache = struct {
	mu      sync.Mutex
	tokens  map[string]string
	headers map[string]map[string]string
}{tokens: map[string]string{}, headers: map[string]map[string]string{}}

// namespaceTokenForTest 取指定环境可用的 namespace token。
//
// 为什么需要它：v1 数据面在 issue #146 之后一律要求「token↔namespace + identity 已确认」的权威身份，
// 而 /admin/v1/namespaces 建的旧环境不回带明文 token。故：
//   - 环境已存在 → 经 v2 轮换端点取回一把新的明文 token；
//   - 环境不存在 → 经 v2 建环境端点建立并直接拿到 token（lifecycle 为空判 active，运行态闸放行）。
func namespaceTokenForTest(t *testing.T, ts *integrationTestServer, code string) string {
	t.Helper()
	agentFixtureCache.mu.Lock()
	defer agentFixtureCache.mu.Unlock()
	cacheKey := ts.URL + "|" + code
	if token, ok := agentFixtureCache.tokens[cacheKey]; ok {
		return token
	}

	var ns model.Namespace
	err := ts.db.Where("code = ?", code).First(&ns).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("读取环境 %s 失败: %v", code, err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/namespaces", map[string]any{"name": code})
		if status != http.StatusCreated {
			t.Fatalf("建环境 %s 应 201，实际 %d：%v", code, status, body)
		}
		token, _ := body["accessToken"].(string)
		if token == "" {
			t.Fatalf("建环境 %s 响应缺 accessToken：%v", code, body)
		}
		agentFixtureCache.tokens[cacheKey] = token
		return token
	}

	status, body := doJSON(t, http.MethodPost, fmt.Sprintf("%s/admin/v2/namespaces/%d/token/rotate", ts.URL, ns.ID), map[string]any{
		"reason": "集成测试取回 agent 数据面鉴权 token",
	})
	if status != http.StatusOK {
		t.Fatalf("轮换环境 %s token 应 200，实际 %d：%v", code, status, body)
	}
	token, _ := body["accessToken"].(string)
	if token == "" {
		t.Fatalf("轮换环境 %s token 响应缺 accessToken：%v", code, body)
	}
	agentFixtureCache.tokens[cacheKey] = token
	return token
}

// bindAgentForTest 为 <namespace, serverID> 建立一套已确认的 v2 身份，
// 并返回该实例在 v1 数据面请求时必须携带的鉴权头（token + identity + boot）。
//
// 这些头正是 v1 端点做权威归属校验的依据：缺任一项都会被拒（401 / 404），
// 故集成测试不能再像过去那样只带共享 token 直连 v1 数据面。
// identityId 由 (环境, serverId) 稳定派生，保证同一用例内多次调用拿到同一身份。
func bindAgentForTest(t *testing.T, ts *integrationTestServer, code, serverID string) map[string]string {
	t.Helper()
	cacheKey := ts.URL + "|" + code + "|" + serverID
	agentFixtureCache.mu.Lock()
	if headers, ok := agentFixtureCache.headers[cacheKey]; ok {
		agentFixtureCache.mu.Unlock()
		return headers
	}
	agentFixtureCache.mu.Unlock()

	token := namespaceTokenForTest(t, ts, code)
	identityID := fmt.Sprintf("a1000000-0000-4000-8000-%012x", stableIdentitySuffix(code+"/"+serverID))
	activateAgent(t, ts, token, identityID, serverID)
	headers := map[string]string{
		"X-Beacon-Token":    token,
		"X-Beacon-Identity": identityID,
		"X-Beacon-Boot":     "boot-1",
	}

	agentFixtureCache.mu.Lock()
	agentFixtureCache.headers[cacheKey] = headers
	agentFixtureCache.mu.Unlock()
	return headers
}

// stableIdentitySuffix 把任意字符串散列成 48 位以内的小整数，用作确定性 UUID 末段。
func stableIdentitySuffix(s string) uint64 {
	var h uint64 = 1469598103934665603 // FNV-1a 偏移基
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h & 0xFFFFFFFFFFFF
}

// doAgentAuto 是「带权威身份直连 v1 数据面」的通用请求入口，替代原先的 doJSON。
//
// v1 数据面各端点现在都要求「token↔namespace + identity 已确认」（issue #146），
// 故调用方不必再关心头怎么拼：本函数从 URL query 或请求体派生目标归属（namespace / serverId），
// 自动为该实例建立（或复用）一套已确认身份并附上鉴权头。
// 归属解析顺序：URL query → 请求体 → **按 commandId 反查命令的真实归属**。
// 最后一步是必需的：ingest / scan / error / commands/result 的请求体只有 commandId，
// 无法自报归属；若退化成固定默认身份，合法回传会因为与命令归属不符而被拒。
func doAgentAuto(t *testing.T, ts *integrationTestServer, method, rawURL string, body any) (int, map[string]any) {
	t.Helper()
	namespace, serverID := agentTargetFromRequest(rawURL, body)
	if namespace == "" || serverID == "" {
		namespace, serverID = commandOwnerForTest(t, ts, body)
	}
	return doAgentJSON(t, method, rawURL, bindAgentForTest(t, ts, namespace, serverID), body)
}

// commandOwnerForTest 按请求体里的 commandId 反查命令的实际归属，供只带 commandId 的回传端点签名。
// 查不到时退化为默认归属，让该用例按原有路径失败并给出可读原因（而不是在脚手架里 panic）。
func commandOwnerForTest(t *testing.T, ts *integrationTestServer, body any) (string, string) {
	t.Helper()
	m, ok := body.(map[string]any)
	if !ok {
		return "prod", "agent-fixture-1"
	}
	raw, ok := m["commandId"]
	if !ok {
		return "prod", "agent-fixture-1"
	}
	id, ok := raw.(int)
	if !ok || id <= 0 {
		return "prod", "agent-fixture-1"
	}
	var cmd model.AgentCommand
	if err := ts.db.First(&cmd, "id = ?", id).Error; err != nil || cmd.ServerID == "" {
		return "prod", "agent-fixture-1"
	}
	return cmd.NamespaceCode, cmd.ServerID
}

// agentTargetFromRequest 从 URL query 与请求体派生 (namespace, serverId)；
// 两处都没有时退化为默认归属（见 doAgentAuto 的说明）。
func agentTargetFromRequest(rawURL string, body any) (string, string) {
	namespace, serverID := "", ""
	if u, err := url.Parse(rawURL); err == nil {
		namespace = u.Query().Get("namespace")
		serverID = u.Query().Get("serverId")
	}
	if m, ok := body.(map[string]any); ok {
		if namespace == "" {
			namespace, _ = m["namespace"].(string)
		}
		if serverID == "" {
			serverID, _ = m["serverId"].(string)
		}
	}
	return namespace, serverID
}

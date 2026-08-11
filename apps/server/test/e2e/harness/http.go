//go:build e2e

package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	artifactSecretFingerprintFileEnv = "BEACON_E2E_SECRET_FINGERPRINT_FILE"
	artifactSecretGeneratedFileEnv   = "BEACON_E2E_SECRET_GENERATED_FILE"
	artifactSecretGeneratedRecord    = "generated\n"
)

var (
	adminRequestTimeout = 30 * time.Second
	artifactSecrets     = make(map[string]struct{})
	artifactSecretsMu   sync.Mutex
	approvalRequestSeq  atomic.Uint64
)

// ProcessGuard 描述可在轮询与 HTTP 请求期间观察的外部进程生命周期。
type ProcessGuard interface {
	Done() <-chan struct{}
	CheckEarlyExit() error
}

type processLogPaths interface {
	LogPaths() (string, string)
}

func firstProcessGuard(guards []ProcessGuard) ProcessGuard {
	if len(guards) == 0 {
		return nil
	}
	return guards[0]
}

// RequestContext 创建带有限 deadline 的请求 context，并在受观察进程早退时立即取消。
func RequestContext(parent context.Context, timeout time.Duration, guard ProcessGuard) (context.Context, context.CancelFunc, error) {
	if err := checkProcessGuard(guard); err != nil {
		return nil, nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = adminRequestTimeout
	}
	timeoutCtx, cancelTimeout := context.WithTimeout(parent, timeout)
	guardedCtx, cancelGuard := contextWithGuard(timeoutCtx, guard)
	return guardedCtx, func() {
		cancelGuard()
		cancelTimeout()
	}, nil
}

// DoRequestWithGuard 用默认客户端执行带 deadline 的请求；guard 早退会取消请求并优先返回进程日志诊断。
func DoRequestWithGuard(req *http.Request, timeout time.Duration, guard ProcessGuard) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("HTTP 请求不能为空")
	}
	requestCtx, cancel, err := RequestContext(req.Context(), timeout, guard)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req.WithContext(requestCtx))
	if err != nil {
		cancel()
		if guardErr := checkProcessGuard(guard); guardErr != nil {
			return nil, guardErr
		}
		return nil, err
	}
	if guardErr := checkProcessGuard(guard); guardErr != nil {
		_ = resp.Body.Close()
		cancel()
		return nil, guardErr
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// Login 用管理员口令换登录令牌（FR-11）；可选 guard 保持既有调用兼容，并在已有进程链路中观察早退。
func Login(baseURL, user, pass string, guards ...ProcessGuard) (string, error) {
	body, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		return "", fmt.Errorf("编码登录请求失败：%w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/admin/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造登录请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := DoRequestWithGuard(req, 0, firstProcessGuard(guards))
	if err != nil {
		return "", fmt.Errorf("登录请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("登录失败：HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("解析登录响应失败：%w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("登录响应缺少 token")
	}
	return out.Token, nil
}

// CreateV2Namespace 创建 v2 namespace，返回数据库 ID 与仅用于 agent 接入的 accessToken。
func CreateV2Namespace(baseURL, token, name, description string) (uint, string, error) {
	var out struct {
		ID          uint   `json:"id"`
		AccessToken string `json:"accessToken"`
	}
	body := map[string]string{"name": name, "description": description}
	if err := doAdminJSON(baseURL, http.MethodPost, "/admin/v2/namespaces", token, body, http.StatusCreated, &out); err != nil {
		return 0, "", fmt.Errorf("创建 v2 namespace 失败：%w", err)
	}
	if out.ID == 0 || out.AccessToken == "" {
		return 0, "", fmt.Errorf("创建 v2 namespace 响应缺少 id 或 accessToken")
	}
	if err := registerArtifactSecret(out.AccessToken); err != nil {
		return 0, "", fmt.Errorf("登记 v2 namespace accessToken 失败：%w", err)
	}
	return out.ID, out.AccessToken, nil
}

// WaitIdentityStatus 按 namespaceID 与 serverID 等待 agent identity 进入指定状态。
// 可选 guard 保持既有调用兼容，并在每轮探测、HTTP 请求与重试等待期间检查进程早退。
func WaitIdentityStatus(
	baseURL, token string,
	namespaceID uint,
	serverID, status string,
	timeout time.Duration,
	guards ...ProcessGuard,
) (string, error) {
	guard := firstProcessGuard(guards)
	var identityID string
	var lastErr error
	err := WaitForCondition(timeout, time.Second, guard, func(ctx context.Context) bool {
		id, currentStatus, found, err := findIdentity(ctx, baseURL, token, namespaceID, serverID, status, guard)
		if err != nil {
			lastErr = err
			return false
		}
		identityID = id
		return found && currentStatus == status
	})
	if err == nil {
		return identityID, nil
	}
	if errors.Is(err, ErrWaitTimeout) && lastErr != nil {
		return "", fmt.Errorf("等待 %s identity 进入 %s 超时（最近错误：%v）：%w", serverID, status, lastErr, err)
	}
	return "", fmt.Errorf("等待 %s identity 进入 %s 失败：%w", serverID, status, err)
}

// WaitIdentityStatusByID 按身份 ID 等待状态，用于待确认阶段尚未获得 serverId 的 v2 agent。
func WaitIdentityStatusByID(
	baseURL, token, identityID, status string,
	timeout time.Duration,
	guards ...ProcessGuard,
) error {
	guard := firstProcessGuard(guards)
	var lastErr error
	err := WaitForCondition(timeout, time.Second, guard, func(ctx context.Context) bool {
		var out struct {
			Status string `json:"status"`
		}
		path := "/admin/v2/agent-identities/" + url.PathEscape(identityID)
		if err := doAdminJSONContext(ctx, baseURL, http.MethodGet, path, token, nil, http.StatusOK, &out, guard); err != nil {
			lastErr = err
			return false
		}
		return out.Status == status
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrWaitTimeout) && lastErr != nil {
		return fmt.Errorf("等待 identity %s 进入 %s 超时（最近错误：%v）：%w", identityID, status, lastErr, err)
	}
	return fmt.Errorf("等待 identity %s 进入 %s 失败：%w", identityID, status, err)
}

// WaitAgentIdentityID 从 agent 自管的 identity.yml 等待读取真实身份 ID。
func WaitAgentIdentityID(path string, timeout time.Duration, guards ...ProcessGuard) (string, error) {
	guard := firstProcessGuard(guards)
	var identityID string
	err := WaitForCondition(timeout, time.Second, guard, func(context.Context) bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		identityID = identityIDFromYAML(string(raw))
		return identityID != ""
	})
	if err != nil {
		return "", fmt.Errorf("读取 agent 身份文件 %s 失败：%w", path, err)
	}
	return identityID, nil
}

// WaitPendingAgentIdentity 读取 agent 自管身份并按 identityId 等待其进入 pending。
func WaitPendingAgentIdentity(
	baseURL, token, identityPath string,
	timeout time.Duration,
	guards ...ProcessGuard,
) (string, error) {
	identityID, err := WaitAgentIdentityID(identityPath, timeout, guards...)
	if err != nil {
		return "", err
	}
	if err := WaitIdentityStatusByID(baseURL, token, identityID, "pending", timeout, guards...); err != nil {
		return "", err
	}
	return identityID, nil
}

func identityIDFromYAML(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "identity-id:") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "identity-id:")), "\"'")
	}
	return ""
}

// ApprovalTicket 是危险操作申请创建后返回的最小审批票据。
type ApprovalTicket struct {
	ApprovalRequestID string `json:"approvalRequestId"`
	Status            string `json:"status"`
	immediate         bool
}

// RequestApprovalAndApprove 提交危险操作申请并作出审批决定，领域写入仍仅由异步 worker 执行。
func RequestApprovalAndApprove(
	baseURL, token, method, path string,
	body any,
	guard ProcessGuard,
) (ApprovalTicket, error) {
	return requestApprovalAndApproveWithKey(baseURL, token, method, path, body, newApprovalRequestKey(), guard)
}

func requestApprovalAndApproveWithKey(
	baseURL, token, method, path string,
	body any,
	idempotencyKey string,
	guard ProcessGuard,
) (ApprovalTicket, error) {
	if err := checkProcessGuard(guard); err != nil {
		return ApprovalTicket{}, fmt.Errorf("提交审批申请失败：%w", err)
	}
	requestCtx, cancel := contextWithGuard(context.Background(), guard)
	defer cancel()
	resp, err := submitApprovalRequest(requestCtx, baseURL, token, method, path, body, idempotencyKey, guard)
	if err != nil {
		return ApprovalTicket{}, fmt.Errorf("提交审批申请失败：%w", err)
	}
	defer resp.Body.Close()
	ticket, err := approvalTicketFromResponse(method, path, body, resp)
	if err != nil {
		return ApprovalTicket{}, fmt.Errorf("提交审批申请失败：%w", err)
	}
	if ticket.immediate || ticket.Status == "succeeded" {
		return ticket, nil
	}
	if err := approveApprovalRequest(requestCtx, baseURL, token, ticket.ApprovalRequestID, guard); err != nil {
		return ApprovalTicket{}, err
	}
	return ticket, nil
}

func submitApprovalRequest(
	ctx context.Context,
	baseURL, token, method, path string,
	body any,
	idempotencyKey string,
	guard ProcessGuard,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("编码请求体失败：%w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return DoRequestWithGuard(req, 0, guard)
}

func approvalTicketFromResponse(method, path string, body any, resp *http.Response) (ApprovalTicket, error) {
	switch resp.StatusCode {
	case http.StatusAccepted:
		var ticket ApprovalTicket
		if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
			return ApprovalTicket{}, fmt.Errorf("解析审批申请响应失败：%w", err)
		}
		if ticket.ApprovalRequestID == "" || (ticket.Status != "pending" && ticket.Status != "succeeded") {
			return ApprovalTicket{}, fmt.Errorf("审批申请返回非法票据：%+v", ticket)
		}
		return ticket, nil
	case http.StatusOK:
		if err := validateImmediateSettingCompletion(method, path, body, resp.Body); err != nil {
			return ApprovalTicket{}, err
		}
		return ApprovalTicket{Status: "succeeded", immediate: true}, nil
	default:
		return ApprovalTicket{}, adminUnexpectedStatusError(method, path, http.StatusAccepted, resp.StatusCode, resp.Body)
	}
}

// validateImmediateSettingCompletion 只接受设置直改端点明确返回的 {"ok":true}，避免任意 200 绕过审批等待。
func validateImmediateSettingCompletion(method, path string, body any, raw io.Reader) error {
	if method != http.MethodPut || !strings.HasPrefix(path, "/admin/v1/settings/") || strings.TrimPrefix(path, "/admin/v1/settings/") == "" {
		return fmt.Errorf("%s %s 返回 HTTP 200，非可验证的设置即时完成响应", method, path)
	}
	request, ok := body.(map[string]any)
	if !ok || request["value"] == nil || request["reason"] == nil {
		return fmt.Errorf("%s %s 返回 HTTP 200，但请求不含当前设置变更所需 value/reason", method, path)
	}
	var response struct {
		OK *bool `json:"ok"`
	}
	if err := json.NewDecoder(raw).Decode(&response); err != nil {
		return fmt.Errorf("解析设置即时完成响应失败：%w", err)
	}
	if response.OK == nil || !*response.OK {
		return fmt.Errorf("%s %s 返回 HTTP 200，但未确认当前设置变更完成", method, path)
	}
	return nil
}

func approveApprovalRequest(ctx context.Context, baseURL, token, approvalRequestID string, guard ProcessGuard) error {
	approvePath := "/admin/v2/approval-requests/" + url.PathEscape(approvalRequestID) + "/approve"
	if err := doAdminJSONContext(ctx, baseURL, http.MethodPost, approvePath, token, nil, http.StatusAccepted, nil, guard); err != nil {
		return fmt.Errorf("批准审批申请失败：%w", err)
	}
	return nil
}

// newApprovalRequestKey 为每次领域申请生成独立键；同一次申请在该调用内始终复用这一键。
func newApprovalRequestKey() string {
	sequence := approvalRequestSeq.Add(1)
	return fmt.Sprintf("e2e-approval-%x-%x", time.Now().UnixNano(), sequence)
}

// WaitApprovalStatus 按审批请求 ID 等待 worker 把申请推进到目标状态。
func WaitApprovalStatus(
	baseURL, token, approvalRequestID, status string,
	timeout time.Duration,
	guards ...ProcessGuard,
) error {
	guard := firstProcessGuard(guards)
	var lastErr error
	err := WaitForCondition(timeout, time.Second, guard, func(ctx context.Context) bool {
		var out struct {
			Status string `json:"status"`
		}
		path := "/admin/v2/approval-requests/" + url.PathEscape(approvalRequestID)
		if err := doAdminJSONContext(ctx, baseURL, http.MethodGet, path, token, nil, http.StatusOK, &out, guard); err != nil {
			lastErr = err
			return false
		}
		return out.Status == status
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrWaitTimeout) && lastErr != nil {
		return fmt.Errorf("等待审批 %s 进入 %s 超时（最近错误：%v）：%w", approvalRequestID, status, lastErr, err)
	}
	return fmt.Errorf("等待审批 %s 进入 %s 失败：%w", approvalRequestID, status, err)
}

// RequestApprovalAndWait 提交申请、作出审批决定，并等待 worker 到达指定终态。
func RequestApprovalAndWait(
	baseURL, token, method, path string,
	body any,
	wantStatus string,
	timeout time.Duration,
	guard ProcessGuard,
) (ApprovalTicket, error) {
	ticket, err := RequestApprovalAndApprove(baseURL, token, method, path, body, guard)
	if err != nil {
		return ApprovalTicket{}, err
	}
	if ticket.immediate {
		if wantStatus != "succeeded" {
			return ApprovalTicket{}, fmt.Errorf("设置即时完成仅支持期望终态 succeeded，实际 %s", wantStatus)
		}
		return ticket, nil
	}
	if err := WaitApprovalStatus(baseURL, token, ticket.ApprovalRequestID, wantStatus, timeout, guard); err != nil {
		return ApprovalTicket{}, err
	}
	return ticket, nil
}

// RequestApproveIdentityWithGuard 走身份审批申请、审批决定与异步 worker，不允许直调领域写入。
func RequestApproveIdentityWithGuard(
	baseURL, token, identityID, serverID, reason string,
	guard ProcessGuard,
	forceUnbindOccupier ...bool,
) error {
	force := len(forceUnbindOccupier) > 0 && forceUnbindOccupier[0]
	path := "/admin/v2/agent-identities/" + url.PathEscape(identityID) + "/approve"
	_, err := RequestApprovalAndApprove(baseURL, token, http.MethodPost, path, map[string]any{
		"serverId": serverID, "reason": reason, "forceUnbindOccupier": force,
	}, guard)
	if err != nil {
		return fmt.Errorf("申请确认 identity %s 失败：%w", identityID, err)
	}
	return nil
}

// WaitInstanceOnline 轮询实例列表直到目标 serverID 状态为 online。
// 可选 guard 保持既有调用兼容，并优先返回外部进程早退根因而非业务超时。
func WaitInstanceOnline(
	baseURL, token, namespace, serverID string,
	timeout time.Duration,
	guards ...ProcessGuard,
) error {
	guard := firstProcessGuard(guards)
	url := strings.TrimRight(baseURL, "/") + "/admin/v1/instances?namespace=" + namespace
	err := WaitForCondition(timeout, time.Second, guard, func(ctx context.Context) bool {
		var resp struct {
			Items []struct {
				ServerID string `json:"serverId"`
				Status   string `json:"status"`
			} `json:"items"`
		}
		if !tryAdminGet(ctx, url, token, &resp, guard) {
			return false
		}
		for _, item := range resp.Items {
			if item.ServerID == serverID && item.Status == "online" {
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("等待 agent 实例 %s online 失败：%w", serverID, err)
	}
	return nil
}

// ErrWaitTimeout 表示受 guard 轮询在业务条件满足前耗尽 deadline。
var ErrWaitTimeout = errors.New("等待条件超时")

// WaitForCondition 以固定周期等待条件，并在探测前、HTTP 请求期间和重试等待期间观察外部进程生命周期。
func WaitForCondition(
	timeout, interval time.Duration,
	guard ProcessGuard,
	condition func(context.Context) bool,
) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := checkProcessGuard(guard); err != nil {
			return err
		}
		ctx, cancel := pollingContext(deadline, interval, guard)
		matched := condition(ctx)
		cancel()
		if err := checkProcessGuard(guard); err != nil {
			return err
		}
		if matched {
			return nil
		}
		if !time.Now().Before(deadline) {
			return waitTimeoutError(guard)
		}
		if err := waitForRetry(deadline, interval, guard); err != nil {
			return err
		}
	}
}

// ObserveFor 在完整时长内持续校验不变量，任一轮失败或 Gradle 早退都会立即返回。
func ObserveFor(
	duration, interval time.Duration,
	guard ProcessGuard,
	invariant func(context.Context) error,
) error {
	deadline := time.Now().Add(duration)
	for {
		if err := checkProcessGuard(guard); err != nil {
			return err
		}
		ctx, cancel := pollingContext(deadline, interval, guard)
		err := invariant(ctx)
		cancel()
		if err != nil {
			return err
		}
		if err := checkProcessGuard(guard); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		if err := waitForRetry(deadline, interval, guard); err != nil {
			return err
		}
	}
}

func pollingContext(deadline time.Time, interval time.Duration, guard ProcessGuard) (context.Context, context.CancelFunc) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	if interval <= 0 || interval > remaining {
		interval = remaining
	}
	ctx, cancelTimeout := context.WithTimeout(context.Background(), interval)
	if guard == nil {
		return ctx, cancelTimeout
	}
	guarded, cancelGuard := context.WithCancel(ctx)
	go func() {
		select {
		case <-guard.Done():
			cancelGuard()
		case <-guarded.Done():
		}
	}()
	return guarded, func() {
		cancelGuard()
		cancelTimeout()
	}
}

func waitForRetry(deadline time.Time, interval time.Duration, guard ProcessGuard) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil
	}
	if interval <= 0 || interval > remaining {
		interval = remaining
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	if guard == nil {
		<-timer.C
		return nil
	}
	select {
	case <-timer.C:
		return nil
	case <-guard.Done():
		return guard.CheckEarlyExit()
	}
}

func checkProcessGuard(guard ProcessGuard) error {
	if guard == nil {
		return nil
	}
	return guard.CheckEarlyExit()
}

func waitTimeoutError(guard ProcessGuard) error {
	paths, ok := guard.(processLogPaths)
	if !ok {
		return ErrWaitTimeout
	}
	stdoutPath, stderrPath := paths.LogPaths()
	return fmt.Errorf("%w；stdout=%s；stderr=%s", ErrWaitTimeout, stdoutPath, stderrPath)
}

func contextWithGuard(parent context.Context, guard ProcessGuard) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if guard == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-guard.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// OfflineInstance 经 admin API 强制把某实例标记下线（POST /admin/v1/instances/{serverId}/offline）。
// 用途：服务端重启相位前，先把上一相位的残留实例标记下线，消除"陈旧 online"竞态——
// 否则控制面健康 TTL 未过期仍显示 online，会让随后的 WaitInstanceOnline 提前返回。
func OfflineInstance(baseURL, token, namespace, serverID string, guards ...ProcessGuard) error {
	return changeOfflineInstance(baseURL, token, namespace, serverID, http.MethodPost, "下线", firstProcessGuard(guards))
}

// CancelOfflineInstance 经 admin API 取消某实例的主动下线标记（DELETE /admin/v1/instances/{serverId}/offline）。
// 用途：FR-49 后「下线」是粘性拒绝态——强制下线清掉陈旧 online 后须随即取消，否则后续全新注册会被 403 拒、永不 online。
func CancelOfflineInstance(baseURL, token, namespace, serverID string, guards ...ProcessGuard) error {
	return changeOfflineInstance(baseURL, token, namespace, serverID, http.MethodDelete, "取消下线", firstProcessGuard(guards))
}

func changeOfflineInstance(baseURL, token, namespace, serverID, method, action string, guard ProcessGuard) error {
	query := url.Values{}
	query.Set("namespace", namespace)
	requestURL := strings.TrimRight(baseURL, "/") + "/admin/v1/instances/" + url.PathEscape(serverID) + "/offline?" + query.Encode()
	req, err := http.NewRequest(method, requestURL, nil)
	if err != nil {
		return fmt.Errorf("构造%s请求失败：%w", action, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return fmt.Errorf("%s请求失败：%w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s失败：HTTP %d", action, resp.StatusCode)
	}
	return nil
}

func findIdentity(ctx context.Context, baseURL, token string, namespaceID uint, serverID, status string, guard ProcessGuard) (string, string, bool, error) {
	var out struct {
		Items []struct {
			IdentityID string `json:"identityId"`
			ServerID   string `json:"serverId"`
			Status     string `json:"status"`
		} `json:"items"`
	}
	query := url.Values{}
	query.Set("namespaceId", strconv.FormatUint(uint64(namespaceID), 10))
	query.Set("status", status)
	query.Set("keyword", serverID)
	path := "/admin/v2/agent-identities?" + query.Encode()
	if err := doAdminJSONContext(ctx, baseURL, http.MethodGet, path, token, nil, http.StatusOK, &out, guard); err != nil {
		return "", "", false, err
	}
	for _, item := range out.Items {
		if item.ServerID == serverID && item.Status == status {
			return item.IdentityID, item.Status, true, nil
		}
	}
	return "", "", false, nil
}

func doAdminJSON(baseURL, method, path, token string, body any, wantStatus int, out any, guards ...ProcessGuard) error {
	return doAdminJSONContext(context.Background(), baseURL, method, path, token, body, wantStatus, out, firstProcessGuard(guards))
}

func doAdminJSONContext(
	ctx context.Context,
	baseURL, method, path, token string,
	body any,
	wantStatus int,
	out any,
	guard ProcessGuard,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体失败：%w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return fmt.Errorf("请求 %s %s 失败：%w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return adminUnexpectedStatusError(method, path, wantStatus, resp.StatusCode, resp.Body)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("解析 %s 响应失败：%w", path, err)
		}
	}
	return nil
}

// adminUnexpectedStatusError 仅提取标准错误码，保留诊断而不把可能敏感的服务端正文写入测试日志。
func adminUnexpectedStatusError(method, path string, wantStatus, gotStatus int, body io.Reader) error {
	const maxErrorBodyBytes = 4 << 10
	raw, _ := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	var response struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &response) == nil && response.Code != "" {
		return fmt.Errorf("%s %s 期望 HTTP %d，得 %d（错误码=%s）", method, path, wantStatus, gotStatus, response.Code)
	}
	return fmt.Errorf("%s %s 期望 HTTP %d，得 %d", method, path, wantStatus, gotStatus)
}

func registerArtifactSecret(value string) error {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("accessToken 为空或包含非法换行")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return nil
	}
	artifactSecretsMu.Lock()
	defer artifactSecretsMu.Unlock()
	if err := persistArtifactSecretFingerprint(value); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "::add-mask::%s\n", escapeWorkflowCommand(value)); err != nil {
		return fmt.Errorf("注册 GitHub Actions 掩码失败：%w", err)
	}
	artifactSecrets[value] = struct{}{}
	return nil
}

func persistArtifactSecretFingerprint(value string) error {
	fingerprintPath := os.Getenv(artifactSecretFingerprintFileEnv)
	generatedPath := os.Getenv(artifactSecretGeneratedFileEnv)
	if fingerprintPath == "" || generatedPath == "" {
		return fmt.Errorf("GitHub Actions 缺少动态凭据指纹状态路径")
	}
	records, err := readArtifactSecretFingerprints(fingerprintPath)
	if err != nil {
		return err
	}
	record := artifactSecretFingerprint(value)
	if containsString(records, record) {
		return nil
	}
	if err := appendArtifactSecretGeneratedRecord(generatedPath); err != nil {
		return err
	}
	return writeArtifactSecretFingerprints(fingerprintPath, append(records, record))
}

func artifactSecretFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x %d", sum, len([]byte(value)))
}

func readArtifactSecretFingerprints(path string) ([]string, error) {
	raw, exists, err := readPrivateStateFile(path, "动态凭据指纹文件")
	if err != nil {
		return nil, err
	}
	if !exists || len(raw) == 0 {
		return nil, nil
	}
	if raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("动态凭据指纹文件格式损坏")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if !validArtifactSecretFingerprint(line) {
			return nil, fmt.Errorf("动态凭据指纹文件格式损坏")
		}
		if _, duplicated := seen[line]; duplicated {
			return nil, fmt.Errorf("动态凭据指纹文件包含重复记录")
		}
		seen[line] = struct{}{}
	}
	return lines, nil
}

func validArtifactSecretFingerprint(line string) bool {
	parts := strings.Split(line, " ")
	if len(parts) != 2 || len(parts[0]) != sha256.Size*2 {
		return false
	}
	for _, char := range parts[0] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	length, err := strconv.Atoi(parts[1])
	return err == nil && length > 0 && strconv.Itoa(length) == parts[1]
}

func appendArtifactSecretGeneratedRecord(path string) error {
	raw, exists, err := readPrivateStateFile(path, "动态凭据生成状态")
	if err != nil {
		return err
	}
	if exists && !validArtifactSecretGeneratedState(raw) {
		return fmt.Errorf("动态凭据生成状态格式损坏")
	}
	content := append(append([]byte{}, raw...), artifactSecretGeneratedRecord...)
	if err := writePrivateFileAtomically(path, content); err != nil {
		return fmt.Errorf("持久化动态凭据生成状态失败：%w", err)
	}
	return nil
}

func validArtifactSecretGeneratedState(raw []byte) bool {
	if len(raw) == 0 || len(raw)%len(artifactSecretGeneratedRecord) != 0 {
		return false
	}
	return string(raw) == strings.Repeat(artifactSecretGeneratedRecord, len(raw)/len(artifactSecretGeneratedRecord))
}

func readPrivateStateFile(path, label string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取%s失败：%w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s不是普通文件", label)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, false, fmt.Errorf("%s权限必须为 0600", label)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("读取%s失败：%w", label, err)
	}
	return raw, true, nil
}

func writeArtifactSecretFingerprints(path string, records []string) error {
	content := []byte(strings.Join(records, "\n") + "\n")
	if err := writePrivateFileAtomically(path, content); err != nil {
		return fmt.Errorf("持久化动态凭据指纹失败：%w", err)
	}
	return nil
}

func writePrivateFileAtomically(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".e2e-secret-state-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := writeSyncedPrivateFile(file, content); err != nil {
		_ = file.Close()
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func writeSyncedPrivateFile(file *os.File, content []byte) error {
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func scanRegisteredArtifactSecrets(repoRoot string) error {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return nil
	}
	values := registeredArtifactSecrets()
	paths, err := artifactLogCandidates(repoRoot)
	if err != nil {
		return artifactScanFailure(repoRoot, err)
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return artifactScanFailure(repoRoot, fmt.Errorf("读取归档候选文件失败：%w", err))
		}
		for _, value := range values {
			if bytes.Contains(content, []byte(value)) {
				return artifactScanFailure(repoRoot, fmt.Errorf("归档候选文件含动态 accessToken：%s", path))
			}
		}
	}
	return nil
}

func artifactScanFailure(repoRoot string, scanErr error) error {
	marker := filepath.Join(repoRoot, ".tmp", "e2e-artifact-secret-leak")
	if err := os.WriteFile(marker, []byte("动态凭据扫描失败\n"), 0o600); err != nil {
		return fmt.Errorf("%v；且无法标记归档阻断：%w", scanErr, err)
	}
	return scanErr
}

func registeredArtifactSecrets() []string {
	artifactSecretsMu.Lock()
	defer artifactSecretsMu.Unlock()
	values := make([]string, 0, len(artifactSecrets))
	for value := range artifactSecrets {
		values = append(values, value)
	}
	return values
}

func artifactLogCandidates(repoRoot string) ([]string, error) {
	roots := []string{
		filepath.Join(repoRoot, ".tmp"),
		filepath.Join(repoRoot, "apps", "agent", "agent-e2e", "build", "mc-testkit"),
	}
	var paths []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			if root != roots[0] || strings.HasSuffix(entry.Name(), ".log") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("遍历归档候选目录失败：%w", err)
		}
	}
	return paths, nil
}

func escapeWorkflowCommand(value string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(value)
}

// tryAdminGet 发一个带 Bearer 的 admin GET，仅在 200 且能解析时返回 true（用于轮询，不报错）。
func tryAdminGet(ctx context.Context, url, token string, out any, guard ProcessGuard) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := DoRequestWithGuard(req, 0, guard)
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

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wcpe/Beacon/internal/model"
)

const (
	// maxBinaryBytes 是下载二进制的大小上限（资源 / 内存护栏，FR-97）。控制面二进制约几十 MB，留足余量取 200MiB。
	maxBinaryBytes = 200 << 20
	// maxSumsBytes 是 SHA256SUMS.txt 大小上限（仅几行文本，1MiB 足够防异常超大）。
	maxSumsBytes = 1 << 20
	// maxReleaseListBytes 是 release 列表 JSON 大小上限。
	maxReleaseListBytes = 8 << 20
	// downloadTimeout 是下载阶段的整体超时（含连接 + 传输），防卡死占资源。
	downloadTimeout = 5 * time.Minute
)

// AuditWriter 是更新服务所需的窄审计写入口（仅 Create，守最小依赖、便于测试）。
type AuditWriter interface {
	Create(entry *model.AuditLog) error
}

// CheckResult 是渠道检查结果（FR-99 端点消费；本 FR 提供服务方法）。
type CheckResult struct {
	Channel        Channel // 检查的渠道
	CurrentVersion string  // 当前运行版本
	LatestVersion  string  // 渠道最新 release 版本（tag）
	HasUpdate      bool    // 是否有可用更新（远端严格高于当前）
	ReleaseNotes   string  // release 正文（FR-100 渲染）
	ReleaseURL     string  // release 页面 URL
	PublishedAt    string  // 预留：发布时间（本 FR 不取，FR-99 按需补）
}

// Service 编排控制面在线更新（FR-97，见 ADR-0044）：查 Release → 下载 → SHA256 → 落位 pending → 请求重启。
// 出站 client 由调用方经 internal/httpx 工厂注入（带代理 + 超时）。
type Service struct {
	currentVersion string
	apiBase        string // GitHub API 基址（默认官方，测试注入 mock）
	repo           string // owner/name
	// newHTTPClient 按代理构造出站 client（注入 internal/httpx.NewClient，使 core 不裸用 net/http）。
	newHTTPClient func(proxyURL string, timeout time.Duration) (*http.Client, error)
	// requestRestart 在落位 pending 成功后回调，触发主进程以 exitcode.RequestUpdateRestart 退出交还 launcher。
	requestRestart func()
	// pendingPath 是 launcher 约定的 pending 新二进制路径（运行二进制同目录 beacon.new[.exe]）。
	pendingPath string
	audit       AuditWriter
	progress    *progressTracker
}

// Config 是更新服务装配参数。
type Config struct {
	CurrentVersion string
	APIBase        string // 空=官方 api.github.com
	Repo           string // 空=wcpe/Beacon
	PendingPath    string // launcher 约定 pending 路径
	NewHTTPClient  func(proxyURL string, timeout time.Duration) (*http.Client, error)
	RequestRestart func()
	Audit          AuditWriter
}

// NewService 构造更新服务。
func NewService(cfg Config) *Service {
	return &Service{
		currentVersion: cfg.CurrentVersion,
		apiBase:        cfg.APIBase,
		repo:           cfg.Repo,
		newHTTPClient:  cfg.NewHTTPClient,
		requestRestart: cfg.RequestRestart,
		pendingPath:    cfg.PendingPath,
		audit:          cfg.Audit,
		progress:       newProgressTracker(),
	}
}

// Snapshot 返回当前更新进度快照（FR-99 状态端点消费）。
func (s *Service) Snapshot() Progress { return s.progress.Snapshot() }

// assetName 返回本平台二进制资产名 beacon-<ver>-<os>-<arch>[.exe]。
// 仅 5 个已发布平台返回名 + true：linux-amd64/arm64、windows-amd64、darwin-amd64/arm64；其余返回 false（不可自更新）。
func assetName(version string) (string, bool) {
	supported := map[string]bool{
		"linux/amd64":   true,
		"linux/arm64":   true,
		"windows/amd64": true,
		"darwin/amd64":  true,
		"darwin/arm64":  true,
	}
	key := runtime.GOOS + "/" + runtime.GOARCH
	if !supported[key] {
		return "", false
	}
	name := fmt.Sprintf("beacon-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name, true
}

// CheckForUpdate 按渠道查最新 release 并与当前版本比对（只读、不下载、不落位）。
// 记一条 system.update-check 审计（detail 含渠道 / 当前 / 最新 / 有无更新，不含敏感）。
func (s *Service) CheckForUpdate(ctx context.Context, ch Channel, proxyURL, operator, clientIP string) (CheckResult, error) {
	s.progress.setPhase(PhaseChecking, "")
	client, err := s.newHTTPClient(proxyURL, downloadTimeout)
	if err != nil {
		s.progress.fail(fmt.Sprintf("构造出站客户端失败: %v", err))
		return CheckResult{}, err
	}
	rc := newReleaseClient(client, s.apiBase, s.repo)
	rel, err := rc.latestForChannel(ctx, ch)
	if err != nil {
		s.progress.fail(fmt.Sprintf("查 release 失败: %v", err))
		return CheckResult{}, err
	}
	hasUpdate, cmpErr := IsNewer(s.currentVersion, rel.TagName)
	if cmpErr != nil {
		// 版本无法比较（远端 tag 非法 / 当前 dev）：不报 5xx 语义，按「无更新」返回，错误仅记日志。
		slog.Warn("更新版本比较异常，按无可用更新处理", "当前", s.currentVersion, "远端", rel.TagName, "错误", cmpErr)
	}
	res := CheckResult{
		Channel:        ch,
		CurrentVersion: s.currentVersion,
		LatestVersion:  rel.TagName,
		HasUpdate:      hasUpdate,
		ReleaseNotes:   rel.Body,
		ReleaseURL:     rel.HTMLURL,
	}
	s.progress.setPhase(PhaseIdle, "")
	s.writeAudit(model.ActionSystemUpdateCheck, rel.TagName, model.ResultOK,
		fmt.Sprintf("渠道=%s 当前=%s 最新=%s 有更新=%v", ch, s.currentVersion, rel.TagName, hasUpdate),
		operator, clientIP)
	return res, nil
}

// ApplyUpdate 执行一次完整更新：查 release → 下载本平台资产 → SHA256 校验 → 原子落位 pending → 请求重启。
// 任何阶段失败：保留旧二进制、清理临时文件、状态 failed、记 system.update-failed、返回错误（进程不退）。
// 成功落位后记 system.update-apply 并回调 requestRestart（主进程将以退出码 70 退出交还 launcher）。
func (s *Service) ApplyUpdate(ctx context.Context, ch Channel, proxyURL, operator, clientIP string) error {
	s.progress.reset("")

	client, err := s.newHTTPClient(proxyURL, downloadTimeout)
	if err != nil {
		return s.failApply("", fmt.Errorf("构造出站客户端失败: %w", err), operator, clientIP)
	}
	rc := newReleaseClient(client, s.apiBase, s.repo)

	rel, err := rc.latestForChannel(ctx, ch)
	if err != nil {
		return s.failApply("", fmt.Errorf("查 release 失败: %w", err), operator, clientIP)
	}
	target := rel.TagName
	s.progress.reset(target)

	hasUpdate, cmpErr := IsNewer(s.currentVersion, target)
	if cmpErr != nil {
		return s.failApply(target, fmt.Errorf("版本比较失败: %w", cmpErr), operator, clientIP)
	}
	if !hasUpdate {
		return s.failApply(target, fmt.Errorf("远端版本 %s 不高于当前 %s，无需更新", target, s.currentVersion), operator, clientIP)
	}

	// 选本平台资产。
	binName, ok := assetName(target)
	if !ok {
		return s.failApply(target, fmt.Errorf("本平台 %s/%s 无可自更新资产", runtime.GOOS, runtime.GOARCH), operator, clientIP)
	}
	binAsset, ok := findAsset(rel, binName)
	if !ok {
		return s.failApply(target, fmt.Errorf("release 缺本平台资产 %s", binName), operator, clientIP)
	}
	sumsAsset, ok := findAsset(rel, "SHA256SUMS.txt")
	if !ok {
		return s.failApply(target, fmt.Errorf("release 缺 SHA256SUMS.txt"), operator, clientIP)
	}

	// 下载二进制到临时文件（同目录，便于同卷原子 rename 落位）。
	s.progress.setPhase(PhaseDownloading, target)
	tmpPath, gotSum, err := s.downloadBinary(ctx, client, binAsset.URL)
	if err != nil {
		return s.failApply(target, fmt.Errorf("下载资产失败: %w", err), operator, clientIP)
	}
	// 自此任何失败都须清理临时文件（资源泄露禁令）。
	cleanup := func() {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("清理更新临时文件失败", "文件", tmpPath, "错误", rmErr)
		}
	}

	// 校验 SHA256。
	s.progress.setPhase(PhaseVerifying, target)
	wantSum, err := s.fetchExpectedSum(ctx, client, sumsAsset.URL, binName)
	if err != nil {
		cleanup()
		return s.failApply(target, fmt.Errorf("取期望 SHA256 失败: %w", err), operator, clientIP)
	}
	if !strings.EqualFold(gotSum, wantSum) {
		cleanup()
		return s.failApply(target, fmt.Errorf("SHA256 校验不通过：期望 %s 实际 %s", wantSum, gotSum), operator, clientIP)
	}

	// 原子落位 pending（同卷 rename）。
	s.progress.setPhase(PhaseStaging, target)
	if err := os.Rename(tmpPath, s.pendingPath); err != nil {
		cleanup()
		return s.failApply(target, fmt.Errorf("落位 pending 失败: %w", err), operator, clientIP)
	}

	s.progress.setPhase(PhaseReadyRestart, target)
	s.writeAudit(model.ActionSystemUpdateApply, target, model.ResultOK,
		fmt.Sprintf("渠道=%s 目标版本=%s 已落位 pending，请求 launcher 换二进制重启", ch, target),
		operator, clientIP)
	slog.Info("更新已落位 pending，请求 launcher 换二进制重启", "目标版本", target, "pending", s.pendingPath)

	// 触发主进程以退出码 70 退出，交还 launcher 换二进制。
	if s.requestRestart != nil {
		s.requestRestart()
	}
	return nil
}

// downloadBinary 下载二进制到运行目录旁的临时文件，边写边算 SHA256，受大小上限约束。
// 返回临时文件路径与实算 hex SHA256；失败时已自清理临时文件并返回错误。
func (s *Service) downloadBinary(ctx context.Context, client *http.Client, url string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("下载返回非 200: %d", resp.StatusCode)
	}

	// 临时文件放 pending 同目录，确保与运行二进制同卷（rename 原子）。
	dir := filepath.Dir(s.pendingPath)
	tmp, err := os.CreateTemp(dir, "beacon-update-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()

	hasher := sha256.New()
	// 限制读取上限 +1 字节：恰好读满上限+1 即判超限（防超大响应耗盘 / 内存）。
	limited := io.LimitReader(resp.Body, maxBinaryBytes+1)
	written, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	closeErr := tmp.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("写临时文件失败: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("关闭临时文件失败: %w", closeErr)
	}
	if written > maxBinaryBytes {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("资产超过大小上限 %d 字节", int64(maxBinaryBytes))
	}
	return tmpPath, hex.EncodeToString(hasher.Sum(nil)), nil
}

// fetchExpectedSum 下载 SHA256SUMS.txt 并解析出目标文件名对应的期望 hex 哈希。
// 行格式：「<hex>␣␣<filename>」（GNU sha256sum 风格，两空格或单空格 / 制表皆容忍）。
func (s *Service) fetchExpectedSum(ctx context.Context, client *http.Client, url, binName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 SHA256SUMS.txt 返回非 200: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSumsBytes))
	if err != nil {
		return "", err
	}
	sum, ok := parseSums(string(body), binName)
	if !ok {
		return "", fmt.Errorf("SHA256SUMS.txt 中无 %s 的校验和", binName)
	}
	return sum, nil
}

// parseSums 从 SHA256SUMS.txt 内容中取目标文件名对应的 hex 哈希。
// 每行「<hex> <filename>」，filename 可能带 '*'（二进制模式前缀）；找到返回 hex+true。
func parseSums(content, binName string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == binName {
			return fields[0], true
		}
	}
	return "", false
}

// failApply 把失败统一收口：标进度 failed、记 system.update-failed 审计、返回错误（进程不退、保留旧二进制）。
func (s *Service) failApply(target string, err error, operator, clientIP string) error {
	s.progress.fail(err.Error())
	s.writeAudit(model.ActionSystemUpdateFailed, target, model.ResultFail, err.Error(), operator, clientIP)
	slog.Error("控制面在线更新失败，保留旧二进制继续运行", "目标版本", target, "错误", err)
	return err
}

// writeAudit 写一条更新审计（best-effort：落库失败仅 WARN，不影响更新流程主结论）。
func (s *Service) writeAudit(action, target, result, detail, operator, clientIP string) {
	if s.audit == nil {
		return
	}
	if operator == "" {
		operator = "system"
	}
	if err := s.audit.Create(&model.AuditLog{
		Operator:   operator,
		Action:     action,
		TargetType: model.TargetTypeSystem,
		TargetRef:  target,
		Detail:     detail,
		Result:     result,
		ClientIP:   clientIP,
	}); err != nil {
		slog.Warn("写更新审计失败", "action", action, "错误", err)
	}
}

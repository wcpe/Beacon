package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Channel 是更新渠道（作 ApplyUpdate/CheckForUpdate 入参，不在本核心读 store；
// store 渠道项由 FR-101 加、FR-99 后续批读后传入，FR-97 不依赖 FR-101）。
type Channel string

const (
	ChannelStable Channel = "stable" // 正式渠道：只消费 tag 精确为 vX.Y.Z 的 GA Release
)

const (
	// defaultRepo 是默认仓库（owner/name），可经构造入参覆盖（仓址做可配项默认此值，FR-97 见 ADR-0044）。
	defaultRepo = "wcpe/Beacon"
	// releasePageSize 使用 GitHub Releases API 允许的最大单页数量，减少分页请求。
	releasePageSize = 100
	// maxReleasePages 是分页硬上限；持续返回满页时直接失败，避免异常服务导致无限请求。
	maxReleasePages = 100
)

// ghAsset 是 GitHub Release 资产（仅取所需字段）。
type ghAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// ghRelease 是 GitHub Release（仅取所需字段）。
type ghRelease struct {
	TagName string `json:"tag_name"`
	// Name 为 Release 标题。稳定更新选择器不读取该字段，仅用于下载前元数据复验。
	Name        string    `json:"name"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"` // 发布时间（RFC3339 字符串，FR-99 端点透传，不参与比较）
	Assets      []ghAsset `json:"assets"`
}

// releaseClient 查 GitHub Releases API。出站 client 由调用方经 internal/httpx 工厂构造（带代理 + 超时），
// 此处不裸建 http.Client、不持有代理逻辑（FR-98 收口出站，见 ADR-0047）。
type releaseClient struct {
	httpClient *http.Client
	apiBase    string // GitHub API 基址，默认 https://api.github.com；测试经 mock server 注入
	repo       string // owner/name
}

// newReleaseClient 构造 release 客户端。apiBase 为空用默认 GitHub API 基址；repo 为空用默认仓库。
func newReleaseClient(httpClient *http.Client, apiBase, repo string) *releaseClient {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	if repo == "" {
		repo = defaultRepo
	}
	return &releaseClient{httpClient: httpClient, apiBase: apiBase, repo: repo}
}

// parseGATag 只接受规范的 GA tag：精确 vX.Y.Z，数字段除 0 外不得有前导零。
func parseGATag(tag string) (semver, error) {
	if len(tag) < 2 || tag[0] != 'v' {
		return semver{}, fmt.Errorf("GA tag 须以 v 开头")
	}
	return parseSemver(tag)
}

func isGATag(tag string) bool {
	_, err := parseGATag(tag)
	return err == nil
}

// latestForChannel 只接受 stable，并从全部合法 GA Release 中选择最高语义版本。
// 合法对象必须同时满足：非 draft、非 prerelease，且 tag 精确为 vX.Y.Z；名称和 API 返回顺序均不参与选择。
func (c *releaseClient) latestForChannel(ctx context.Context, ch Channel) (*ghRelease, error) {
	if ch != ChannelStable {
		return nil, fmt.Errorf("未知更新渠道: %q", ch)
	}
	var latest *ghRelease
	var latestVersion semver
	for page := 1; page <= maxReleasePages; page++ {
		releases, err := c.listReleasePage(ctx, page)
		if err != nil {
			return nil, err
		}
		latest, latestVersion = selectLatestGA(releases, latest, latestVersion)
		if len(releases) < releasePageSize {
			if latest == nil {
				return nil, fmt.Errorf("渠道 %q 无可用 GA release", ch)
			}
			return latest, nil
		}
	}
	return nil, fmt.Errorf("查 release 列表超过分页上限: %d", maxReleasePages)
}

func selectLatestGA(releases []ghRelease, latest *ghRelease, latestVersion semver) (*ghRelease, semver) {
	for i := range releases {
		release := &releases[i]
		version, err := parseGATag(release.TagName)
		if release.Draft || release.Prerelease || err != nil {
			continue
		}
		if latest == nil || compareSemver(version, latestVersion) > 0 {
			latest = release
			latestVersion = version
		}
	}
	return latest, latestVersion
}

// listReleasePage 按 page/per_page 拉取一页 Release，并对每页响应保留大小限制。
func (c *releaseClient) listReleasePage(ctx context.Context, page int) ([]ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d&page=%d", c.apiBase, c.repo, releasePageSize, page)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub API 推荐显式 Accept 头。
	request.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("查 release 列表失败: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体，关闭错误无可处置
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查 release 列表返回非 200: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseListBytes))
	if err != nil {
		return nil, fmt.Errorf("读 release 列表响应失败: %w", err)
	}
	var releases []ghRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("解析 release 列表失败: %w", err)
	}
	return releases, nil
}

// ping 用当前 client 试连 GitHub release API（FR-124 代理测试）：发一个轻量 release 列表请求，
// 连通且 2xx 即视为可达；网络 / 代理失败或非 2xx 返回错误。不解析响应体。
func (c *releaseClient) ping(ctx context.Context) error {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=1", c.apiBase, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接 GitHub 失败: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读、不读体
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub 返回非 2xx: %d", resp.StatusCode)
	}
	return nil
}

// findAsset 在 release 资产中按精确文件名找资产（本平台二进制 / SHA256SUMS.txt）。
func findAsset(r *ghRelease, name string) (ghAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// validateRevalidatedGARelease 确认下载前二次查询仍指向同一 GA 及其目标资产。
func validateRevalidatedGARelease(expected, actual *ghRelease, binaryName string) error {
	if expected.TagName != actual.TagName || expected.Name != actual.Name || expected.Draft != actual.Draft ||
		expected.Prerelease != actual.Prerelease || expected.HTMLURL != actual.HTMLURL || expected.PublishedAt != actual.PublishedAt {
		return fmt.Errorf("GA 元数据已变化")
	}
	for _, name := range []string{binaryName, "SHA256SUMS.txt"} {
		before, ok := findAsset(expected, name)
		if !ok {
			return fmt.Errorf("首次查询缺少资产 %s", name)
		}
		after, ok := findAsset(actual, name)
		if !ok || before.ID != after.ID || before.URL != after.URL {
			return fmt.Errorf("GA 资产已变化: %s", name)
		}
	}
	return nil
}

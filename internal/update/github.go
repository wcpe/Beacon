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
	ChannelStable Channel = "stable" // 稳定渠道：取最新非 prerelease release
	ChannelRC     Channel = "rc"     // 预发布渠道：取最新 prerelease release（rc 语义见 ADR-0046）
)

// defaultRepo 是默认仓库（owner/name），可经构造入参覆盖（仓址做可配项默认此值，FR-97 见 ADR-0044）。
const defaultRepo = "wcpe/Beacon"

// ghAsset 是 GitHub Release 资产（仅取所需字段）。
type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// ghRelease 是 GitHub Release（仅取所需字段）。
type ghRelease struct {
	TagName     string    `json:"tag_name"`
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

// latestForChannel 取指定渠道的最新 release：
//   - stable：列表中最新的非 prerelease、非 draft release；
//   - rc：列表中最新的 prerelease（非 draft）。
//
// GitHub `/releases` 默认按发布时间倒序，取首个匹配即「最新」。无匹配返回错误。
func (c *releaseClient) latestForChannel(ctx context.Context, ch Channel) (*ghRelease, error) {
	releases, err := c.listReleases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		r := &releases[i]
		if r.Draft {
			continue
		}
		switch ch {
		case ChannelStable:
			if !r.Prerelease {
				return r, nil
			}
		case ChannelRC:
			if r.Prerelease {
				return r, nil
			}
		default:
			return nil, fmt.Errorf("未知更新渠道: %q", ch)
		}
	}
	return nil, fmt.Errorf("渠道 %q 无可用 release", ch)
}

// listReleases 拉取 release 列表（首页足够取最新）。
func (c *releaseClient) listReleases(ctx context.Context) ([]ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", c.apiBase, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub API 推荐显式 Accept 头。
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("查 release 列表失败: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体，关闭错误无可处置
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查 release 列表返回非 200: %d", resp.StatusCode)
	}
	// 限制响应体大小，防异常超大响应耗内存（API JSON 通常很小）。
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

// findAsset 在 release 资产中按精确文件名找资产（本平台二进制 / SHA256SUMS.txt）。
func findAsset(r *ghRelease, name string) (ghAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsGATagStrict 验证 GA tag 必须严格为 vX.Y.Z，且各数字段除 0 外不得有前导零。
func TestIsGATagStrict(t *testing.T) {
	valid := []string{"v0.0.0", "v1.2.3", "v10.20.30"}
	for _, tag := range valid {
		if !isGATag(tag) {
			t.Errorf("合法 GA tag %q 应被接受", tag)
		}
	}

	invalid := []string{
		"0.0.0", "v01.2.3", "v1.02.3", "v1.2.03",
		"v1.2", "v1.2.3.4", "v1.2.3-rc.1", "v1.2.3-dev.1.gabcdef0",
		"v1.2.3+build.1", "dev", " v1.2.3", "v1.2.3 ",
	}
	for _, tag := range invalid {
		if isGATag(tag) {
			t.Errorf("非法 GA tag %q 应被拒绝", tag)
		}
	}
}

// TestLatestForChannelSelectsHighestGATag 验证稳定更新从全部合法 GA Release 中选择最高语义版本，不依赖 API 返回顺序。
func TestLatestForChannelSelectsHighestGATag(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{
			{TagName: "v9.0.0", Name: "v9.0.0", Draft: true},
			{TagName: "v8.0.0", Name: "v8.0.0", Prerelease: true},
			{TagName: "v7.0.0-rc.1", Name: "v7.0.0-rc.1"},
			{TagName: "dev", Name: "v99.0.0"},
			{TagName: "v01.7.0", Name: "v01.7.0"},
			{TagName: "1.6.0", Name: "v1.6.0"},
			{TagName: "v1.9.99", Name: "较早返回的合法版本"},
			{TagName: "v1.10.0", Name: "最高合法版本"},
			{TagName: "v1.8.100", Name: "较晚返回但版本更低"},
		}
		_ = json.NewEncoder(w).Encode(releases)
	})

	rc := newReleaseClient(&http.Client{}, srv.URL, "")
	stable, err := rc.latestForChannel(context.Background(), ChannelStable)
	if err != nil {
		t.Fatalf("取 stable 失败: %v", err)
	}
	if stable.TagName != "v1.10.0" {
		t.Fatalf("stable 应选择最高合法 GA tag v1.10.0，实际 %q", stable.TagName)
	}
}

// TestLatestForChannelRejectsOnlyRC 验证只有 RC 或开发 Release 时，稳定更新不产生候选。
func TestLatestForChannelRejectsOnlyRC(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{
			{TagName: "v1.0.0-rc.3", Name: "v1.0.0-rc.3", Prerelease: true},
			{TagName: "dev", Name: "v1.0.0-dev.1.gabc1234", Prerelease: true},
		}
		_ = json.NewEncoder(w).Encode(releases)
	})

	rc := newReleaseClient(&http.Client{}, srv.URL, "")
	if _, err := rc.latestForChannel(context.Background(), ChannelStable); err == nil {
		t.Fatal("只有 RC 或开发 Release 时不应返回稳定更新候选")
	}
}

// TestLatestForChannelSelectsHighestGAFromSecondPage 验证最高合法 GA 位于第二页时仍会被选中。
func TestLatestForChannelSelectsHighestGAFromSecondPage(t *testing.T) {
	requests := 0
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page 应为 100，实际 %q", r.URL.Query().Get("per_page"))
		}
		switch r.URL.Query().Get("page") {
		case "1":
			releases := make([]ghRelease, releasePageSize)
			for i := range releases {
				releases[i] = ghRelease{TagName: "dev", Prerelease: true}
			}
			releases[0] = ghRelease{TagName: "v1.9.9"}
			_ = json.NewEncoder(w).Encode(releases)
		case "2":
			_ = json.NewEncoder(w).Encode([]ghRelease{{TagName: "v2.0.0"}})
		default:
			t.Errorf("不应请求第 %s 页", r.URL.Query().Get("page"))
			_ = json.NewEncoder(w).Encode([]ghRelease{})
		}
	})

	rc := newReleaseClient(&http.Client{}, srv.URL, "")
	stable, err := rc.latestForChannel(context.Background(), ChannelStable)
	if err != nil {
		t.Fatalf("取 stable 失败: %v", err)
	}
	if stable.TagName != "v2.0.0" {
		t.Fatalf("应选择第二页最高合法 GA v2.0.0，实际 %q", stable.TagName)
	}
	if requests != 2 {
		t.Fatalf("第二页为短页后应停止分页，实际请求 %d 次", requests)
	}
}

// TestLatestForChannelStopsAtPageLimit 验证服务端持续返回满页时会在固定上限停止，避免无限分页。
func TestLatestForChannelStopsAtPageLimit(t *testing.T) {
	requests := 0
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		releases := make([]ghRelease, releasePageSize)
		for i := range releases {
			releases[i] = ghRelease{TagName: "dev", Prerelease: true}
		}
		_ = json.NewEncoder(w).Encode(releases)
	})

	rc := newReleaseClient(&http.Client{}, srv.URL, "")
	_, err := rc.latestForChannel(context.Background(), ChannelStable)
	if err == nil || !strings.Contains(err.Error(), "分页上限") {
		t.Fatalf("持续满页应在分页上限停止，实际错误 %v", err)
	}
	if requests != maxReleasePages {
		t.Fatalf("分页请求次数应受限为 %d，实际 %d", maxReleasePages, requests)
	}
}

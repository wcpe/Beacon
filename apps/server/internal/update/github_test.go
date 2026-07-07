package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReleaseVersionTagPreferredNameFallback 验证版本解析：tag 为合法 semver 时用 tag；
// tag 非 semver（滚动标签如 prerelease）时回退用 Release name（v<VERSION>）。
func TestReleaseVersionTagPreferredNameFallback(t *testing.T) {
	cases := []struct {
		name    string
		rel     ghRelease
		want    string
		wantErr bool
	}{
		// stable 路径：tag=vX.Y.Z 直接用 tag。
		{"tag 为 semver", ghRelease{TagName: "v0.17.0", Name: "随便的标题"}, "v0.17.0", false},
		// 滚动路径：tag=prerelease 非 semver，回退 name=v<VERSION>。
		{"tag 非 semver 回退 name", ghRelease{TagName: "prerelease", Name: "v0.17.0"}, "v0.17.0", false},
		// 两者都非 semver → 错误（当未知处理）。
		{"两者都非法", ghRelease{TagName: "prerelease", Name: "滚动预发布"}, "", true},
		// tag 合法但 name 也合法 → 仍优先 tag。
		{"tag 优先于 name", ghRelease{TagName: "v0.16.0", Name: "v9.9.9"}, "v0.16.0", false},
	}
	for _, c := range cases {
		got, err := releaseVersion(&c.rel)
		if (err != nil) != c.wantErr {
			t.Errorf("%s：releaseVersion err=%v，期望 wantErr=%v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("%s：releaseVersion=%q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestLatestForChannelSelectsByPrereleaseBool 渠道选取：stable 取最新非 prerelease，
// prerelease 取最新 prerelease（GitHub prerelease 布尔区分，ADR-0052 决策 4）。
func TestLatestForChannelSelectsByPrereleaseBool(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		// 列表按发布时间倒序（GitHub 约定）：首个滚动预发布，其后正式版。
		releases := []ghRelease{
			{TagName: "prerelease", Name: "v0.17.0", Prerelease: true},
			{TagName: "v0.16.0", Name: "v0.16.0", Prerelease: false},
		}
		_ = json.NewEncoder(w).Encode(releases)
	})
	rc := newReleaseClient(&http.Client{}, srv.URL, "")

	stable, err := rc.latestForChannel(context.Background(), ChannelStable)
	if err != nil {
		t.Fatalf("取 stable 失败: %v", err)
	}
	if stable.TagName != "v0.16.0" {
		t.Fatalf("stable 应取最新非 prerelease，实际 %q", stable.TagName)
	}

	pre, err := rc.latestForChannel(context.Background(), ChannelPrerelease)
	if err != nil {
		t.Fatalf("取 prerelease 失败: %v", err)
	}
	if pre.TagName != "prerelease" || pre.Name != "v0.17.0" {
		t.Fatalf("prerelease 应取最新 prerelease 滚动 Release，实际 tag=%q name=%q", pre.TagName, pre.Name)
	}
}

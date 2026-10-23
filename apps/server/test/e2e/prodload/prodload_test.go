//go:build prodload

// 生产只读压测驱动。
//
// 与 apps/server/test/loadtest 的区别：那套 harness 会自起隔离控制面（BuildBeacon + StartControlPlane），
// 无法指向既有生产实例。本驱动只做两件事：登录拿令牌 + 对只读端点按并发档位打点，
// 不创建/修改任何业务数据，不注册虚拟 agent（那会污染生产库）。
//
// 安全边界（刻意收窄）：
//   - 只允许 GET 只读端点，路径写死在本文件，不接受命令行传入任意 URL；
//   - 不做写操作压测（写压测会逐条落审计，压出来的是审计能力而非接口能力）；
//   - 不打印令牌，也不落盘令牌。
//
// 用法：
//
//	PRODLOAD_BASE=http://127.0.0.1:8848 \
//	PRODLOAD_USER=admin PRODLOAD_PASS='<口令>' \
//	PRODLOAD_LEVELS=1,8,32,128 PRODLOAD_DURATION=15s \
//	go test -tags prodload -timeout=40m -v ./apps/server/test/e2e/prodload/
package prodload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 只读端点白名单。全部为 GET 且不产生业务写入。
// 注意：/admin/v1/topology 需要 namespace 查询参数，缺参数会返回 400，不计入压测口径。
var readonlyRoutes = []string{
	"/admin/v1/system/status",
	"/admin/v2/servers",
	"/admin/v2/zone-tree",
	"/admin/v2/health",
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func baseURL() string { return strings.TrimRight(envOr("PRODLOAD_BASE", "http://127.0.0.1:8848"), "/") }

// login 用管理口令换令牌。仅内存持有，不打印、不落盘。
func login(t *testing.T, base, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/admin/v1/auth/login", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("构造登录请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("登录请求失败：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录失败：状态=%d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		t.Fatalf("登录响应未含令牌")
	}
	return out.Token
}

type result struct {
	route   string
	latency time.Duration
	status  int
	err     error
}

// percentile 取分位数（p 为 0~1），输入要求已升序。
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// TestProductionReadOnly 对生产控制面做只读并发压测。
func TestProductionReadOnly(t *testing.T) {
	base := baseURL()
	user := envOr("PRODLOAD_USER", "admin")
	pass := os.Getenv("PRODLOAD_PASS")
	if pass == "" {
		t.Fatal("缺少 PRODLOAD_PASS")
	}
	levels := parseLevels(envOr("PRODLOAD_LEVELS", "1,8,32,128"))
	dur, err := time.ParseDuration(envOr("PRODLOAD_DURATION", "15s"))
	if err != nil {
		t.Fatalf("PRODLOAD_DURATION 非法：%v", err)
	}

	token := login(t, base, user, pass)
	t.Logf("登录成功，目标=%s 档位=%v 每档=%s 只读端点=%d 个", base, levels, dur, len(readonlyRoutes))

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 512,
			MaxConnsPerHost:     0,
			IdleConnTimeout:     60 * time.Second,
		},
	}

	// 预热：避免把 TLS/TCP 建连与连接池填充算进第一档。
	for _, r := range readonlyRoutes {
		_ = doRead(context.Background(), client, base, r, token)
	}

	for _, workers := range levels {
		t.Logf("===== 并发 %d，持续 %s =====", workers, dur)
		ctx, cancel := context.WithTimeout(context.Background(), dur)

		var mu sync.Mutex
		var all []time.Duration
		byStatus := map[int]int{}
		byRoute := map[string][]time.Duration{}
		errCount := 0
		sent := 0

		start := time.Now()
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				idx := w
				for ctx.Err() == nil {
					route := readonlyRoutes[idx%len(readonlyRoutes)]
					idx++
					r := doRead(ctx, client, base, route, token)
					mu.Lock()
					sent++
					if r.err != nil {
						errCount++
					} else {
						all = append(all, r.latency)
						byStatus[r.status]++
						byRoute[r.route] = append(byRoute[r.route], r.latency)
					}
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)
		cancel()

		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

		rps := float64(len(all)) / elapsed.Seconds()
		t.Logf("结果：发起=%d 完成=%d 错误=%d 实际耗时=%s RPS=%.1f",
			sent, len(all), errCount, elapsed.Round(time.Millisecond), rps)
		if len(all) > 0 {
			t.Logf("延迟：p50=%s p90=%s p99=%s max=%s",
				percentile(all, 0.50).Round(time.Microsecond),
				percentile(all, 0.90).Round(time.Microsecond),
				percentile(all, 0.99).Round(time.Microsecond),
				all[len(all)-1].Round(time.Microsecond))
		}
		var codes []string
		for c, n := range byStatus {
			codes = append(codes, fmt.Sprintf("%d×%d", c, n))
		}
		sort.Strings(codes)
		t.Logf("状态码：%s（非 2xx 占比 %.3f%%）",
			strings.Join(codes, " "), 100*float64(nonOK(byStatus))/float64(max1(len(all))))
		var routes []string
		for r, ls := range byRoute {
			sort.Slice(ls, func(i, j int) bool { return ls[i] < ls[j] })
			routes = append(routes, fmt.Sprintf("%s p50=%s p99=%s n=%d", r,
				percentile(ls, 0.50).Round(time.Microsecond), percentile(ls, 0.99).Round(time.Microsecond), len(ls)))
		}
		sort.Strings(routes)
		for _, s := range routes {
			t.Logf("  端点 %s", s)
		}
		cancel()
	}
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func nonOK(byStatus map[int]int) int {
	n := 0
	for c, v := range byStatus {
		if c < 200 || c >= 300 {
			n += v
		}
	}
	return n
}

func doRead(ctx context.Context, client *http.Client, base, route, token string) result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+route, nil)
	if err != nil {
		return result{route: route, err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	begin := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{route: route, err: err}
	}
	// 必须读完并关闭，否则连接无法复用，测出来的是建连能力。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<22))
	_ = resp.Body.Close()
	return result{route: route, latency: time.Since(begin), status: resp.StatusCode}
}

func parseLevels(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		out = []int{1, 8, 32, 128}
	}
	return out
}

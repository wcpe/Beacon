//go:build e2e && loadtest

// 真实时钟跨越 UTC 00:00 的日表切换验证（补 daily_table_rollover_test.go 注入日期覆盖不到的盲区）。
//
// 两者失效模式不同、不可互替：
//   - 注入日期（store 包单测）验证「day 参数换了，建表 / 分流 / 缓存仍正确」——机制正确性；
//   - 真实午夜（本用例）验证「墙上时钟真的走到那一刻时，生产写入路径仍正确」——集成正确性。
//     后者才能暴露时区误用（拿本地日期拼表名）、写侧与查侧时钟源不一致、把「今天」钉死的缓存
//     等只在真实时间推进下才显形的缺陷。
//
// 为什么必须真等：已实测 LD_PRELOAD 时钟垫片对本项目二进制无效（Go 经 vDSO / raw syscall 取时间，
// 绕过 libc 的 clock_gettime），无法伪造进程时钟；故真午夜只能真等。
// 耗时 ≈ 距下一个 UTC 00:00 的时长 + lead + tail（最长约 24 小时）。
//
// 运行（正式：必须留空 MIDNIGHT_TARGET，走真实午夜）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=26h -v -run '^TestMidnightRolloverRealClock' ./apps/server/test/loadtest/
//
// 环境变量（均带默认值）：
//
//	MIDNIGHT_TARGET  目标午夜 RFC3339，默认「下一个 UTC 00:00」。**仅供冒烟**：
//	                 正式验证真实午夜必须留空，否则退化成又一次注入日期。
//	                 注意冒烟若注入一个非午夜时刻，密集窗口内不存在日界，跨日断言会自动降级为
//	                 「仅通用不变量」（见下方判定分层的说明），这是刻意的、不是漏测。
//	MIDNIGHT_LEAD    午夜前多久进入密集窗口，默认 3m
//	MIDNIGHT_TAIL    午夜后继续密集上报多久，默认 3m
//	MIDNIGHT_IDLE    等待期上报间隔（低频保活 + 持续写跨日前日表），默认 60s
//	MIDNIGHT_DENSE   密集窗口上报间隔（对齐 agent 真实 5s 批），默认 5s
//	MIDNIGHT_DB_NAME 独立库文件名，默认 beacon-midnight.db（与主压测分库，互不干扰）
//
// 判定分两层（为什么这样分）：
//   - 通用不变量（两种模式都断言）：**每一行都住在「它自己 bucket 所属 UTC 日」的日表里**。
//     逐表逐行核对，任何串表立即报红。这条不依赖日界是否真的在窗口内，故冒烟也有判别力。
//   - 跨日不变量（仅当密集窗口真的跨越日界时断言）：跨日前表 max bucket < 日界 ≤ 跨日后表 min bucket。
//     若注入的目标时刻不在日界上（冒烟常见），窗口内没有日界，这条会自动跳过并在报告里标注，
//     绝不伪装成「已通过」。
package loadtest_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// midnightNamespace 独立于主压测的 v2 namespace，避免与并行运行的主压测抢同名空间。
	midnightNamespace = "loadtest-midnight"
	// midnightRequestTimeout 是单次上报上限（短请求，超时即暴露卡死而非挂住整轮）。
	midnightRequestTimeout = 10 * time.Second
	// midnightFlushSettle 是收尾等待：异步日表写入通道是 500ms 攒批，落地需要一点时间。
	midnightFlushSettle = 3 * time.Second
	// metricSampleBaseTable 是日表基名（与 model.MetricSampleV2.TableName() 一致）。
	metricSampleBaseTable = "metric_sample"
)

// midnightTarget 返回本次要跨越的 UTC 午夜：默认「下一个 UTC 00:00」。
// MIDNIGHT_TARGET 仅用于冒烟（验证用例本身可用），正式运行必须走默认值。
func midnightTarget(t *testing.T) time.Time {
	t.Helper()
	if raw := strings.TrimSpace(os.Getenv("MIDNIGHT_TARGET")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatalf("MIDNIGHT_TARGET 不是合法 RFC3339（%q）：%v", raw, err)
		}
		return parsed.UTC()
	}
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

// buildBeaconTo 把控制面编译到指定路径。不用 harness.BuildBeacon 的固定输出路径：
// 本轮可能同时有其它压测在构建，共用同一可执行文件路径会互相覆盖。
func buildBeaconTo(repoRoot, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, "./apps/server/cmd/beacon")
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("构建控制面失败：%w\n%s", err, string(out))
	}
	return nil
}

// openMidnightProbeDB 打开一个直连该 sqlite 文件的核对用连接，用于从外部核对日表落盘事实。
// 控制面正持有写连接，但库是 WAL 模式（store.Open 对 sqlite 自动注入），多读者不阻塞写者。
func openMidnightProbeDB(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: dbPath,
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开核对用连接失败（%s）：%v", dbPath, err)
	}
	t.Cleanup(func() { store.Close(db) })
	return db
}

// reportMetricBucket 向 v2 指标上报端点发一个 5s 桶（bucket 由调用方给，保证同实例内唯一以免被去重）。
// 返回状态码与响应片段。
func reportMetricBucket(ctx context.Context, client *http.Client, baseURL string, v2 *v2Bootstrap,
	bucketMs int64, guard harness.ProcessGuard) (int, string, error) {
	raw, err := json.Marshal(map[string]any{
		"namespace": v2.Namespace,
		"serverId":  v2.ServerIDs[0],
		"kind":      model.ServerKindProxy,
		"samples": []map[string]any{{
			"bucketStartMs": bucketMs,
			"sampleCount":   1,
			"cpuPctAvg":     10.0,
			"cpuPctMax":     20.0,
			"memUsedMbAvg":  512.0,
			"memMaxMb":      2048,
			"connAvg":       4,
			"connMax":       8,
			"backendUp":     2,
			"backendTotal":  2,
			"reportRttMs":   3,
		}},
	})
	if err != nil {
		return 0, "", fmt.Errorf("编码指标上报体失败：%w", err)
	}
	reqCtx, cancel, err := harness.RequestContext(ctx, midnightRequestTimeout, guard)
	if err != nil {
		return 0, "", fmt.Errorf("构造请求上下文失败：%w", err)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/beacon/v2/agent/metrics/report", bytes.NewReader(raw))
	if err != nil {
		return 0, "", fmt.Errorf("构造指标上报请求失败：%w", err)
	}
	for _, h := range v2ReportHeaders(v2) {
		header := h
		req.Header.Set(header.key, header.value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("指标上报请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, v2SnippetLimit))
	return resp.StatusCode, string(snippet), nil
}

// dailyTableStat 是一张日表的落盘统计。
type dailyTableStat struct {
	Table  string
	Day    string // 表名后缀的 UTC 日（YYYYMMDD）
	Count  int
	Min    int64
	Max    int64
	Exists bool
}

// dailyTableDay 从日表名解析出它代表的 UTC 日（YYYYMMDD）；非法后缀返回空串。
func dailyTableDay(table string) string {
	suffix := strings.TrimPrefix(table, metricSampleBaseTable+"_")
	if len(suffix) != 8 || suffix == table {
		return ""
	}
	return suffix
}

// utcDayOfMs 返回毫秒时间戳所属的 UTC 日（YYYYMMDD）。
func utcDayOfMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("20060102")
}

// scanDailyTableStat 统计单张日表的行数与 bucket 边界；表不存在返回 Exists=false。
func scanDailyTableStat(t *testing.T, db *gorm.DB, table string) dailyTableStat {
	t.Helper()
	stat := dailyTableStat{Table: table, Day: dailyTableDay(table)}
	if !db.Migrator().HasTable(table) {
		return stat
	}
	stat.Exists = true
	type bounds struct {
		Cnt int
		Min *int64
		Max *int64
	}
	var b bounds
	if err := db.Table(table).
		Select("COUNT(*) AS cnt, MIN(bucket_start_ms) AS min, MAX(bucket_start_ms) AS max").
		Scan(&b).Error; err != nil {
		t.Fatalf("查日表 %s 边界失败：%v", table, err)
	}
	stat.Count = b.Cnt
	if b.Min != nil {
		stat.Min = *b.Min
	}
	if b.Max != nil {
		stat.Max = *b.Max
	}
	return stat
}

// listMetricDailyTables 枚举库中全部 metric_sample_* 日表，按名排序（供逐表核对归属）。
func listMetricDailyTables(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("枚举库表失败：%v", err)
	}
	var out []string
	for _, name := range tables {
		if dailyTableDay(name) != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// assertEveryRowInOwnDayTable 是通用不变量：逐表逐行核对，每行的 bucket 必须属于该表名所指的 UTC 日。
//
// 这是本用例最强的判据，且**不依赖日界是否落在观测窗口内**——任何串表（写错表、按本地日期拼表名、
// 跨日分流错误）都会在这里被逐行抓出，冒烟模式下同样有判别力。
func assertEveryRowInOwnDayTable(t *testing.T, db *gorm.DB) (checked int, violations []string) {
	t.Helper()
	for _, table := range listMetricDailyTables(t, db) {
		wantDay := dailyTableDay(table)
		type row struct {
			ServerID      string
			BucketStartMs int64
		}
		var rows []row
		if err := db.Table(table).Select("server_id, bucket_start_ms").Find(&rows).Error; err != nil {
			violations = append(violations, fmt.Sprintf("读日表 %s 失败：%v", table, err))
			continue
		}
		for _, r := range rows {
			gotDay := utcDayOfMs(r.BucketStartMs)
			if gotDay != wantDay {
				violations = append(violations, fmt.Sprintf(
					"串表：日表 %s（应只含 UTC 日 %s 的行）含 bucket=%d 的行，该行属于 UTC 日 %s（serverId=%s）",
					table, wantDay, r.BucketStartMs, gotDay, r.ServerID))
			}
			checked++
		}
	}
	return checked, violations
}

// midnightRunOutcome 汇总一次真实跨午夜运行的全部证据。
type midnightRunOutcome struct {
	Target       time.Time
	Started      time.Time
	Finished     time.Time
	DenseStart   time.Time
	DenseEnd     time.Time
	BoundaryMs   int64 // 密集窗口内的日界毫秒（无日界时为 0）
	CrossingReal bool  // 密集窗口是否真的跨越了 UTC 日界
	BeforeDay    string
	AfterDay     string

	IdleReports  int
	DenseReports int
	ReportErrors []string

	TableAfterAt time.Time // 首次观测到跨日日表存在的时刻（零值=未观测到）
	RowsChecked  int
	Tables       []dailyTableStat
	// Violations 是全部不变量违规项（空=全部通过）。先收集、写进报告、再统一报红，
	// 使失败时也有完整可复核的上下文。
	Violations []string
}

// collectViolations 收集全部不变量违规项（**只收集、不中止**）。
//
// 为什么不直接 t.Fatalf：本函数的结果要进报告，而报告必须能在断言失败时也落盘——
// 失败场景正是最需要证据的时候。跨午夜 19 小时的运行换来一句「串表」而无上下文是不可接受的，
// 故由调用方先写报告、再统一报红。
func collectViolations(t *testing.T, db *gorm.DB, out *midnightRunOutcome, crossing bool,
	boundaryMs int64, realMidnight bool, target time.Time, denseStart time.Time, afterTable string) []string {
	t.Helper()
	var violations []string

	// 第 1 层：通用不变量（逐行归属），两种模式都断言。
	rowsChecked, rowViolations := assertEveryRowInOwnDayTable(t, db)
	out.RowsChecked = rowsChecked
	violations = append(violations, rowViolations...)
	if rowsChecked == 0 {
		violations = append(violations, "库中没有任何日表行：上报虽返回 202，但异步入库未落盘，本轮证据无效")
	}
	if len(out.ReportErrors) > 0 {
		violations = append(violations, fmt.Sprintf("本轮有 %d 次上报未获 202（前 3 条）：%v",
			len(out.ReportErrors), firstN(out.ReportErrors, 3)))
	}

	// 第 2 层：跨日不变量（仅当窗口真含日界；否则如实标注跳过，不伪装通过）。
	if !crossing {
		t.Logf("密集窗口内无 UTC 日界，跨日不变量按设计跳过；本轮仅证明通用不变量（%d 行归属正确）", out.RowsChecked)
		return violations
	}

	beforeStat := scanDailyTableStat(t, db, store.DailyTableName(metricSampleBaseTable, denseStart))
	afterStat := scanDailyTableStat(t, db, afterTable)
	if !beforeStat.Exists {
		violations = append(violations, fmt.Sprintf(
			"跨日前日表 %s 不存在：等待期 / 密集期前半的上报未落盘", beforeStat.Table))
	}
	if !afterStat.Exists {
		violations = append(violations, fmt.Sprintf(
			"跨日后日表 %s 不存在：真实跨越 UTC 00:00 后未按新日期建表", afterStat.Table))
	}
	if beforeStat.Exists && beforeStat.Max >= boundaryMs {
		violations = append(violations, fmt.Sprintf(
			"跨日前日表 %s 含日界之后的行（max bucket=%d ≥ 日界 %d）——跨日写入串表",
			beforeStat.Table, beforeStat.Max, boundaryMs))
	}
	if afterStat.Exists && afterStat.Min < boundaryMs {
		violations = append(violations, fmt.Sprintf(
			"跨日后日表 %s 含日界之前的行（min bucket=%d < 日界 %d）——跨日写入串表",
			afterStat.Table, afterStat.Min, boundaryMs))
	}
	if realMidnight && out.TableAfterAt.IsZero() {
		violations = append(violations, fmt.Sprintf(
			"未观测到跨日日表 %s 出现（午夜=%s）——须确认建表时点",
			afterTable, target.Format(time.RFC3339)))
	}
	return violations
}

// renderMidnightReport 把一次运行的证据渲染成 Markdown。
func renderMidnightReport(o midnightRunOutcome, realMidnight bool) string {
	var b strings.Builder
	mode := "真实午夜"
	if !realMidnight {
		mode = "冒烟（MIDNIGHT_TARGET 注入，不构成真实午夜证据）"
	}
	fmt.Fprintf(&b, "# 真实时钟跨 UTC 00:00 日表切换\n\n")
	fmt.Fprintf(&b, "- 模式：%s\n", mode)
	fmt.Fprintf(&b, "- 目标午夜：%s\n", o.Target.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 起止：%s → %s（%.2f 小时）\n",
		o.Started.Format(time.RFC3339), o.Finished.Format(time.RFC3339), o.Finished.Sub(o.Started).Hours())
	fmt.Fprintf(&b, "- 密集窗口：%s → %s\n",
		o.DenseStart.Format(time.RFC3339), o.DenseEnd.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 上报：等待期 %d 行 / 密集期 %d 行，失败 %d\n",
		o.IdleReports, o.DenseReports, len(o.ReportErrors))
	fmt.Fprintf(&b, "- 逐行核对：%d 行全部落在自己 UTC 日的日表内\n", o.RowsChecked)

	fmt.Fprintf(&b, "\n## 日界跨越\n\n")
	if o.CrossingReal {
		fmt.Fprintf(&b, "- 密集窗口跨越 UTC 日界：**是**（%s → %s，日界 %d = %s）\n",
			o.BeforeDay, o.AfterDay, o.BoundaryMs, time.UnixMilli(o.BoundaryMs).UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(&b, "- 密集窗口跨越 UTC 日界：**否**（窗口内无日界，跨日不变量已按设计跳过）\n")
		fmt.Fprintf(&b, "  本模式只断言通用不变量（逐行归属），不构成跨日证据。\n")
	}

	fmt.Fprintf(&b, "\n## 日表落盘\n\n")
	fmt.Fprintf(&b, "| 日表 | UTC 日 | 存在 | 行数 | bucket 最小 | bucket 最大 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
	for _, s := range o.Tables {
		fmt.Fprintf(&b, "| %s | %s | %v | %d | %d | %d |\n",
			s.Table, s.Day, s.Exists, s.Count, s.Min, s.Max)
	}

	fmt.Fprintf(&b, "\n## 建表时点\n\n")
	if o.TableAfterAt.IsZero() {
		fmt.Fprintf(&b, "- 跨日日表首次被观测到存在的时刻：**未观测到**\n")
	} else {
		fmt.Fprintf(&b, "- 跨日日表首次被观测到存在：%s（午夜后 %s）\n",
			o.TableAfterAt.Format(time.RFC3339), o.TableAfterAt.Sub(o.Target).Round(time.Millisecond))
	}

	if len(o.ReportErrors) > 0 {
		fmt.Fprintf(&b, "\n## 上报异常（%d 条，前 5）\n\n", len(o.ReportErrors))
		for _, e := range firstN(o.ReportErrors, 5) {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}

	// 不变量判定必须进报告：失败场景正是最需要证据的时候。
	fmt.Fprintf(&b, "\n## 不变量判定\n\n")
	if len(o.Violations) == 0 {
		fmt.Fprintf(&b, "- 全部通过：逐行归属 %d 行、跨日边界（若窗口含日界）均无违规\n", o.RowsChecked)
	} else {
		fmt.Fprintf(&b, "- **发现 %d 项违规**：\n\n", len(o.Violations))
		for _, v := range o.Violations {
			fmt.Fprintf(&b, "  - %s\n", v)
		}
	}
	fmt.Fprintf(&b, "\n## 口径说明\n\n")
	fmt.Fprintf(&b, "- 本用例只验证「真实时钟推进到 UTC 00:00 时，生产写入路径的日表切换正确」，")
	fmt.Fprintf(&b, "不替代注入日期用例（后者覆盖并发建表、缓存隔离、UTC 归属等机制分支）。\n")
	fmt.Fprintf(&b, "- 写入经生产链路：v2 上报端点 → 鉴权 → 内存窗口去重 → 异步入队 → 按行内时间戳定日表 → 建表 / 落盘。\n")
	fmt.Fprintf(&b, "- bucket 取真实上报时刻对齐 5s；判据分两层：通用不变量（逐行归属，两种模式都断言）")
	fmt.Fprintf(&b, "与跨日不变量（仅窗口真含日界时断言）。\n")
	return b.String()
}

// firstN 取前 n 个元素（不足则全取）。
func firstN(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

// TestMidnightRolloverRealClock 用真实时钟跨越 UTC 00:00，验证生产写入路径在真实跨日窗口的日表切换。
func TestMidnightRolloverRealClock(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	target := midnightTarget(t)
	lead := durationEnv(t, "MIDNIGHT_LEAD", "3m")
	tail := durationEnv(t, "MIDNIGHT_TAIL", "3m")
	idleEvery := durationEnv(t, "MIDNIGHT_IDLE", "60s")
	denseEvery := durationEnv(t, "MIDNIGHT_DENSE", "5s")
	dbName := envOr("MIDNIGHT_DB_NAME", "beacon-midnight.db")

	started := time.Now().UTC()
	denseStart := target.Add(-lead)
	denseEnd := target.Add(tail)
	// 日界由密集窗口边界推导（而非 target±1 天）：这样冒烟注入任意时刻时语义仍自洽——
	// 窗口内没有日界就如实报「无日界」，而不是拿一个不存在的「前一天」硬断言。
	beforeDay := denseStart.Format("20060102")
	afterDay := denseEnd.Format("20060102")
	crossing := beforeDay != afterDay
	realMidnight := os.Getenv("MIDNIGHT_TARGET") == ""
	boundaryMs := time.Date(denseEnd.Year(), denseEnd.Month(), denseEnd.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()

	t.Logf("目标午夜 %s（UTC）；当前 %s；距午夜 %.2f 小时；真实午夜模式=%v",
		target.Format(time.RFC3339), started.Format(time.RFC3339), target.Sub(started).Hours(), realMidnight)
	t.Logf("密集窗口 %s → %s；窗口内含 UTC 日界=%v（%s → %s）",
		denseStart.Format(time.RFC3339), denseEnd.Format(time.RFC3339), crossing, beforeDay, afterDay)
	if !realMidnight {
		t.Logf("注意：MIDNIGHT_TARGET 已注入，本次为冒烟（验证用例可用），不构成真实午夜证据")
	}
	// 目标时刻已过去的话，等待期与密集窗口都会立即结束、一个请求都发不出去，
	// 最终只会以「没有日表行」失败——那个报错指向错误的方向（看起来像入库坏了）。
	// 故在起控制面之前就快速失败，并把真实原因说清楚。
	if !denseEnd.After(started) {
		t.Fatalf("目标时刻已过去：目标 %s，密集窗口结束 %s，当前 %s——请注入一个未来的 MIDNIGHT_TARGET（正式运行请留空以走真实午夜）",
			target.Format(time.RFC3339), denseEnd.Format(time.RFC3339), started.Format(time.RFC3339))
	}

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	dbPath := filepath.Join(workDir, dbName)
	// 只清自己的库（含 WAL / SHM），不碰同目录下主压测的库。
	for _, suffix := range []string{"", "-shm", "-wal"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("清理遗留库 %s%s 失败：%v", dbPath, suffix, err)
		}
	}

	binPath := filepath.Join(repoRoot, ".tmp", "beacon-midnight")
	if err := buildBeaconTo(repoRoot, binPath); err != nil {
		t.Fatalf("%v", err)
	}

	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: binPath, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: "beacon-midnight",
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": envOr("LOADTEST_CP_LOG_LEVEL", defaultCPLogLevel)},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	stdoutPath, stderrPath := cp.LogPaths()
	t.Logf("控制面已就绪：%s（日志：%s / %s）", baseURL, stdoutPath, stderrPath)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	v2 := bootstrapV2Identity(t, baseURL, token, midnightNamespace, 1, cp)

	client := newLoadClient(4)
	defer client.CloseIdleConnections()
	probeDB := openMidnightProbeDB(t, dbPath)

	out := midnightRunOutcome{
		Target: target, Started: started, DenseStart: denseStart, DenseEnd: denseEnd,
		CrossingReal: crossing, BeforeDay: beforeDay, AfterDay: afterDay, BoundaryMs: boundaryMs,
	}
	var lastBucket int64
	afterTable := store.DailyTableName(metricSampleBaseTable, time.UnixMilli(boundaryMs).UTC())

	// reportOne 发一行上报并登记结果；dense 阶段顺带核对跨日日表是否已出现。
	reportOne := func(dense bool, at time.Time) {
		bucket := at.UnixMilli() / 5000 * 5000
		if bucket <= lastBucket {
			bucket = lastBucket + 5000
		}
		lastBucket = bucket
		status, snippet, err := reportMetricBucket(context.Background(), client, baseURL, v2, bucket, cp)
		if err != nil {
			out.ReportErrors = append(out.ReportErrors, fmt.Sprintf("%s 上报失败：%v", at.Format(time.RFC3339), err))
			return
		}
		if status != http.StatusAccepted {
			out.ReportErrors = append(out.ReportErrors,
				fmt.Sprintf("%s 上报期望 202，实际 %d；片段：%s", at.Format(time.RFC3339), status, snippet))
			return
		}
		if dense {
			out.DenseReports++
			if out.TableAfterAt.IsZero() && probeDB.Migrator().HasTable(afterTable) {
				out.TableAfterAt = time.Now().UTC()
				t.Logf("首次观测到日表 %s 存在：%s", afterTable, out.TableAfterAt.Format(time.RFC3339))
			}
		} else {
			out.IdleReports++
		}
	}

	// 等待期：低频保活，持续给窗口起始日那张日表写入真实行。
	for {
		now := time.Now().UTC()
		if !now.Before(denseStart) {
			break
		}
		reportOne(false, now)
		next := now.Add(idleEvery)
		if next.After(denseStart) {
			next = denseStart
		}
		time.Sleep(time.Until(next))
	}

	// 密集窗口：覆盖日界前后，每 denseEvery 一行。
	t.Logf("进入密集窗口：%s ~ %s（每 %s 一行）",
		denseStart.Format(time.RFC3339), denseEnd.Format(time.RFC3339), denseEvery)
	for {
		now := time.Now().UTC()
		if !now.Before(denseEnd) {
			break
		}
		reportOne(true, now)
		time.Sleep(denseEvery)
	}

	time.Sleep(midnightFlushSettle)
	out.Finished = time.Now().UTC()
	for _, table := range listMetricDailyTables(t, probeDB) {
		out.Tables = append(out.Tables, scanDailyTableStat(t, probeDB, table))
	}

	// —— 判定 ——
	// 关键顺序：**先把证据写进报告、再报红**（collectViolations 只收集不中止）。
	// 失败场景正是最需要证据的时候，若让 t.Fatalf 先中止，跨午夜 19 小时换来的一句
	// 「串表」就没有任何可复核的上下文。
	out.Violations = collectViolations(t, probeDB, &out, crossing, boundaryMs, realMidnight, target, denseStart, afterTable)
	report := renderMidnightReport(out, realMidnight)
	reportPath := filepath.Join(workDir, "midnight-rollover-"+started.Format("20060102-150405")+".md")
	if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		t.Fatalf("写跨午夜报告失败：%v", err)
	}
	t.Logf("\n%s", report)
	t.Logf("报告已落盘：%s", reportPath)

	// 报告落盘之后才报红：跨午夜 19 小时的运行，失败时必须留下可复核的完整证据。
	if len(out.Violations) > 0 {
		t.Fatalf("跨午夜不变量有 %d 项违规（完整证据见报告 %s）：%v",
			len(out.Violations), reportPath, firstN(out.Violations, 3))
	}
}

// 编译期引用：本文件独立运行（-run '^TestMidnightRolloverRealClock'），
// 这些 helper 在主流程里才被调用，此处显式引用以消解 unused 类 linter。
var (
	_ = dailyTableDay
	_ = utcDayOfMs
	_ = scanDailyTableStat
	_ = listMetricDailyTables
	_ = assertEveryRowInOwnDayTable
	_ = renderMidnightReport
	_ = firstN
	_ = midnightTarget
	_ = buildBeaconTo
	_ = openMidnightProbeDB
	_ = reportMetricBucket
)

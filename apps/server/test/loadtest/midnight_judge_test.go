//go:build e2e && loadtest

// 跨日判定分支的秒级守护（midnight_rollover_test.go 的配套单测）。
//
// 为什么需要它：真实午夜长跑要等 19 小时，而它的**全部价值**都压在 crossing=true 那条分支上
// （逐行归属 + 日界前后不串表 + 建表时点）。若该分支本身写错，19 小时换来的是一个假结论——
// 而「通过」与「判定逻辑根本没执行」在报告上长得一模一样。故此处用毫秒级夹具把该分支的
// 通过与各类失败形态逐一钉死，让长跑跑在已验证的判据上。
//
// 夹具直接调生产同款接缝（store.EnsureDailyTable + db.Table(name).Create），不复制判定逻辑，
// 保证测的就是长跑真正会走的那段代码。

package loadtest_e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// midnightFixture 是一套跨日夹具：真实文件库 + D/D+1 两张日表。
type midnightFixture struct {
	DB         *gorm.DB
	DayD       time.Time // 日界前一天（UTC 零点）
	BoundaryMs int64     // 日界毫秒（= D+1 的 UTC 零点）
	TableD     string
	TableNext  string
}

// newMidnightFixture 建真实文件 sqlite 库并建出 D 与 D+1 两张日表。
// 用文件库而非内存库：与长跑同形（长跑也是文件库），且避免内存库的池退化差异。
func newMidnightFixture(t *testing.T) *midnightFixture {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "midnight_judge.db"),
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开夹具库失败：%v", err)
	}
	t.Cleanup(func() { store.Close(db) })

	// 固定日期而非 time.Now()：判定只依赖「表名所指 UTC 日」与「行 bucket 所属 UTC 日」的关系，
	// 与今天是哪天无关；固定日期让失败信息可复现。
	dayD := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	tableD, err := store.EnsureDailyTable(db, &model.MetricSampleV2{}, dayD)
	if err != nil {
		t.Fatalf("建 D 日表失败：%v", err)
	}
	dayNext := dayD.AddDate(0, 0, 1)
	tableNext, err := store.EnsureDailyTable(db, &model.MetricSampleV2{}, dayNext)
	if err != nil {
		t.Fatalf("建 D+1 日表失败：%v", err)
	}
	return &midnightFixture{
		DB: db, DayD: dayD, BoundaryMs: dayNext.UnixMilli(),
		TableD: tableD, TableNext: tableNext,
	}
}

// writeRow 向指定日表写一行（serverID 供断言区分）。
func (f *midnightFixture) writeRow(t *testing.T, table, serverID string, bucketMs int64) {
	t.Helper()
	row := &model.MetricSampleV2{ServerID: serverID, Kind: "backend", BucketStartMs: bucketMs}
	if err := f.DB.Table(table).Create(row).Error; err != nil {
		t.Fatalf("写 %s（serverId=%s）失败：%v", table, serverID, err)
	}
}

// judge 以 crossing=true 跑长跑同款判定，返回违规清单。
func (f *midnightFixture) judge(t *testing.T, realMidnight bool, tableAfterAt time.Time) []string {
	t.Helper()
	out := &midnightRunOutcome{TableAfterAt: tableAfterAt}
	return collectViolations(t, f.DB, out, true, f.BoundaryMs, realMidnight, time.UnixMilli(f.BoundaryMs).UTC(),
		f.DayD, f.TableNext)
}

// assertNoViolation 断言无违规。
func assertNoViolation(t *testing.T, violations []string) {
	t.Helper()
	if len(violations) > 0 {
		t.Fatalf("本应无违规，实际 %d 项：%v", len(violations), violations)
	}
}

// assertViolationContains 断言存在包含指定关键词的违规项（用关键词而非全等，
// 避免措辞微调就让用例失效；但必须命中，不能因清单为空而静默通过）。
func assertViolationContains(t *testing.T, violations []string, keyword string) {
	t.Helper()
	for _, v := range violations {
		if strings.Contains(v, keyword) {
			return
		}
	}
	t.Fatalf("应有一条违规包含 %q，实际 %d 项：%v", keyword, len(violations), violations)
}

// TestMidnightJudgeCrossingPasses 校验跨日判定在正确数据上**通过**（防止判定恒报错、
// 令 19 小时长跑无谓失败）。
func TestMidnightJudgeCrossingPasses(t *testing.T) {
	f := newMidnightFixture(t)
	// D 表：日界之前的行；D+1 表：日界及之后的行（日界那一刻归 D+1，故用 >= boundary）。
	f.writeRow(t, f.TableD, "d-1", f.BoundaryMs-5000)
	f.writeRow(t, f.TableD, "d-2", f.BoundaryMs-1)
	f.writeRow(t, f.TableNext, "n-1", f.BoundaryMs)
	f.writeRow(t, f.TableNext, "n-2", f.BoundaryMs+5000)

	assertNoViolation(t, f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second)))
}

// TestMidnightJudgeDetectsCrossTableRows 校验**串表**被抓出：这正是长跑要证明的核心不变量，
// 若判定漏掉它，长跑无论如何都会「通过」。
func TestMidnightJudgeDetectsCrossTableRows(t *testing.T) {
	t.Run("前日表混入日界之后的行", func(t *testing.T) {
		f := newMidnightFixture(t)
		f.writeRow(t, f.TableD, "d-ok", f.BoundaryMs-5000)
		f.writeRow(t, f.TableD, "d-bad", f.BoundaryMs) // 越界：属 D+1 却写进 D
		f.writeRow(t, f.TableNext, "n-ok", f.BoundaryMs)

		v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
		assertViolationContains(t, v, "跨日写入串表")
	})

	t.Run("后日表混入日界之前的行", func(t *testing.T) {
		f := newMidnightFixture(t)
		f.writeRow(t, f.TableD, "d-ok", f.BoundaryMs-5000)
		f.writeRow(t, f.TableNext, "n-bad", f.BoundaryMs-1) // 越界：属 D 却写进 D+1
		f.writeRow(t, f.TableNext, "n-ok", f.BoundaryMs)

		v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
		assertViolationContains(t, v, "跨日写入串表")
	})

	t.Run("本地日期与 UTC 日期错位也归串表", func(t *testing.T) {
		// 长跑真正要防的形态：按本地日期拼表名。这里用「D 表里放一个 UTC 属 D+1 的行」等价表达，
		// 并额外确认逐行归属层（不依赖日界）也能独立抓出它。
		f := newMidnightFixture(t)
		f.writeRow(t, f.TableD, "d-bad", f.BoundaryMs+3600_000)

		v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
		assertViolationContains(t, v, "串表")
	})
}

// TestMidnightJudgeDetectsMissingTables 校验「日表根本不存在」被抓出（否则空库会被当成通过）。
func TestMidnightJudgeDetectsMissingTables(t *testing.T) {
	t.Run("跨日后日表缺失", func(t *testing.T) {
		f := newMidnightFixture(t)
		f.writeRow(t, f.TableD, "d-1", f.BoundaryMs-5000)
		if err := f.DB.Migrator().DropTable(f.TableNext); err != nil {
			t.Fatalf("删除 D+1 表失败：%v", err)
		}
		v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
		assertViolationContains(t, v, "不存在")
	})

	t.Run("跨日前日表缺失", func(t *testing.T) {
		f := newMidnightFixture(t)
		f.writeRow(t, f.TableNext, "n-1", f.BoundaryMs)
		if err := f.DB.Migrator().DropTable(f.TableD); err != nil {
			t.Fatalf("删除 D 表失败：%v", err)
		}
		v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
		assertViolationContains(t, v, "不存在")
	})
}

// TestMidnightJudgeDetectsUnobservedAfterTable 校验「真实午夜模式下从未观测到跨日表出现」被抓出：
// 该条只在 realMidnight 下生效（注入冒烟不算真实证据），是长跑「真等」的意义所在。
func TestMidnightJudgeDetectsUnobservedAfterTable(t *testing.T) {
	f := newMidnightFixture(t)
	f.writeRow(t, f.TableD, "d-1", f.BoundaryMs-5000)
	f.writeRow(t, f.TableNext, "n-1", f.BoundaryMs)

	// realMidnight=true 且 TableAfterAt 为零值 → 应报「未观测到」。
	v := f.judge(t, true, time.Time{})
	assertViolationContains(t, v, "未观测到跨日日表")

	// 同一份数据、注入模式（realMidnight=false）→ 不报该条（注入模式本就不构成真实午夜证据）。
	assertNoViolation(t, f.judge(t, false, time.Time{}))
}

// TestMidnightJudgeSkipsCrossDayWhenNoBoundary 校验 crossing=false 时跨日判据**按设计跳过**：
// 此时只断言逐行归属，不因「没有日界」而误报失败（冒烟常见形态）。
func TestMidnightJudgeSkipsCrossDayWhenNoBoundary(t *testing.T) {
	f := newMidnightFixture(t)
	f.writeRow(t, f.TableD, "d-1", f.BoundaryMs-5000)

	out := &midnightRunOutcome{}
	v := collectViolations(t, f.DB, out, false, f.BoundaryMs, true,
		time.UnixMilli(f.BoundaryMs).UTC(), f.DayD, f.TableNext)
	assertNoViolation(t, v)
	if out.RowsChecked != 1 {
		t.Fatalf("应核对到 1 行，实际 %d", out.RowsChecked)
	}
}

// TestMidnightJudgeDetectsEmptyDatabase 校验空库被抓出：上报全 202 但一行未落盘时，
// 不能因「没有行可查」而静默通过。
func TestMidnightJudgeDetectsEmptyDatabase(t *testing.T) {
	f := newMidnightFixture(t)
	v := f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second))
	assertViolationContains(t, v, "没有任何日表行")
}

// TestMidnightJudgeReportsUploadErrors 校验上报异常会进违规清单（长跑若全程 401 也必须报红，
// 而不是因「表里恰好有旧行」而通过）。
func TestMidnightJudgeReportsUploadErrors(t *testing.T) {
	f := newMidnightFixture(t)
	f.writeRow(t, f.TableD, "d-1", f.BoundaryMs-5000)
	f.writeRow(t, f.TableNext, "n-1", f.BoundaryMs)

	out := &midnightRunOutcome{
		ReportErrors: []string{"2026-07-11T23:59:00Z 上报期望 202，实际 401；片段：…"},
		TableAfterAt: time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second),
	}
	v := collectViolations(t, f.DB, out, true, f.BoundaryMs, true,
		time.UnixMilli(f.BoundaryMs).UTC(), f.DayD, f.TableNext)
	assertViolationContains(t, v, "未获 202")
}

// TestMidnightJudgeBoundaryIsInclusive 钉死日界归属口径：日界那一刻（== boundaryMs）算**次日**。
// 口径若反过来（把 == 归前一日），长跑会在午夜整点那一行上误报串表。
func TestMidnightJudgeBoundaryIsInclusive(t *testing.T) {
	f := newMidnightFixture(t)
	// 日界整点归 D+1：放 D+1 应通过。
	f.writeRow(t, f.TableNext, "boundary-next", f.BoundaryMs)
	assertNoViolation(t, f.judge(t, true, time.UnixMilli(f.BoundaryMs).UTC().Add(time.Second)))

	// 同一时刻放 D 应被抓（证明上一断言不是因为判据不敏感才通过）。
	f2 := newMidnightFixture(t)
	f2.writeRow(t, f2.TableD, "boundary-d", f2.BoundaryMs)
	v := f2.judge(t, true, time.UnixMilli(f2.BoundaryMs).UTC().Add(time.Second))
	assertViolationContains(t, v, "跨日写入串表")
}

// TestMidnightJudgeFixtureUsesRealTables 是夹具自检：确认夹具真的建出了两张不同日表
// （若表名退化成同一张，上面所有断言都会失去意义）。
func TestMidnightJudgeFixtureUsesRealTables(t *testing.T) {
	f := newMidnightFixture(t)
	if f.TableD == f.TableNext {
		t.Fatalf("夹具两日表名不应相同，实际同为 %s", f.TableD)
	}
	if want := fmt.Sprintf("%s_20260711", metricSampleBaseTable); f.TableD != want {
		t.Fatalf("D 日表名应为 %s，实际 %s", want, f.TableD)
	}
	if want := fmt.Sprintf("%s_20260712", metricSampleBaseTable); f.TableNext != want {
		t.Fatalf("D+1 日表名应为 %s，实际 %s", want, f.TableNext)
	}
	if !f.DB.Migrator().HasTable(f.TableD) || !f.DB.Migrator().HasTable(f.TableNext) {
		t.Fatal("夹具应已建出两张日表")
	}
}

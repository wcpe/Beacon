package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// —— P1-2 判别性用例：归档数据面语句自带长期限，不被 call 预算误杀 ——
//
// 背景（独立评审 P1-2）：store 的连接等待防护给每条语句套了 `call-timeout-ms`（默认 5s），
// 而 `database/sql` 把该 ctx 同时绑到「等连接」「语句执行」与「Rows 生存期」上。
// 5s 是按**在线业务语句**定的（实测中位 1.482ms），但归档的存在理由就是大表：其验证步有
// 按设计无 LIMIT 的全区间查询（`countRows` 的 COUNT(*) / `orderedPKs` 的全量 Pluck /
// `hashRows` 的 IN 大集合 / `applyRange` 覆盖的全部未归档历史）。防护上线后这些查询会在
// 5s 处被掐断 → runVerify 失败 → item 判 failed → 归档任务失败：表越大越必然失败。
//
// 修复：归档 runner 的 hot / archive 两库在整单范围内绑定 `archiveLongQueryTimeout`（见
// archive_pipeline.go 的 bindLongBudget）。本文件锁定「绑定确实生效」。
//
// ## 判据为什么这样选（记录取舍，避免日后被"加强"成脆弱用例）
//
// 本组**不**用「跑一条耗时超过 call 预算的语句、断言它跑完」作判据。该写法曾试过，在此仓不可靠：
//   - 单条语句的耗时依机器与 `-race` 而变（同一条递归 CTE 实测普通 1.8s / `-race` 下 73s），
//     于是「是否超过预算」这个前提本身不稳定，用例要么假绿要么超时；
//   - 更糟的是它与连接寿命耦合：`ConnMaxLifetimeSec` 到期会回收空闲连接，而内存 sqlite
//     （`mode=memory&cache=shared`）在**全部**连接关闭时整库消失——`-race` 下长语句恰好触发，
//     表现为 `no such table`，把「防护误杀」伪装成「表不存在」这种误导性失败。
//
// 改为**断言 ctx 的期限**这一确定性判据，依据是两步可组合的推理：
//  1. store 层的预算判据是「ctx 无 deadline 则补 default，有则原样尊重」——
//     该行为已由 `TestConnPoolDoesNotOverrideCallerDeadline` 与
//     `TestConnPoolDoesNotExtendCallerDeadlineForTransaction` 锁定。
//  2. TestArchiveDataPlaneStatementsCarryLongDeadline 证明归档 runner 的语句**确实带着长期限的 ctx**
//     （已反向验证：把 bindLongBudget 里的两处 WithContext 去掉，该用例立刻红并指认热库未带期限）。
//
// 两条合起来即得「归档长查询不被 call 预算掐断」这个端到端结论，且完全不依赖计时与机器速度。
// gorm 把 `db.Statement.Context` 用于驱动调用，故断言它就是断言真正下传的那条 ctx（不是旁证）。
//
// 本文件三条用例的分工（勿高估后两条的判别力）：
//   - CarryLongDeadline：**主判据**，断言绑定期限本身（有牙，已反向验证）；
//   - JobEndToEndWithTightCallBudget：判「生产入口整单能跑完」，判据是端到端结果而非耗时。

// openArchiveServiceWithCallBudget 装配归档服务，热 / 归档库都启用一个**很短的** call 预算，
// 用以在断言里制造鲜明对比（真实部署是 5s + 大表，此处是 300ms + 小库）。
//
// 用**文件**库而非内存库：内存 sqlite 在全部连接关闭时整库消失，而绑定长期限的用例会持有连接较久，
// 一旦撞上 `ConnMaxLifetimeSec` 回收就会得到误导性的 `no such table`（见上文取舍说明）。
func openArchiveServiceWithCallBudget(t *testing.T, callMs int) (*ArchiveService, *gorm.DB, *gorm.DB) {
	t.Helper()
	dir := t.TempDir()
	open := func(tag string) *gorm.DB {
		db, err := store.Open(config.DatabaseConfig{
			Driver: "sqlite", DSN: filepath.Join(dir, tag+".db"),
			MaxOpenConns: 2, MaxIdleConns: 2, ConnMaxLifetimeSec: 1800,
			CallTimeoutMs: callMs, TxTimeoutMs: 60000,
		})
		if err != nil {
			t.Fatalf("打开 %s 库失败: %v", tag, err)
		}
		db.Logger = db.Logger.LogMode(logger.Silent)
		t.Cleanup(func() { store.Close(db) })
		return db
	}
	hot, archive := open("hot"), open("arc")

	settings, err := NewSettingsService(hot, repository.NewSettingRepository(hot), repository.NewAuditLogRepository(hot))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	settings.mu.Lock()
	settings.cache[SettingArchiveBatchIntervalMs] = "0"
	settings.mu.Unlock()

	svc := NewArchiveService(hot, archive,
		store.ArchiveInfo{Mode: store.ArchiveModeSameInstance, Database: "beacon_archive", DSNMasked: "sqlite"},
		repository.NewArchiveJobRepository(hot), settings, repository.NewAuditLogRepository(hot))
	svc.now = func() time.Time { return archiveTestNow }
	return svc, hot, archive
}

// TestArchiveDataPlaneStatementsCarryLongDeadline 是本组的主判据：runner 绑定的期限必须
// (1) 真的存在、(2) 显著长于 call 预算、(3) 热 / 归档**两库都**绑上。
//
// 为什么单独锁「绑定」这一步：P1-2 的成因不是「预算太短」而是「长查询没有例外通道」，
// 而例外通道的唯一作用点就是绑定。日后若有人新增 runner 构造路径却忘了绑定
// （例如为某个新阶段另起一个未绑定的 *gorm.DB），本用例立刻指认。
//
// 为什么三库都要断：漏掉 archive 侧最危险——验证步的 `countRows(r.archive, ...)` 与
// `hashRows(r.archive, ...)` 同样是无 LIMIT 全区间查询，只绑 hot 会漏掉一半。
func TestArchiveDataPlaneStatementsCarryLongDeadline(t *testing.T) {
	svc, hot, archive := openArchiveServiceWithCallBudget(t, 300)
	day := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	seedMetricDailyTable(t, hot, day, 1)

	r := newTestRunner(hot, archive, mustDomain(t, "metric_sample"), model.ArchiveModeExecute, 100)

	// —— 反证：未绑定时两库都没有期限，故语句必落在 store 的 call 预算里 ——
	if dl, ok := r.hot.Statement.Context.Deadline(); ok {
		t.Fatalf("未绑定时不应带期限（实际 %v）——本用例的对比前提失效", dl)
	}
	if dl, ok := r.archive.Statement.Context.Deadline(); ok {
		t.Fatalf("未绑定时归档侧不应带期限（实际 %v）", dl)
	}

	cancel := r.bindLongBudget(context.Background())
	defer cancel()

	// —— 正证：两库都带上长期限 ——
	for _, side := range []struct {
		name string
		db   *gorm.DB
	}{{"热库", r.hot}, {"归档库", r.archive}} {
		dl, ok := side.db.Statement.Context.Deadline()
		if !ok {
			t.Fatalf("%s 未带期限：数据面语句会落回 call 预算，长查询将被误杀（P1-2 回归）", side.name)
		}
		remaining := time.Until(dl)
		// 余量判据：必须显著长于 call 预算（本夹具 300ms），否则等于没给例外通道。
		// 下界取 archiveLongQueryTimeout 的一半（避开 WithTimeout 的微秒级抖动，
		// 同时足以把「误绑成 call 预算」区分出来——300ms 连这个下界的 0.1% 都不到）。
		if remaining < archiveLongQueryTimeout/2 {
			t.Fatalf("%s 的期限余量只有 %v，明显短于 archiveLongQueryTimeout(%v)——疑似绑成了 call 预算",
				side.name, remaining, archiveLongQueryTimeout)
		}
		t.Logf("%s 期限余量 %v（call 预算 300ms）", side.name, remaining)
	}

	// —— 端到端：正常体量的流水线照常走完（证明绑定没有破坏正常路径）——
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: store.DailyTableName("metric_sample", day),
		Phase: model.ArchiveItemPending}
	if err := r.run(item); err != nil {
		t.Fatalf("归档流水线失败: %v", err)
	}
	if item.Phase != model.ArchiveItemDone {
		t.Fatalf("终态应为 done，实际 %s", item.Phase)
	}
	if item.VerifyPassed == nil || !*item.VerifyPassed {
		t.Fatalf("校验应通过，实际 %+v", item.VerifyPassed)
	}
	_ = svc
}

// TestArchiveJobEndToEndWithTightCallBudget 是生产入口侧的**回归保护**：在 call 预算极紧（50ms）的库上，
// 经 `CreateJob` + `drainActive`（生产的两条真实入口）完成一次 execute，整单必须成功。
//
// **诚实说明它的判别力边界（勿高估）**：本用例**不能**证明「长查询不被误杀」——
// 夹具只有 3 行数据，任何语句都远在 50ms 内完成。「绑定期限」这件事由
// TestArchiveDataPlaneStatementsCarryLongDeadline 直接断言（那一条才有牙，已反向验证）。
//
// 那它的价值是什么：把「生产装配路径上整单能跑完」钉住。若日后有人把 bindLongBudget 从 runJob
// 里删掉、或改坏了数据面语句（例如 applyRange 漏过滤、删除阶段提前退出），
// 只要造成**任何**失败，本用例就会红——它判的是端到端结果，不是某一条语句的耗时。
func TestArchiveJobEndToEndWithTightCallBudget(t *testing.T) {
	svc, hot, archive := openArchiveServiceWithCallBudget(t, 50)
	// 日表保留期以天计，故种子日必须早于 now-retention 才会"到期"（archiveTestNow 是 2026-06-20）。
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)

	job, err := svc.CreateJob(model.ArchiveModeExecute, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	svc.drainActive(context.Background())

	detail, err := svc.GetJob(job.ID)
	if err != nil {
		t.Fatalf("回读任务失败: %v", err)
	}
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("任务应 succeeded，实际 %s；items=%+v", detail.Status, detail.Items)
	}
	if len(detail.Items) == 0 {
		t.Fatal("判据空洞：任务没有任何 item，说明整单没经过数据面语句")
	}
	if detail.Items[0].Phase != model.ArchiveItemDone {
		t.Fatalf("item 应 done，实际 %s", detail.Items[0].Phase)
	}
	if detail.Items[0].VerifyPassed == nil || !*detail.Items[0].VerifyPassed {
		t.Fatalf("校验应通过，实际 %+v", detail.Items[0].VerifyPassed)
	}
	// 日表形态下热库整表被 DropTable（见 runDelete），故只断言归档侧落齐。
	if tableCount(t, archive, name) != 3 {
		t.Fatalf("归档表应 3 行")
	}
}

package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// newAuditTestDB 打开内存 sqlite 并迁移 audit_log，供过滤/分页单测（不依赖 MySQL/DSN）。
func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("迁移 audit_log 失败: %v", err)
	}
	// 清表，避免共享内存库残留串扰
	if err := db.Exec("DELETE FROM audit_log").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

// seed 追加一条审计（指定 operator/action/createdAt）。
func seed(t *testing.T, r *AuditLogRepository, operator, action string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: action,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: model.ResultOK, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// seedDetail 追加一条带指定 detail 的审计（用于 detail 关键字检索测试）。
func seedDetail(t *testing.T, r *AuditLogRepository, operator, detail string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Detail: detail, Result: model.ResultOK, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditListFilterByDetailKeyword 验证 FR-84 新增的「按 detail 关键字」LIKE 子串检索：
// 命中含子串的记录、与既有过滤 AND 叠加、空关键字不过滤，且 % / _ 不被当通配符（已转义）。
func TestAuditListFilterByDetailKeyword(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedDetail(t, r, "alice", `{"dataId":"mysql.yml","version":3}`, base)
	seedDetail(t, r, "bob", `{"dataId":"redis.yml","version":1}`, base.Add(time.Minute))
	seedDetail(t, r, "alice", `{"note":"100%_done"}`, base.Add(2*time.Minute))

	// 子串「mysql」仅命中第 1 条
	items, total, err := r.List(AuditFilter{DetailKeyword: "mysql", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("detailKeyword=mysql 应 1 条，实际 total=%d len=%d", total, len(items))
	}

	// detailKeyword 与 operator 叠加：alice 且 detail 含「version」→ 仅第 1 条（bob 的虽含 version 但非 alice）
	_, both, err := r.List(AuditFilter{Operator: "alice", DetailKeyword: "version", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("叠加查询失败: %v", err)
	}
	if both != 1 {
		t.Fatalf("operator=alice & detailKeyword=version 应 1 条，实际 %d", both)
	}

	// 空关键字不过滤 → 全量 3 条
	_, all, _ := r.List(AuditFilter{Page: 1, Size: 20})
	if all != 3 {
		t.Fatalf("空 detailKeyword 应返回全量 3 条，实际 %d", all)
	}

	// 通配符转义：搜「100%」应作为字面子串命中第 3 条（含 "100%_done"），不被当成「100 + 任意」
	_, pct, err := r.List(AuditFilter{DetailKeyword: "100%", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("通配符查询失败: %v", err)
	}
	if pct != 1 {
		t.Fatalf("detailKeyword=100%% 转义后应 1 条，实际 %d", pct)
	}

	// 下划线转义：搜「l_yml」不应命中任何记录（既无字面「l_yml」），证明 _ 未被当单字符通配
	_, us, err := r.List(AuditFilter{DetailKeyword: "l_yml", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("下划线查询失败: %v", err)
	}
	if us != 0 {
		t.Fatalf("detailKeyword=l_yml 转义后应 0 条，实际 %d", us)
	}
}

// TestAuditListFilterByTargetRefSubstring 验证 target_ref 子串匹配：
// 输入 lobby-1 可命中 pure "lobby-1" 与复合 "realhost/lobby-1"；% / _ 转义为字面。
func TestAuditListFilterByTargetRefSubstring(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedTargetRef(t, r, "lobby-1", base)
	seedTargetRef(t, r, "realhost/lobby-1", base.Add(time.Minute))
	seedTargetRef(t, r, "game-1", base.Add(2*time.Minute))
	seedTargetRef(t, r, "realhost/lobby_1", base.Add(3*time.Minute)) // 下划线字面，不应被 _ 通配误伤

	items, total, err := r.List(AuditFilter{TargetRef: "lobby-1", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("targetRef=lobby-1 应命中 2 条（含 ns 前缀），实际 total=%d len=%d", total, len(items))
	}

	// 下划线字面：搜 lobby_1 只命中带下划线那条，不被当成单字符通配
	_, us, err := r.List(AuditFilter{TargetRef: "lobby_1", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("下划线查询失败: %v", err)
	}
	if us != 1 {
		t.Fatalf("targetRef=lobby_1 转义后应 1 条，实际 %d", us)
	}
}

// seedTargetRef 追加一条指定 target_ref 的审计。
func seedTargetRef(t *testing.T, r *AuditLogRepository, targetRef string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		NamespaceCode: "realhost", Operator: "agent", Action: model.ActionInstanceRegister,
		TargetType: "instance", TargetRef: targetRef,
		Result: model.ResultOK, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditStreamCursor 验证 FR-84 流式导出的游标分批：分批回调覆盖全部命中记录、
// 时间倒序（id 降序）、复用过滤；batch 小于总数时分多批不丢不重。
func TestAuditStreamCursor(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedDetail(t, r, "alice", "k", base.Add(time.Duration(i)*time.Minute))
	}
	seedDetail(t, r, "bob", "k", base.Add(time.Hour)) // 过滤外，验 Stream 也复用过滤

	var got []model.AuditLog
	if err := r.Stream(AuditFilter{Operator: "alice"}, 2, func(batch []model.AuditLog) error {
		got = append(got, batch...)
		return nil
	}); err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("Stream 应覆盖 alice 全部 5 条，实际 %d", len(got))
	}
	// id 降序（时间倒序）：相邻 id 严格递减，证明分批拼接顺序正确、不丢不重
	for i := 1; i < len(got); i++ {
		if got[i].ID >= got[i-1].ID {
			t.Fatalf("Stream 结果应按 id 降序，第 %d 项 id=%d 不小于前项 %d", i, got[i].ID, got[i-1].ID)
		}
	}
	for _, a := range got {
		if a.Operator != "alice" {
			t.Fatalf("Stream 结果含非 alice 记录：%s", a.Operator)
		}
	}
}

// TestAuditListFilterByOperator 验证 FR-30 新增的「按操作者」过滤维度。
func TestAuditListFilterByOperator(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seed(t, r, "alice", model.ActionConfigCreate, base)
	seed(t, r, "bob", model.ActionConfigPublish, base.Add(time.Minute))
	seed(t, r, "alice", model.ActionConfigPublish, base.Add(2*time.Minute))

	// 仅按 operator=alice 过滤 → 2 条（Page/Size 由调用方/服务层规整，单测直连仓库须显式给定）
	items, total, err := r.List(AuditFilter{Operator: "alice", Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("operator=alice 应 2 条，实际 total=%d len=%d", total, len(items))
	}
	for _, it := range items {
		if it.Operator != "alice" {
			t.Fatalf("结果含非 alice 操作者: %s", it.Operator)
		}
	}

	// operator 与 action 叠加过滤 → alice 的 publish 仅 1 条
	_, both, err := r.List(AuditFilter{Operator: "alice", Action: model.ActionConfigPublish})
	if err != nil {
		t.Fatalf("叠加查询失败: %v", err)
	}
	if both != 1 {
		t.Fatalf("operator=alice & action=publish 应 1 条，实际 %d", both)
	}

	// 空 operator 不过滤 → 全量 3 条
	_, all, _ := r.List(AuditFilter{})
	if all != 3 {
		t.Fatalf("空过滤应返回全量 3 条，实际 %d", all)
	}
}

// seedResult 追加一条指定 result 的审计（用于成功/失败筛选测试）。
func seedResult(t *testing.T, r *AuditLogRepository, operator, result string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: result, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestAuditListFilterByResult 验证「按成功/失败筛选」：result=ok / result=fail 各自只回对应记录，
// 空值不过滤，且能与既有过滤维度（operator）AND 叠加。
func TestAuditListFilterByResult(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedResult(t, r, "alice", model.ResultOK, base)
	seedResult(t, r, "bob", model.ResultFail, base.Add(time.Minute))
	seedResult(t, r, "alice", model.ResultOK, base.Add(2*time.Minute))
	seedResult(t, r, "bob", model.ResultFail, base.Add(3*time.Minute))

	// result=ok → 2 条且全为 ok
	items, total, err := r.List(AuditFilter{Result: model.ResultOK, Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("result=ok 应 2 条，实际 total=%d len=%d", total, len(items))
	}
	for _, it := range items {
		if it.Result != model.ResultOK {
			t.Fatalf("result=ok 结果集含非 ok 记录：%q", it.Result)
		}
	}

	// result=fail → 2 条且全为 fail
	fails, failTotal, err := r.List(AuditFilter{Result: model.ResultFail, Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if failTotal != 2 || len(fails) != 2 {
		t.Fatalf("result=fail 应 2 条，实际 total=%d len=%d", failTotal, len(fails))
	}
	for _, it := range fails {
		if it.Result != model.ResultFail {
			t.Fatalf("result=fail 结果集含非 fail 记录：%q", it.Result)
		}
	}

	// 与 operator 叠加：alice 的两条均为 ok → alice & fail 应 0 条
	_, both, err := r.List(AuditFilter{Operator: "alice", Result: model.ResultFail, Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("叠加查询失败: %v", err)
	}
	if both != 0 {
		t.Fatalf("operator=alice & result=fail 应 0 条，实际 %d", both)
	}

	// 空 result 不过滤 → 全量 4 条
	_, all, err := r.List(AuditFilter{Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if all != 4 {
		t.Fatalf("空 result 应返回全量 4 条，实际 %d", all)
	}
}

// TestAuditStreamFilterByResult 验证导出流（Stream）与列表同口径复用 result 过滤：
// 分批拉取结果只含目标结果集，且不因分批丢失记录。
func TestAuditStreamFilterByResult(t *testing.T) {
	r := NewAuditLogRepository(newAuditTestDB(t))
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedResult(t, r, "alice", model.ResultOK, base.Add(time.Duration(i)*time.Minute))
	}
	for i := 0; i < 2; i++ {
		seedResult(t, r, "alice", model.ResultFail, base.Add(time.Duration(i+10)*time.Minute))
	}

	var got []model.AuditLog
	if err := r.Stream(AuditFilter{Result: model.ResultFail}, 1, func(batch []model.AuditLog) error {
		got = append(got, batch...)
		return nil
	}); err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Stream result=fail 应覆盖 2 条，实际 %d", len(got))
	}
	for _, it := range got {
		if it.Result != model.ResultFail {
			t.Fatalf("Stream result=fail 结果含非 fail 记录：%q", it.Result)
		}
	}
}

// TestAuditListColdFilterByResult 验证冷查询（热 + 归档并表）同样复用 result 过滤：
// 两侧各造 ok/fail，result=fail 只回失败记录，不把另一侧的结果混入。
func TestAuditListColdFilterByResult(t *testing.T) {
	hotDB, arcDB := openRepoSQLite(t, "audit_cold_result_hot"), openRepoSQLite(t, "audit_cold_result_arc")
	for _, db := range []*gorm.DB{hotDB, arcDB} {
		if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
			t.Fatalf("迁移 audit_log 失败: %v", err)
		}
	}
	repo := NewAuditLogRepository(hotDB)
	repo.SetArchiveDB(arcDB)
	arc := NewAuditLogRepository(arcDB)

	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	// 冷查询并表按主键 id 去重保热侧，跨库造数须给互不相同的主键（真实环境 id 全局自增）
	seedResultByID(t, repo, 1, model.ResultOK, base)
	seedResultByID(t, repo, 2, model.ResultFail, base.Add(time.Minute))
	seedResultByID(t, arc, 101, model.ResultOK, base.Add(-time.Minute))
	seedResultByID(t, arc, 102, model.ResultFail, base.Add(-2*time.Minute))

	items, _, err := repo.ListCold(AuditFilter{Result: model.ResultFail}, "", 10)
	if err != nil {
		t.Fatalf("冷查询失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("冷查询 result=fail 应 2 条（热 1 + 冷 1），实际 %d", len(items))
	}
	for _, it := range items {
		if it.Result != model.ResultFail {
			t.Fatalf("冷查询 result=fail 结果含非 fail 记录：%q", it.Result)
		}
	}
}

// seedResultByID 造一条指定主键与结果的审计（冷查询跨库造数用，避免两侧主键撞车被去重）。
func seedResultByID(t *testing.T, r *AuditLogRepository, id uint, result string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AuditLog{
		ID: id, NamespaceCode: "prod", Operator: "alice", Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Result: result, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

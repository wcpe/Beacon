package service

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newIgnoreRuleTestDB 打开内存 sqlite 并迁移忽略规则 + 审计表。
func newIgnoreRuleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:ignorerule?mode=memory&cache=shared"), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ReverseFetchIgnoreRule{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"reverse_fetch_ignore_rule", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

func newIgnoreRuleSvc(db *gorm.DB) *ReverseFetchIgnoreRuleService {
	return NewReverseFetchIgnoreRuleService(db, repository.NewReverseFetchIgnoreRuleRepository(db),
		repository.NewAuditLogRepository(db))
}

func countAuditAction(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("计数审计失败: %v", err)
	}
	return n
}

// TestIgnoreRuleCRUD 建 / 列 / 删忽略规则 + 审计；重复同标识活跃规则 → 冲突；删后可再建。
func TestIgnoreRuleCRUD(t *testing.T) {
	db := newIgnoreRuleTestDB(t)
	svc := newIgnoreRuleSvc(db)

	rule, err := svc.Create(CreateRuleParams{
		Namespace: "prod", Scope: model.ScopeGroup, Group: "area1",
		RuleType: model.IgnoreRuleExact, Pattern: "ServerProbe/metrics.jsonl", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建规则应成功: %v", err)
	}
	if rule.ID == 0 || rule.Pattern != "ServerProbe/metrics.jsonl" {
		t.Fatalf("规则落库异常: %+v", rule)
	}
	if countAuditAction(t, db, model.ActionReverseFetchIgnoreRuleAdd) != 1 {
		t.Fatal("应记一条 ignore-rule-add 审计")
	}

	// 列出
	rules, _ := svc.List("prod", model.ScopeGroup, "area1", "")
	if len(rules) != 1 {
		t.Fatalf("应列出 1 条规则，实际 %d", len(rules))
	}

	// 同标识活跃规则再建 → 冲突
	if _, err := svc.Create(CreateRuleParams{
		Namespace: "prod", Scope: model.ScopeGroup, Group: "area1",
		RuleType: model.IgnoreRuleExact, Pattern: "ServerProbe/metrics.jsonl", Operator: "bob",
	}); err != apperr.ErrConfigConflict {
		t.Fatalf("同标识活跃规则再建应 CONFLICT，实际 %v", err)
	}

	// 删除 + 审计
	if err := svc.Delete(rule.ID, "alice", ""); err != nil {
		t.Fatalf("删规则应成功: %v", err)
	}
	if countAuditAction(t, db, model.ActionReverseFetchIgnoreRuleRemove) != 1 {
		t.Fatal("应记一条 ignore-rule-remove 审计")
	}
	if rules, _ := svc.List("prod", "", "", ""); len(rules) != 0 {
		t.Fatalf("删后应无活跃规则，实际 %d", len(rules))
	}
	// 删后同标识可再建（软删哨兵释放唯一键占位）
	if _, err := svc.Create(CreateRuleParams{
		Namespace: "prod", Scope: model.ScopeGroup, Group: "area1",
		RuleType: model.IgnoreRuleExact, Pattern: "ServerProbe/metrics.jsonl", Operator: "alice",
	}); err != nil {
		t.Fatalf("删后同标识应可再建: %v", err)
	}
	// 删不存在规则 → 404
	if err := svc.Delete(99999, "alice", ""); err != apperr.ErrConfigNotFound {
		t.Fatalf("删不存在规则应 NOT_FOUND，实际 %v", err)
	}
}

// TestIgnoreRuleValidation 非法入参拒：非法 scope / 缺 group / server 缺 target / 非法 ruleType / 空 pattern。
func TestIgnoreRuleValidation(t *testing.T) {
	db := newIgnoreRuleTestDB(t)
	svc := newIgnoreRuleSvc(db)
	cases := []CreateRuleParams{
		{Namespace: "prod", Scope: "global", Group: "area1", RuleType: model.IgnoreRuleExact, Pattern: "a", Operator: "x"},
		{Namespace: "prod", Scope: model.ScopeGroup, Group: "", RuleType: model.IgnoreRuleExact, Pattern: "a", Operator: "x"},
		{Namespace: "prod", Scope: model.ScopeServer, Group: "area1", ScopeTarget: "", RuleType: model.IgnoreRuleExact, Pattern: "a", Operator: "x"},
		{Namespace: "prod", Scope: model.ScopeGroup, Group: "area1", RuleType: "regex", Pattern: "a", Operator: "x"},
		{Namespace: "prod", Scope: model.ScopeGroup, Group: "area1", RuleType: model.IgnoreRuleExact, Pattern: "", Operator: "x"},
	}
	for i, c := range cases {
		if _, err := svc.Create(c); err == nil {
			t.Fatalf("用例 %d 非法入参应拒，实际通过", i)
		}
	}
}

// TestIgnoredByRulesMatching exact / prefix 匹配纯函数；prefix 模式归一为目录前缀。
func TestIgnoredByRulesMatching(t *testing.T) {
	rules := []model.ReverseFetchIgnoreRule{
		{RuleType: model.IgnoreRuleExact, Pattern: "AllinCore/data.db"},
		{RuleType: model.IgnoreRulePrefix, Pattern: "ServerProbe/"},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"AllinCore/data.db", true},         // exact 命中
		{"AllinCore/config.yml", false},     // exact 不命中
		{"ServerProbe/metrics.jsonl", true}, // prefix 命中
		{"ServerProbe/sub/deep.log", true},  // prefix 深层命中
		{"ServerProbeX/other.yml", false},   // prefix 边界：非目录前缀不误命中（归一补 /）
		{"Other/x.yml", false},              // 都不命中
	}
	for _, c := range cases {
		if got := IgnoredByRules(c.path, rules); got != c.want {
			t.Fatalf("IgnoredByRules(%q) = %v, 期望 %v", c.path, got, c.want)
		}
	}
}

// TestMatchActiveLayering server 作用域任务叠加 group 层 + 本实例 server 层规则；group 作用域仅 group 层。
func TestMatchActiveLayering(t *testing.T) {
	db := newIgnoreRuleTestDB(t)
	svc := newIgnoreRuleSvc(db)
	// 大区层规则
	if _, err := svc.Create(CreateRuleParams{
		Namespace: "prod", Scope: model.ScopeGroup, Group: "area1",
		RuleType: model.IgnoreRulePrefix, Pattern: "ServerProbe/", Operator: "x",
	}); err != nil {
		t.Fatalf("建大区层规则失败: %v", err)
	}
	// 仅作用 lobby-1 的实例层规则
	if _, err := svc.Create(CreateRuleParams{
		Namespace: "prod", Scope: model.ScopeServer, Group: "area1", ScopeTarget: "lobby-1",
		RuleType: model.IgnoreRuleExact, Pattern: "local/only.yml", Operator: "x",
	}); err != nil {
		t.Fatalf("建实例层规则失败: %v", err)
	}

	// group 作用域任务：仅见大区层规则
	groupRules, _ := svc.MatchActive("prod", model.ScopeGroup, "area1", "")
	if len(groupRules) != 1 || groupRules[0].Scope != model.ScopeGroup {
		t.Fatalf("group 作用域应仅见 1 条大区层规则，实际 %+v", groupRules)
	}
	// server 作用域任务（lobby-1）：叠加大区层 + 本实例层
	serverRules, _ := svc.MatchActive("prod", model.ScopeServer, "area1", "lobby-1")
	if len(serverRules) != 2 {
		t.Fatalf("server 作用域应叠加 2 条规则，实际 %d", len(serverRules))
	}
	// server 作用域任务（lobby-2）：仅大区层（无 lobby-2 的实例层规则）
	otherRules, _ := svc.MatchActive("prod", model.ScopeServer, "area1", "lobby-2")
	if len(otherRules) != 1 {
		t.Fatalf("lobby-2 应仅见大区层规则，实际 %d", len(otherRules))
	}
}

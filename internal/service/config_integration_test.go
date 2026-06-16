package service_test

import (
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/service"
	"beacon/internal/testsupport"
)

// testDB 连接 service 包独立测试库并清表；未设 BEACON_TEST_DSN 则跳过。
func testDB(t *testing.T) *gorm.DB {
	return testsupport.OpenTestDB(t, "service")
}

// newStack 装配配置与有效配置服务。
func newStack(t *testing.T) (*service.ConfigService, *service.EffectiveService, *gorm.DB) {
	db := testDB(t)
	cr := repository.NewConfigItemRepository(db)
	rr := repository.NewConfigRevisionRepository(db)
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	return service.NewConfigService(db, cr, rr, ar), service.NewEffectiveService(cr, asg), db
}

// TestConfigLifecycle 集成验证：建→发布→历史→回滚→diff→软删。
func TestConfigLifecycle(t *testing.T) {
	cfg, _, db := newStack(t)

	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "mysql.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: "pool: 10\n", Operator: "alice", Comment: "首次",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	if item.Version != 1 || item.ContentMD5 != merge.MD5Hex("pool: 10\n") {
		t.Fatalf("首发版本/ md5 错误：version=%d md5=%s", item.Version, item.ContentMD5)
	}

	// 发布新版本
	pub, err := cfg.Publish(item.ID, "pool: 20\n", "bob", "调大连接池")
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if pub.Version != 2 {
		t.Fatalf("发布后版本应为 2，实际 %d", pub.Version)
	}

	// 历史
	revs, err := cfg.ListRevisions(item.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("历史应有 2 版，实际 %d，err=%v", len(revs), err)
	}

	// 回滚到 v1
	rb, err := cfg.Rollback(item.ID, 1, "carol", "回退")
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if rb.Version != 3 || rb.Content != "pool: 10\n" {
		t.Fatalf("回滚后版本应为 3 且内容回到 v1，实际 version=%d content=%q", rb.Version, rb.Content)
	}
	// 回滚记录 source_revision
	rev3, err := cfg.GetRevision(item.ID, 3)
	if err != nil || rev3.SourceRevision == nil {
		t.Fatalf("回滚版本应记录 source_revision，err=%v", err)
	}

	// diff v1 vs v2
	from, to, err := cfg.Diff(item.ID, 1, 2)
	if err != nil || from != "pool: 10\n" || to != "pool: 20\n" {
		t.Fatalf("diff 错误：from=%q to=%q err=%v", from, to, err)
	}

	// 审计应有 建/发布/回滚 至少 3 条
	var auditCount int64
	db.Model(&model.AuditLog{}).Count(&auditCount)
	if auditCount < 3 {
		t.Fatalf("审计记录应 >=3，实际 %d", auditCount)
	}

	// 软删后取不到
	if err := cfg.Delete(item.ID, "dave", "下线"); err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	if _, err := cfg.Get(item.ID); !errors.Is(err, apperr.ErrConfigNotFound) {
		t.Fatalf("软删后应返回 CONFIG_NOT_FOUND，实际 %v", err)
	}
}

// TestConfigValidation 集成验证：各类发布前校验。
func TestConfigValidation(t *testing.T) {
	cfg, _, _ := newStack(t)
	base := service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "a.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML, Content: "x: 1\n", Operator: "alice",
	}

	// 坏 yaml
	bad := base
	bad.DataID, bad.Content = "bad.yml", "a: 1\n  b: 2\n bad"
	if _, err := cfg.Create(bad); !errors.Is(err, apperr.ErrContentInvalid) {
		t.Errorf("坏内容应返回 CONTENT_INVALID，实际 %v", err)
	}

	// 超长
	big := base
	big.DataID, big.Content = "big.yml", "k: "+string(make([]byte, service.MaxContentBytes))+"\n"
	if _, err := cfg.Create(big); !errors.Is(err, apperr.ErrContentTooLarge) {
		t.Errorf("超长应返回 CONTENT_TOO_LARGE，实际 %v", err)
	}

	// 重复标识
	if _, err := cfg.Create(base); err != nil {
		t.Fatalf("首建应成功: %v", err)
	}
	if _, err := cfg.Create(base); !errors.Is(err, apperr.ErrConfigConflict) {
		t.Errorf("重复标识应返回 CONFIG_CONFLICT，实际 %v", err)
	}

	// 跨层格式不一致：a.yml 已是 yaml，再以 json 在 group 层建
	inc := service.CreateConfigParams{
		Namespace: "prod", Group: "area1", DataID: "a.yml",
		ScopeLevel: model.ScopeGroup, Format: merge.FormatJSON, Content: "{}", Operator: "alice",
	}
	if _, err := cfg.Create(inc); !errors.Is(err, apperr.ErrFormatInconsistent) {
		t.Errorf("跨层格式不一致应返回 FORMAT_INCONSISTENT，实际 %v", err)
	}

	// zone 层缺 scopeTarget
	noTarget := service.CreateConfigParams{
		Namespace: "prod", Group: "area1", DataID: "z.yml",
		ScopeLevel: model.ScopeZone, Format: merge.FormatYAML, Content: "x: 1\n", Operator: "alice",
	}
	if _, err := cfg.Create(noTarget); !errors.Is(err, apperr.ErrInvalidScope) {
		t.Errorf("zone 层缺 target 应返回 INVALID_SCOPE，实际 %v", err)
	}
}

// TestEffectiveFourLayer 集成验证：四层覆盖链合并 + 整体 md5 稳定。
func TestEffectiveFourLayer(t *testing.T) {
	cfg, eff, db := newStack(t)

	// 指派 lobby-1 → area1/zoneA
	if err := db.Create(&model.ZoneAssignment{
		NamespaceCode: "prod", ServerID: "lobby-1", GroupCode: "area1", ZoneCode: "zoneA",
	}).Error; err != nil {
		t.Fatalf("建指派失败: %v", err)
	}

	create := func(group, scope, target, content string) {
		if _, err := cfg.Create(service.CreateConfigParams{
			Namespace: "prod", Group: group, DataID: "mysql.yml",
			ScopeLevel: scope, ScopeTarget: target, Format: merge.FormatYAML,
			Content: content, Operator: "alice",
		}); err != nil {
			t.Fatalf("建 %s 层失败: %v", scope, err)
		}
	}
	create(model.GlobalGroupCode, model.ScopeGlobal, "", "host: g\npool: 1\nnest:\n  a: 1\n  b: 2\n")
	create("area1", model.ScopeGroup, "", "pool: 2\n")
	create("area1", model.ScopeZone, "zoneA", "nest:\n  b: 20\n")
	create("area1", model.ScopeServer, "lobby-1", "extra: \"yes\"\n")

	res, err := eff.Resolve("prod", "lobby-1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Group != "area1" || res.Zone != "zoneA" || len(res.Items) != 1 {
		t.Fatalf("解析结果元数据错误：group=%s zone=%s items=%d", res.Group, res.Zone, len(res.Items))
	}
	parsed, _ := merge.Parse(merge.FormatYAML, res.Items[0].Content)
	want := map[string]any{
		"host":  "g",
		"pool":  2,
		"nest":  map[string]any{"a": 1, "b": 20},
		"extra": "yes",
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("四层合并结果错误：\ngot=%v\nwant=%v", parsed, want)
	}

	// 整体 md5 稳定（再解析一次相同）
	res2, _ := eff.Resolve("prod", "lobby-1")
	if res.MD5 != res2.MD5 {
		t.Fatalf("整体 md5 不稳定：%s vs %s", res.MD5, res2.MD5)
	}

	// 未指派的 server 只拿到 global 层
	resUnassigned, err := eff.Resolve("prod", "ghost-9")
	if err != nil {
		t.Fatalf("解析未指派 server 失败: %v", err)
	}
	pg, _ := merge.Parse(merge.FormatYAML, resUnassigned.Items[0].Content)
	wantGlobal := map[string]any{"host": "g", "pool": 1, "nest": map[string]any{"a": 1, "b": 2}}
	if !reflect.DeepEqual(pg, wantGlobal) {
		t.Fatalf("未指派 server 应只含 global 层：got=%v", pg)
	}
}

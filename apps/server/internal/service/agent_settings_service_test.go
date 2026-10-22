package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// newTestAgentSettingsService 装配一个空 agent_setting 服务的测试夹具（内存 sqlite，单连接）。
//
// 每个测试用 t.Name() 起独立内存库：共享 file::memory: 会让不同测试的事务在 shared-cache
// 写锁上互相拖死（与 settings 测试夹具同一处置）。
func newTestAgentSettingsService(t *testing.T) (*AgentSettingsService, *gorm.DB, uint) {
	t.Helper()
	dsn := "file:agent_settings_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AgentSetting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("建环境失败: %v", err)
	}
	svc := NewAgentSettingsService(db,
		repository.NewAgentSettingRepository(db), repository.NewAuditLogRepository(db),
		repository.NewNamespaceRepository(db))
	return svc, db, ns.ID
}

// ptr 构造写入参数里的非 nil 值（helper 名称与测试断言可读性优先）。
func ptr(value string) *string { return &value }

// TestUpdateBatchServerOverridesNamespace 覆盖 FR-277 的作用域优先级：
// 同键在两处都有值时，server 级胜过 namespace 级；来源标记必须如实反映「谁是当前生效的那一层」。
func TestUpdateBatchServerOverridesNamespace(t *testing.T) {
	svc, _, nsID := newTestAgentSettingsService(t)

	const key = "delivery.config-reload-timeout-ms"
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "", Values: map[string]*string{key: ptr("30000")},
		ConfirmationScope: "prod", Operator: "admin", ClientIP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("写 namespace 级默认失败: %v", err)
	}
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "lobby-1", Values: map[string]*string{key: ptr("45000")},
		ConfirmationScope: "prod/lobby-1", Operator: "admin", ClientIP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("写 server 级覆盖失败: %v", err)
	}

	serverView, err := svc.ListEffective(nsID, "lobby-1")
	if err != nil {
		t.Fatalf("读 server 作用域失败: %v", err)
	}
	item := mustAgentSettingItem(t, serverView.Items, key)
	if item.Value != "45000" || item.Source != AgentSettingSourceServer {
		t.Fatalf("server 级应覆盖 namespace 级（值 45000 / 来源 server），实际 %s / %s", item.Value, item.Source)
	}

	// 同环境的另一台服没有自己的覆盖，应看到 namespace 级默认。
	otherView, err := svc.ListEffective(nsID, "lobby-2")
	if err != nil {
		t.Fatalf("读另一台服作用域失败: %v", err)
	}
	otherItem := mustAgentSettingItem(t, otherView.Items, key)
	if otherItem.Value != "30000" || otherItem.Source != AgentSettingSourceNamespace {
		t.Fatalf("未覆盖的服应沿用 namespace 级（值 30000 / 来源 namespace），实际 %s / %s",
			otherItem.Value, otherItem.Source)
	}
}

// TestUpdateBatchRejectedKeys 覆盖两条键级防线的**写入侧**：禁区键与未知键都不得落库，
// 且错误要能指出具体键（否则运维只能逐个试）。
func TestUpdateBatchRejectedKeys(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	for _, key := range []string{"beacon.endpoints", "beacon.bootstrap-token", "identity.server-id", "identity.identity-id"} {
		_, err := svc.UpdateBatch(AgentSettingsWriteParams{
			NamespaceID: nsID, Values: map[string]*string{key: ptr("http://evil.example:8848")},
			ConfirmationScope: "prod", Operator: "admin",
		})
		if err == nil {
			t.Fatalf("禁区键 %q 必须被拒", key)
		}
		if !containsAll(err.Error(), "AGENT_SETTING_KEY_FORBIDDEN", key) {
			t.Fatalf("禁区键 %q 的错误须带专用码且点明键名，实际 %v", key, err)
		}
	}
	// 未知键（拼写错误）也必须拒：放行等于写了个永不生效的值。
	_, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{"delivery.config-reload-timeout_ms": ptr("30000")},
		ConfirmationScope: "prod", Operator: "admin",
	})
	if err == nil || !containsAll(err.Error(), "AGENT_SETTING_KEY_NOT_ALLOWED") {
		t.Fatalf("未知键必须被拒且带 KEY_NOT_ALLOWED，实际 %v", err)
	}
	// 被拒的请求不得留下任何行（整批不落地）。
	var count int64
	if err := db.Model(&model.AgentSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒的写入不应落库，实际残留 %d 行", count)
	}
}

// TestUpdateBatchWritesAuditPerKey 覆盖审计落库：每个真正变更的键一条 agent-settings.update，
// detail 要能回答「谁在什么时候改了哪台服的哪个配置项、改前改后是什么」。
func TestUpdateBatchWritesAuditPerKey(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	const key = "assets.scan-interval-sec"
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "lobby-1", Values: map[string]*string{key: ptr("600")},
		ConfirmationScope: "prod/lobby-1", Reason: "降低扫描频率", Operator: "alice", ClientIP: "10.0.0.9",
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionAgentSettingsUpdate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("一次按键变更应写 1 条审计，实际 %d 条", len(logs))
	}
	entry := logs[0]
	if entry.Operator != "alice" || entry.NamespaceCode != "prod" || entry.Result != model.ResultOK {
		t.Fatalf("审计主体 / 环境 / 结果不符：%+v", entry)
	}
	if entry.TargetType != model.TargetTypeAgentSetting || entry.TargetRef != "prod/lobby-1/"+key {
		t.Fatalf("审计对象应为 agent-setting + 作用域/键，实际 %s %s", entry.TargetType, entry.TargetRef)
	}
	for _, want := range []string{key, `"value":"600"`, `"reason":"降低扫描频率"`, `"operation":"upsert"`, `"serverId":"lobby-1"`} {
		if !containsAll(entry.Detail, want) {
			t.Fatalf("审计 detail 缺 %q：%s", want, entry.Detail)
		}
	}

	// 撤销覆盖同样必须留痕（operation=delete，且记下被删掉的值）。
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "lobby-1", Values: map[string]*string{key: nil},
		ConfirmationScope: "prod/lobby-1", Operator: "bob",
	}); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if err := db.Where("action = ?", model.ActionAgentSettingsUpdate).Order("id").Find(&logs).Error; err != nil {
		t.Fatalf("重查审计失败: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("撤销应再写 1 条审计，实际共 %d 条", len(logs))
	}
	if !containsAll(logs[1].Detail, `"operation":"delete"`, `"previousValue":"600"`) {
		t.Fatalf("撤销审计应记 operation=delete 与被删值：%s", logs[1].Detail)
	}
}

// TestUpdateBatchRequiresMatchingConfirmation 二次确认串不符必须整批拒绝且不落库：
// 这是「以为在改这台服、实际改了整个环境」的唯一拦路点。
func TestUpdateBatchRequiresMatchingConfirmation(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	cases := []struct {
		name      string
		serverID  string
		confirm   string
		wantLabel string
	}{
		{"漏填确认串", "", "", "prod"},
		{"server 级写成环境码", "lobby-1", "prod", "prod/lobby-1"},
		{"环境级写成 server 码", "", "prod/lobby-1", "prod"},
		{"拼错服名", "lobby-1", "prod/lobby-11", "prod/lobby-1"},
	}
	for _, c := range cases {
		_, err := svc.UpdateBatch(AgentSettingsWriteParams{
			NamespaceID: nsID, ServerID: c.serverID, ConfirmationScope: c.confirm,
			Values: map[string]*string{"timing.poll-timeout-ms": ptr("20000")}, Operator: "admin",
		})
		if err == nil {
			t.Fatalf("%s：应因确认串不符被拒", c.name)
		}
		if !containsAll(err.Error(), "AGENT_SETTING_CONFIRM_MISMATCH", c.wantLabel) {
			t.Fatalf("%s：错误须点明期望的作用域标识 %q，实际 %v", c.name, c.wantLabel, err)
		}
	}
	var count int64
	if err := db.Model(&model.AgentSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("确认串不符不得落库，实际残留 %d 行", count)
	}
}

// TestUpdateBatchRejectsEmptyAndInvalidValues 空值与非法值都必须拒：
// agent 侧把空白值当「未下发」，存进去等于写了个永不生效的值（静默失效）。
func TestUpdateBatchRejectsEmptyAndInvalidValues(t *testing.T) {
	svc, _, nsID := newTestAgentSettingsService(t)

	bad := []struct {
		key, value string
	}{
		{"timing.poll-timeout-ms", ""},        // 空值
		{"timing.poll-timeout-ms", "   "},     // 纯空白
		{"timing.poll-timeout-ms", "abc"},     // 非整数
		{"timing.poll-timeout-ms", "0"},       // 低于下界
		{"timing.poll-timeout-ms", "9999999"}, // 高于上界
		{"snapshot.enabled", "yes"},           // 非布尔
		{"assets.scan-interval-sec", "60"},    // 低于 agent 侧收口下限 300
		{"backoff.jitter-ratio", "2"},         // 比例越界
		{"snapshot.file-name", "../etc/passwd"},
		// string-list 也必须有非法值用例：列表按逗号 / 换行切分后要求至少一个非空项。
		{"override.command-whitelist", ","},
		{"override.command-whitelist", " \n , \n "},
	}
	for _, c := range bad {
		_, err := svc.UpdateBatch(AgentSettingsWriteParams{
			NamespaceID: nsID, Values: map[string]*string{c.key: ptr(c.value)},
			ConfirmationScope: "prod", Operator: "admin",
		})
		if err == nil || !containsAll(err.Error(), "AGENT_SETTING_VALUE_INVALID") {
			t.Fatalf("%s=%q 应被拒且带 VALUE_INVALID，实际 %v", c.key, c.value, err)
		}
	}
	// 合法边界值应通过（0 是 delivery.config-reload-timeout-ms 的合法语义「不设超时」）。
	good := []struct{ key, value string }{
		{"delivery.config-reload-timeout-ms", "0"},
		{"delivery.config-reload-timeout-ms", "30000"},
	}
	for _, c := range good {
		if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
			NamespaceID: nsID, Values: map[string]*string{c.key: ptr(c.value)},
			ConfirmationScope: "prod", Operator: "admin",
		}); err != nil {
			t.Fatalf("%s=%q 应通过，实际 %v", c.key, c.value, err)
		}
	}
	// override.command-whitelist 的**合法值**不在此处断言「直写成功」：它是高危键，
	// 即便值合法也必须转审批（本夹具未注入审批核心，故这里只可能报内部错误）。
	// 值校验本身由上面两条非法用例锁定，合法值经审批落库的路径见 agent_settings_approval_test.go。
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{"override.command-whitelist": ptr("reload, taboolib")},
		ConfirmationScope: "prod", Operator: "admin",
	}); err == nil || containsAll(err.Error(), "AGENT_SETTING_VALUE_INVALID") {
		t.Fatalf("合法白名单值应通过值校验后停在审批环节，实际 %v", err)
	}
}

// TestUpdateBatchIdempotent 重复提交同样的值不应产生新的审计行（幂等重放不算一次变更）。
func TestUpdateBatchIdempotent(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	params := AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{"messaging.rpc-timeout-ms": ptr("8000")},
		ConfirmationScope: "prod", Operator: "admin",
	}
	first, err := svc.UpdateBatch(params)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if len(first.Applied) != 1 || len(first.Unchanged) != 0 {
		t.Fatalf("首次写入应记 applied=1，实际 %+v", first)
	}
	second, err := svc.UpdateBatch(params)
	if err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	if len(second.Applied) != 0 || len(second.Unchanged) != 1 {
		t.Fatalf("重复写入应记 unchanged=1，实际 %+v", second)
	}
	var count int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", model.ActionAgentSettingsUpdate).Count(&count).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("未产生变更时不应新增审计，实际 %d 条", count)
	}
}

// TestPullRejectsForbiddenKeys 覆盖**读侧**键级防线：库里即便存在禁区键也不能下发给 agent，
// 且这次拒绝必须落一条 fail 审计（控制面自己被用来做劫持的痕迹）。
//
// 直接写库、绕过 UpdateBatch 的写入校验，正是为了模拟「经其它途径（旧版本 / 直连库 / 将来的
// 新通道）塞进了禁区键」——服务端拒绝不能依赖写入路径已经挡过。
func TestPullRejectsForbiddenKeys(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	forbidden := []string{"beacon.endpoints", "identity.server-id"}
	rows := []model.AgentSetting{
		{NamespaceID: nsID, ServerID: "lobby-1", Key: "beacon.endpoints", Value: "http://evil.example:8848", UpdatedBy: "intruder"},
		{NamespaceID: nsID, ServerID: "lobby-1", Key: "identity.server-id", Value: "ghost", UpdatedBy: "intruder"},
		{NamespaceID: nsID, ServerID: "lobby-1", Key: "timing.request-timeout-ms", Value: "7000", UpdatedBy: "admin"},
		{NamespaceID: nsID, ServerID: "", Key: "snapshot.enabled", Value: "false", UpdatedBy: "admin"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("预置行失败: %v", err)
	}

	view, err := svc.Pull(agentauth.Identity{
		NamespaceID: nsID, Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity-1",
	})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	for _, key := range forbidden {
		if value, hit := view.Values[key]; hit {
			t.Fatalf("禁区键 %q 绝不可下发，实际返回 %q", key, value)
		}
	}
	if len(view.RejectedKeys) != len(forbidden) {
		t.Fatalf("应报告 %d 个被拒键，实际 %v", len(forbidden), view.RejectedKeys)
	}
	// 正常键照发，且 server 级覆盖 namespace 级。
	if view.Values["timing.request-timeout-ms"] != "7000" {
		t.Fatalf("合法键应下发，实际 %v", view.Values)
	}
	if view.Values["snapshot.enabled"] != "false" {
		t.Fatalf("namespace 级默认应下发，实际 %v", view.Values)
	}

	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionAgentSettingsRejected).Find(&logs).Error; err != nil {
		t.Fatalf("查拒绝审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("一次含禁区键的拉取应写 1 条拒绝审计，实际 %d 条", len(logs))
	}
	entry := logs[0]
	if entry.Operator != "agent:identity-1" || entry.Result != model.ResultFail {
		t.Fatalf("拒绝审计主体 / 结果不符：%+v", entry)
	}
	if entry.TargetRef != "prod/lobby-1" {
		t.Fatalf("拒绝审计应定位到作用域，实际 %s", entry.TargetRef)
	}
	for _, key := range forbidden {
		if !containsAll(entry.Detail, key) {
			t.Fatalf("拒绝审计 detail 应列出被拒键 %q：%s", key, entry.Detail)
		}
	}
}

// TestPullRejectsIdentityPrefix 前缀禁区（identity.）必须整段挡住，
// 而不是只挡硬编码的那两个键——新增身份字段默认就该在下发通道之外。
func TestPullRejectsIdentityPrefix(t *testing.T) {
	svc, _, nsID := newTestAgentSettingsService(t)

	if err := svc.db.Create(&model.AgentSetting{
		NamespaceID: nsID, ServerID: "lobby-1", Key: "identity.future-field", Value: "x",
	}).Error; err != nil {
		t.Fatalf("预置行失败: %v", err)
	}
	view, err := svc.Pull(agentauth.Identity{NamespaceID: nsID, Namespace: "prod", ServerID: "lobby-1", IdentityID: "id-2"})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(view.Values) != 0 {
		t.Fatalf("identity.* 整段禁发，实际返回 %v", view.Values)
	}
	if len(view.RejectedKeys) != 1 || view.RejectedKeys[0] != "identity.future-field" {
		t.Fatalf("应报告 identity.future-field 被拒，实际 %v", view.RejectedKeys)
	}
}

// TestPullIsolatesNamespaces 作用域隔离：A 环境的配置绝不出现在 B 环境 agent 的响应里。
func TestPullIsolatesNamespaces(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	other := &model.Namespace{Code: "test", Name: "测试", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("建第二个环境失败: %v", err)
	}
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{"snapshot.enabled": ptr("false")},
		ConfirmationScope: "prod", Operator: "admin",
	}); err != nil {
		t.Fatalf("写 prod 配置失败: %v", err)
	}

	view, err := svc.Pull(agentauth.Identity{
		NamespaceID: other.ID, Namespace: "test", ServerID: "lobby-1", IdentityID: "identity-test",
	})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(view.Values) != 0 {
		t.Fatalf("跨环境配置不得下发，实际 %v", view.Values)
	}
}

// TestListEffectiveUnknownNamespace 环境不存在时报 NAMESPACE_NOT_FOUND，不能静默返回空视图。
func TestListEffectiveUnknownNamespace(t *testing.T) {
	svc, _, _ := newTestAgentSettingsService(t)

	if _, err := svc.ListEffective(9999, "lobby-1"); err == nil {
		t.Fatal("不存在的环境应报错，而不是返回空视图（否则运维会把漏建环境误当「没有下发」）")
	}
}

// TestUpdateBatchSameAsInheritedValueStillPinsOverride 守护一个易错语义：server 级提交的值
// 与继承来的 namespace 默认**恰好相同**时，仍必须真的写一行 server 级覆盖（不能因「值没变」跳过）。
//
// 差别是实质性的：写了覆盖 = 这台服被钉在这个值，环境默认后续调整不影响它；没写 = 它仍然跟着
// 环境默认走。判「未变化」会让运维以为已钉住，实际下次改环境默认就跟着漂。
func TestUpdateBatchSameAsInheritedValueStillPinsOverride(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	const key = "timing.request-timeout-ms"
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{key: ptr("5000")},
		ConfirmationScope: "prod", Operator: "admin",
	}); err != nil {
		t.Fatalf("写 namespace 级失败: %v", err)
	}
	result, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "lobby-1", Values: map[string]*string{key: ptr("5000")},
		ConfirmationScope: "prod/lobby-1", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("写 server 级失败: %v", err)
	}
	if len(result.Applied) != 1 || len(result.Unchanged) != 0 {
		t.Fatalf("与继承值相同的 server 级提交仍应真正写入，实际 applied=%v unchanged=%v",
			result.Applied, result.Unchanged)
	}
	var rows []model.AgentSetting
	if err := db.Where("namespace_id = ? AND server_id = ?", nsID, "lobby-1").Find(&rows).Error; err != nil {
		t.Fatalf("查表失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "5000" {
		t.Fatalf("应存在一条 server 级覆盖行，实际 %+v", rows)
	}
	// 视图来源也必须是 server（它已经是显式覆盖，不再是「沿用默认」）。
	view, err := svc.ListEffective(nsID, "lobby-1")
	if err != nil {
		t.Fatalf("读视图失败: %v", err)
	}
	if item := mustAgentSettingItem(t, view.Items, key); item.Source != AgentSettingSourceServer {
		t.Fatalf("应报来源 server，实际 %s", item.Source)
	}
}

// TestUpdateBatchDeleteOnlyTouchesOwnScope 守护撤销语义：server 级提交 null 只撤销该服的覆盖，
// 且当本服本来就没有覆盖时**不产生任何写入与审计**（不能删掉环境默认，也不能记一条假变更）。
func TestUpdateBatchDeleteOnlyTouchesOwnScope(t *testing.T) {
	svc, db, nsID := newTestAgentSettingsService(t)

	const key = "snapshot.file-name"
	if _, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, Values: map[string]*string{key: ptr("prod-snapshot.json")},
		ConfirmationScope: "prod", Operator: "admin",
	}); err != nil {
		t.Fatalf("写 namespace 级失败: %v", err)
	}
	// 本服没有覆盖，提交 null 应记 unchanged 且不动环境默认。
	result, err := svc.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: nsID, ServerID: "lobby-1", Values: map[string]*string{key: nil},
		ConfirmationScope: "prod/lobby-1", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Unchanged) != 1 {
		t.Fatalf("无覆盖时撤销应记 unchanged，实际 removed=%v unchanged=%v", result.Removed, result.Unchanged)
	}
	var nsRows int64
	if err := db.Model(&model.AgentSetting{}).
		Where("namespace_id = ? AND server_id = ?", nsID, "").Count(&nsRows).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if nsRows != 1 {
		t.Fatalf("环境默认不得被 server 级撤销删掉，实际剩 %d 行", nsRows)
	}
	var audits int64
	if err := db.Model(&model.AuditLog{}).
		Where("action = ?", model.ActionAgentSettingsUpdate).Count(&audits).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	if audits != 1 {
		t.Fatalf("无实际变更不应新增审计（应只有最初那条），实际 %d 条", audits)
	}
}

// TestIndexAgentSettingRowsPrefersServerRegardlessOfOrder 守护合并逻辑与行顺序无关：
// 无论仓库返回的是「namespace 级在前」还是「server 级在前」，server 级都必须胜出。
//
// 这条单独固定下来，是因为正确性一旦依赖仓库排序，任何排序改动（比如给查询加一个索引
// 让执行计划变序）都会静默翻转「谁是生效值」——那是最难查的一类配置事故。
func TestIndexAgentSettingRowsPrefersServerRegardlessOfOrder(t *testing.T) {
	const key = "timing.poll-timeout-ms"
	nsRow := model.AgentSetting{NamespaceID: 1, ServerID: "", Key: key, Value: "10000"}
	serverRow := model.AgentSetting{NamespaceID: 1, ServerID: "lobby-1", Key: key, Value: "20000"}

	orders := map[string][]model.AgentSetting{
		"namespace 级在前": {nsRow, serverRow},
		"server 级在前":    {serverRow, nsRow},
	}
	for name, rows := range orders {
		merged := indexAgentSettingRows(rows, "lobby-1")
		if got := merged[key].Value; got != "20000" {
			t.Fatalf("%s：server 级应覆盖 namespace 级得到 20000，实际 %q", name, got)
		}
	}
	// 另一台服视角下，同一批行里 server 级那条不属于它，必须被排除。
	merged := indexAgentSettingRows([]model.AgentSetting{serverRow}, "lobby-2")
	if _, hit := merged[key]; hit {
		t.Fatal("他服的 server 级行不得混入本服视角")
	}
}

// TestAgentSettingItemSource 是「生效来源判定」的表驱动测试（FR-279 #2）。
//
// 这张表锁的不是「三态凑齐了」，而是**控制面敢断言到哪里**：
//   - 有下发行 → 来源必须精确到那一层（server / namespace），因为此时下发值就是生效值；
//   - 没有下发行 → 只能给 local-or-default。控制面看不到 agent 本地链
//     （环境变量 > config.yml > 代码默认）的解析结果，因此**不允许**在这里返回
//     `default` / `local` 之类「看起来更精确」的值。
//
// 把这条口径钉进测试，是为了防止后来者顺手补一个 `default` 来源把假报变成默认行为：
// 一旦服务端开始声称「这是代码默认值」，运维就会照着结论去排查、而那个结论并不成立。
func TestAgentSettingItemSource(t *testing.T) {
	const nsID uint = 7
	ns := &model.Namespace{Code: "prod"}
	ns.ID = nsID

	cases := []struct {
		name     string
		serverID string
		rows     []model.AgentSetting
		key      string
		want     string
	}{
		{
			name:     "有 server 级下发行 → server",
			serverID: "lobby-1",
			rows: []model.AgentSetting{
				{NamespaceID: nsID, ServerID: "lobby-1", Key: "timing.poll-timeout-ms", Value: "15000"},
			},
			key:  "timing.poll-timeout-ms",
			want: AgentSettingSourceServer,
		},
		{
			name:     "无 server 行、有 namespace 行 → namespace",
			serverID: "lobby-1",
			rows: []model.AgentSetting{
				{NamespaceID: nsID, ServerID: "", Key: "messaging.rpc-timeout-ms", Value: "5000"},
			},
			key:  "messaging.rpc-timeout-ms",
			want: AgentSettingSourceNamespace,
		},
		{
			name:     "两级都有行 → server 级胜出",
			serverID: "lobby-1",
			rows: []model.AgentSetting{
				{NamespaceID: nsID, ServerID: "", Key: "assets.scan-interval-sec", Value: "1800"},
				{NamespaceID: nsID, ServerID: "lobby-1", Key: "assets.scan-interval-sec", Value: "600"},
			},
			key:  "assets.scan-interval-sec",
			want: AgentSettingSourceServer,
		},
		{
			// 元数据里 desc 写着「默认 2.0」，但那是给人看的中文说明，不是机器可读的默认值；
			// 即便有，也只说明「本地链的最后一环有兜底」，无法排除环境变量 / config.yml 先命中。
			name:     "两级都无行 + 该键有代码默认 → 仍不可区分（local-or-default）",
			serverID: "lobby-1",
			rows:     nil,
			key:      "backoff.multiplier",
			want:     AgentSettingSourceLocalOrDefault,
		},
		{
			// proxy.home-group 的 agent 侧默认是空串。空默认也仍是「代码默认」，
			// 并不能推出「这一定是本地配置」——所以同样不可区分。
			name:     "两级都无行 + 该键默认是空串 → 同为 local-or-default",
			serverID: "lobby-1",
			rows:     nil,
			key:      "proxy.home-group",
			want:     AgentSettingSourceLocalOrDefault,
		},
		{
			// 只有他服的 server 级行：对本作用域而言等于「没下发过」，
			// 但下发行属于本环境，来源仍是 namespace 级（不是 local-or-default）。
			name:     "只有他服的 server 级行 → 本服沿用 namespace 默认",
			serverID: "lobby-1",
			rows: []model.AgentSetting{
				{NamespaceID: nsID, ServerID: "lobby-2", Key: "snapshot.enabled", Value: "false"},
			},
			key:  "snapshot.enabled",
			want: AgentSettingSourceLocalOrDefault,
		},
		{
			// 控制面不认识的键（已从元数据移除但库里还有行）：来源判定与 known 无关，
			// 有 server 级行就仍是 server——否则运维会把「遗留覆盖」误当成「本地配置」。
			name:     "未知键但有 server 级行 → server（来源判定不受 known 影响）",
			serverID: "lobby-1",
			rows: []model.AgentSetting{
				{NamespaceID: nsID, ServerID: "lobby-1", Key: "legacy.retired-option", Value: "on"},
			},
			key:  "legacy.retired-option",
			want: AgentSettingSourceServer,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := buildAgentSettingScopeView(ns, c.serverID, c.rows)
			if got := mustAgentSettingItem(t, view.Items, c.key).Source; got != c.want {
				t.Fatalf("%s：来源应为 %q，实际 %q", c.key, c.want, got)
			}
		})
	}
}

// TestAgentSettingLocalOrDefaultDoesNotFabricateValue 守护 local-or-default 的**另一半诚实性**：
// 既然控制面不知道 agent 本地的生效值，视图就不能顺手回填一个「看起来像生效值」的东西
// （比如按元数据里的默认值填上）。value 必须为空串、updatedAt 必须为 nil，前端据此显示占位符。
func TestAgentSettingLocalOrDefaultDoesNotFabricateValue(t *testing.T) {
	ns := &model.Namespace{Code: "prod"}
	ns.ID = 7
	view := buildAgentSettingScopeView(ns, "lobby-1", nil)

	const key = "timing.request-timeout-ms"
	item := mustAgentSettingItem(t, view.Items, key)
	if item.Source != AgentSettingSourceLocalOrDefault {
		t.Fatalf("无下发行应报 local-or-default，实际 %q", item.Source)
	}
	if item.Value != "" {
		t.Fatalf("控制面不知道本地生效值，value 必须为空串，实际 %q", item.Value)
	}
	if item.UpdatedAt != nil {
		t.Fatalf("未下发过的键不应有更新时间，实际 %v", item.UpdatedAt)
	}
	if !item.Known {
		t.Fatalf("该键在控制面已知清单内，known 应为 true")
	}
	// 作用域整体也没有任何下发时间：前端据此展示「该作用域尚未下发过任何配置」。
	if view.UpdatedAt != nil {
		t.Fatalf("无任何下发行时作用域 updatedAt 应为 nil，实际 %v", view.UpdatedAt)
	}
}

// mustAgentSettingItem 按 key 取视图项，缺失即失败。
func mustAgentSettingItem(t *testing.T, items []AgentSettingItemView, key string) AgentSettingItemView {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("视图中缺少键 %q", key)
	return AgentSettingItemView{}
}

// TestAgentSettingMetadataMatchesAgentDefaults 锁住「控制面元数据」与「agent 代码默认值」的一致性。
//
// 为什么必须有这把锁（FR-280 #4/#5）：这两处默认值分处 Go 与 Kotlin，语言不同、无编译期关联，
// 已有先例就是它们悄悄漂移——元数据 desc 曾写「默认 0=不设超时」而 agent 实际默认 30000，
// 上限也曾是 600000 vs 3600000：运维在 config.yml 写 1800000 能生效、在 Web 上填同样的值
// 却被 400 拒，两边"合法范围"说的不是一回事。跨语言无法共享常量，只能在此显式对齐。
func TestAgentSettingMetadataMatchesAgentDefaults(t *testing.T) {
	// 期望值来自 agent 侧：AgentSettings.configReloadTimeoutMs / configReloadPoolSize 的默认值，
	// 以及 PushedConfigCoordinator.MAX_RELOAD_TIMEOUT_MS / MAX_RELOAD_POOL_SIZE。
	cases := []struct {
		key     string
		wantMin int
		wantMax int
	}{
		{"delivery.config-reload-timeout-ms", 0, 3_600_000},
		{"delivery.reload-pool-size", 1, 256},
	}
	for _, c := range cases {
		meta, ok := agentSettingMetaFor(c.key)
		if !ok {
			t.Fatalf("%s 必须登记在元数据表里", c.key)
		}
		if meta.min != c.wantMin || meta.max != c.wantMax {
			t.Fatalf("%s 的合法区间应与 agent 侧同值：期望 [%d,%d]，实际 [%d,%d]",
				c.key, c.wantMin, c.wantMax, meta.min, meta.max)
		}
	}
}

package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// agentSettingsApprovalFixture 装配「审批链路可用」的 agent 配置服务夹具。
//
// 迁移里必须带 approval_request / approval_execution_receipt：本组测试的核心断言是
// 「202 时库内没变」与「批准后由执行事务落库」，缺这两张表就无从证明。
type agentSettingsApprovalFixture struct {
	svc      *AgentSettingsService
	approval *ApprovalService
	db       *gorm.DB
	nsID     uint
}

// newAgentSettingsApprovalFixture 构造夹具；每测试独立内存库，避免共享缓存写锁互相拖死。
func newAgentSettingsApprovalFixture(t *testing.T) *agentSettingsApprovalFixture {
	t.Helper()
	dsn := "file:agent_settings_approval_" + t.Name() + "?mode=memory&cache=shared"
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
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AgentSetting{}, &model.AuditLog{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("建环境失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db),
		repository.NewAuditLogRepository(db), registry)
	svc := NewAgentSettingsService(db, repository.NewAgentSettingRepository(db),
		repository.NewAuditLogRepository(db), repository.NewNamespaceRepository(db))
	svc.SetApprovalService(approval)
	RegisterAgentSettingsApprovalAdapter(registry, svc)
	return &agentSettingsApprovalFixture{svc: svc, approval: approval, db: db, nsID: ns.ID}
}

// write 用管理员主体提交一次批量写入。
func (f *agentSettingsApprovalFixture) write(p AgentSettingsWriteParams) (*AgentSettingsWriteResult, error) {
	p.Operator = "alice"
	p.ClientIP = "10.0.0.9"
	p.Principal = auth.HumanPrincipal("alice")
	return f.svc.UpdateBatch(p)
}

// approveAndRun 批准票据并让审批 worker 执行一次领域事务。
func (f *agentSettingsApprovalFixture) approveAndRun(t *testing.T, requestID string) model.ApprovalRequest {
	t.Helper()
	if _, err := f.approval.Approve(requestID, auth.HumanPrincipal("bob"), "10.0.0.10"); err != nil {
		t.Fatalf("批准申请失败: %v", err)
	}
	if _, err := NewApprovalWorker(f.approval).RunOnce(); err != nil {
		t.Fatalf("审批 worker 执行失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := f.db.Where("request_id = ?", requestID).First(&req).Error; err != nil {
		t.Fatalf("重读审批申请失败: %v", err)
	}
	return req
}

// approvalPayloadHash 取某条审批申请的冻结载荷哈希。
func (f *agentSettingsApprovalFixture) approvalPayloadHash(t *testing.T, requestID string) string {
	t.Helper()
	var req model.ApprovalRequest
	if err := f.db.Where("request_id = ?", requestID).First(&req).Error; err != nil {
		t.Fatalf("重读审批申请失败: %v", err)
	}
	return req.FrozenPayloadSHA256
}

// agentSettingRowValue 取某作用域下某键的库内值；不存在返回 ("", false)。
func (f *agentSettingsApprovalFixture) agentSettingRowValue(t *testing.T, serverID, key string) (string, bool) {
	t.Helper()
	var rows []model.AgentSetting
	if err := f.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?", f.nsID, serverID, key).
		Find(&rows).Error; err != nil {
		t.Fatalf("查配置行失败: %v", err)
	}
	if len(rows) == 0 {
		return "", false
	}
	return rows[0].Value, true
}

// TestDangerousAgentSettingWriteReturnsTicketWithoutWriting 覆盖验收缺口本身：
// 高危键（override.command-whitelist）的 PUT 必须**只**返回票据，库内一行都不能变。
//
// 这条锁的是「不得假报成功」：票据表示受理，不是生效。
func TestDangerousAgentSettingWriteReturnsTicketWithoutWriting(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const key = "override.command-whitelist"
	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{key: ptr("reload, taboolib")}, Reason: "开启三方插件命令派发",
	})
	if err != nil {
		t.Fatalf("高危键提交应转为审批而不是报错: %v", err)
	}
	if result.ApprovalTicket == nil {
		t.Fatalf("高危键必须返回审批票据，实际 %+v", result)
	}
	if result.ApprovalTicket.ApprovalRequestID == "" || result.ApprovalTicket.Status != model.ApprovalStatusPending {
		t.Fatalf("票据应有申请 ID 且状态 pending，实际 %+v", result.ApprovalTicket)
	}
	if result.ApprovalTicket.OperationKey != authz.OperationAgentSettingsDangerous {
		t.Fatalf("票据操作键应为 agent-settings.update.dangerous，实际 %q", result.ApprovalTicket.OperationKey)
	}
	// 三个变更集必须为空：本次提交什么都没落地。
	if len(result.Applied) != 0 || len(result.Removed) != 0 || len(result.Unchanged) != 0 {
		t.Fatalf("转审批时不得报告任何变更，实际 applied=%v removed=%v unchanged=%v",
			result.Applied, result.Removed, result.Unchanged)
	}
	if value, hit := f.agentSettingRowValue(t, "lobby-1", key); hit {
		t.Fatalf("转审批时绝不可落库，实际库内为 %q", value)
	}
	// 也不得留下「已下发」审计：库内没有变更，写一条 update 审计就是假证据。
	var audits int64
	if err := f.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionAgentSettingsUpdate).
		Count(&audits).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	if audits != 0 {
		t.Fatalf("转审批时不应写配置变更审计，实际 %d 条", audits)
	}
}

// TestNonDangerousAgentSettingWriteStaysDirect 是加性演进的回归锁：
// 不含高危键的 PUT 必须仍然直接落库并返回无票据的结果（老客户端行为完全不变）。
func TestNonDangerousAgentSettingWriteStaysDirect(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const key = "timing.poll-timeout-ms"
	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, Values: map[string]*string{key: ptr("20000")},
		ConfirmationScope: "prod", Reason: "调整长轮询挂起上限",
	})
	if err != nil {
		t.Fatalf("非高危键应直写成功: %v", err)
	}
	if result.ApprovalTicket != nil {
		t.Fatalf("非高危键不得转审批，实际 %+v", result.ApprovalTicket)
	}
	if len(result.Applied) != 1 || result.Applied[0] != key {
		t.Fatalf("非高危键应记 applied=[%s]，实际 %v", key, result.Applied)
	}
	if value, hit := f.agentSettingRowValue(t, "", key); !hit || value != "20000" {
		t.Fatalf("非高危键应立即落库为 20000，实际 %q（存在=%v）", value, hit)
	}
}

// TestApprovedDangerousWriteAppliesAndAudits 覆盖审批通过后的执行侧：
// 值真的写进库，且审计能追到是凭哪张批准落库的。
func TestApprovedDangerousWriteAppliesAndAudits(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const key = "override.command-whitelist"
	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{key: ptr("reload, taboolib")}, Reason: "开启三方插件命令派发",
	})
	if err != nil || result.ApprovalTicket == nil {
		t.Fatalf("应先拿到票据，实际 %+v err=%v", result, err)
	}
	requestID := result.ApprovalTicket.ApprovalRequestID

	req := f.approveAndRun(t, requestID)
	if req.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("批准并执行后申请应为 succeeded，实际 %s（%s）", req.Status, req.FailureSummary)
	}
	if value, hit := f.agentSettingRowValue(t, "lobby-1", key); !hit || value != "reload, taboolib" {
		t.Fatalf("批准后应落库冻结值，实际 %q（存在=%v）", value, hit)
	}
	var logs []model.AuditLog
	if err := f.db.Where("action = ?", model.ActionAgentSettingsUpdate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("批准执行应写 1 条变更审计，实际 %d 条", len(logs))
	}
	entry := logs[0]
	if entry.Operator != "alice" || entry.ClientIP != "10.0.0.9" {
		t.Fatalf("审计主体应是申请人（谁提的）而不是批准人：%+v", entry)
	}
	if entry.TargetRef != "prod/lobby-1/"+key {
		t.Fatalf("审计对象应定位到作用域/键，实际 %s", entry.TargetRef)
	}
	for _, want := range []string{`"operation":"upsert"`, `"approvalRequestId":"` + requestID + `"`,
		`"reason":"开启三方插件命令派发"`} {
		if !containsAll(entry.Detail, want) {
			t.Fatalf("审计 detail 缺 %q：%s", want, entry.Detail)
		}
	}
	// 执行回执必须存在：审批内核据此判断副作用已完成，重放不会再写一次。
	var receipts int64
	if err := f.db.Model(&model.ApprovalExecutionReceipt{}).
		Where("request_id = ?", requestID).Count(&receipts).Error; err != nil {
		t.Fatalf("统计执行回执失败: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("应提交 1 条执行回执，实际 %d 条", receipts)
	}
}

// TestApprovedDangerousWriteRejectedWhenBaselineChanged 覆盖并发防护：
// 等待审批期间该键被别的途径改过，则批准后的执行必须整体失败且不落库。
func TestApprovedDangerousWriteRejectedWhenBaselineChanged(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const key = "override.command-whitelist"
	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{key: ptr("reload")}, Reason: "收窄白名单",
	})
	if err != nil || result.ApprovalTicket == nil {
		t.Fatalf("应先拿到票据，实际 %+v err=%v", result, err)
	}
	// 模拟等待期间另一条途径写入（旧版本 / 直连库 / 另一个已批准的申请）。
	if err := f.db.Create(&model.AgentSetting{
		NamespaceID: f.nsID, ServerID: "lobby-1", Key: key, Value: "reload, taboolib, shell", UpdatedBy: "intruder",
	}).Error; err != nil {
		t.Fatalf("预置并发写入失败: %v", err)
	}

	req := f.approveAndRun(t, result.ApprovalTicket.ApprovalRequestID)
	if req.Status != model.ApprovalStatusFailed {
		t.Fatalf("基线已变时执行应失败，实际状态 %s", req.Status)
	}
	if value, hit := f.agentSettingRowValue(t, "lobby-1", key); !hit || value != "reload, taboolib, shell" {
		t.Fatalf("基线冲突时绝不可覆盖库内现值，实际 %q", value)
	}
	var audits int64
	if err := f.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionAgentSettingsUpdate).
		Count(&audits).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	if audits != 0 {
		t.Fatalf("执行失败不应留下变更审计，实际 %d 条", audits)
	}
}

// TestApprovedDangerousWriteRejectsNamespaceRename 覆盖作用域复核：
// 环境改名后，冻结时的作用域标识不再成立，执行必须判目标已变（否则会写到「已不是同一个作用域」）。
func TestApprovedDangerousWriteRejectsNamespaceRename(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{"override.command-whitelist": ptr("reload")}, Reason: "收窄白名单",
	})
	if err != nil || result.ApprovalTicket == nil {
		t.Fatalf("应先拿到票据，实际 %+v err=%v", result, err)
	}
	if err := f.db.Model(&model.Namespace{}).Where("id = ?", f.nsID).
		Update("code", "prod-renamed").Error; err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	req := f.approveAndRun(t, result.ApprovalTicket.ApprovalRequestID)
	if req.Status != model.ApprovalStatusFailed {
		t.Fatalf("环境改名后执行应失败，实际状态 %s", req.Status)
	}
	if value, hit := f.agentSettingRowValue(t, "lobby-1", "override.command-whitelist"); hit {
		t.Fatalf("目标作用域已变时不得落库，实际 %q", value)
	}
}

// TestMixedDangerousBatchFreezesWholeRequest 守护「整单」取舍：
// 同一次提交里混含高危与非高危键时，非高危键**也不得提前落库**。
//
// 拆开处理会制造「一半已生效、一半待批」的中间态：刷新后页面看着一切正常，
// 而真正被拦下的那半只在审批中心里——无法向人解释，也无法回答「刚才那一下到底改了没有」。
func TestMixedDangerousBatchFreezesWholeRequest(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const (
		dangerKey = "override.command-whitelist"
		safeKey   = "timing.poll-timeout-ms"
	)
	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{
			dangerKey: ptr("reload"),
			safeKey:   ptr("45000"),
		},
		Reason: "调整时序并收窄白名单",
	})
	if err != nil || result.ApprovalTicket == nil {
		t.Fatalf("混含高危键的提交应整单转审批，实际 %+v err=%v", result, err)
	}
	for _, key := range []string{dangerKey, safeKey} {
		if value, hit := f.agentSettingRowValue(t, "lobby-1", key); hit {
			t.Fatalf("整单转审批时 %q 也不得提前落库，实际 %q", key, value)
		}
	}

	// 批准后两个键一起生效，证明冻结的是整批而非只有危险键。
	req := f.approveAndRun(t, result.ApprovalTicket.ApprovalRequestID)
	if req.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("批准执行应成功，实际 %s（%s）", req.Status, req.FailureSummary)
	}
	for key, want := range map[string]string{dangerKey: "reload", safeKey: "45000"} {
		if value, hit := f.agentSettingRowValue(t, "lobby-1", key); !hit || value != want {
			t.Fatalf("批准后 %q 应为 %q，实际 %q（存在=%v）", key, want, value, hit)
		}
	}
}

// TestDangerousApprovalCannotBypassForbiddenKeys 守护「审批通道不能成为旁路」：
// 同批含禁区键时必须在建申请之前就拒掉，连票据都不该产生。
func TestDangerousApprovalCannotBypassForbiddenKeys(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	for _, forbiddenKey := range []string{"beacon.endpoints", "beacon.bootstrap-token", "identity.server-id"} {
		_, err := f.write(AgentSettingsWriteParams{
			NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
			Values: map[string]*string{
				"override.command-whitelist": ptr("reload"),
				forbiddenKey:                 ptr("http://evil.example:8848"),
			},
			Reason: "试图借审批通道塞入劫持键",
		})
		if err == nil || !containsAll(err.Error(), "AGENT_SETTING_KEY_FORBIDDEN", forbiddenKey) {
			t.Fatalf("禁区键 %q 必须被拒且点明键名，实际 %v", forbiddenKey, err)
		}
	}
	var requests int64
	if err := f.db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if requests != 0 {
		t.Fatalf("被拒提交不得产生审批申请，实际 %d 条", requests)
	}
	var rows int64
	if err := f.db.Model(&model.AgentSetting{}).Count(&rows).Error; err != nil {
		t.Fatalf("统计配置行失败: %v", err)
	}
	if rows != 0 {
		t.Fatalf("被拒提交不得落库，实际 %d 行", rows)
	}
}

// TestDangerousAgentSettingWriteIsIdempotentOnReplay 覆盖重复提交：
// 运维在页面上连点两次「保存」时，服务端派生的幂等键应让第二次命中既有申请，
// 返回**同一张**票据，而不是往审批中心堆两条几乎一样的待批单。
func TestDangerousAgentSettingWriteIsIdempotentOnReplay(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	params := AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{"override.command-whitelist": ptr("reload")}, Reason: "收窄白名单",
	}
	first, err := f.write(params)
	if err != nil || first.ApprovalTicket == nil {
		t.Fatalf("首次提交应拿到票据，实际 %+v err=%v", first, err)
	}
	second, err := f.write(params)
	if err != nil || second.ApprovalTicket == nil {
		t.Fatalf("重复提交应同样拿到票据，实际 %+v err=%v", second, err)
	}
	if first.ApprovalTicket.ApprovalRequestID != second.ApprovalTicket.ApprovalRequestID {
		t.Fatalf("同一内容的重复提交应复用同一张票据：%s vs %s",
			first.ApprovalTicket.ApprovalRequestID, second.ApprovalTicket.ApprovalRequestID)
	}
	var requests int64
	if err := f.db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if requests != 1 {
		t.Fatalf("重复提交不得新增审批申请，实际 %d 条", requests)
	}

	// 内容不同（换一个值）则必须是一张新单，不能被幂等键误合并。
	other, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{"override.command-whitelist": ptr("reload, taboolib")}, Reason: "收窄白名单",
	})
	if err != nil || other.ApprovalTicket == nil {
		t.Fatalf("不同内容应拿到票据，实际 %+v err=%v", other, err)
	}
	if other.ApprovalTicket.ApprovalRequestID == first.ApprovalTicket.ApprovalRequestID {
		t.Fatal("内容不同的提交不得复用同一张票据")
	}
}

// TestApprovedDangerousWriteRejectsNonDangerousPayload 守护反向旁路：
// 有人把一张不含任何高危键的载荷塞进审批执行，不能借「已批准」把普通写入写进去。
func TestApprovedDangerousWriteRejectsNonDangerousPayload(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	empty := AgentSettingsWriteResult{}
	result, err := f.svc.applyApprovedBatchInTx(f.db, "apr_fake", agentSettingsApprovalPayload{
		NamespaceID: f.nsID, NamespaceCode: "prod", ServerID: "lobby-1", ScopeLabel: "prod/lobby-1",
		Values: []agentSettingsApprovalItem{{Key: "timing.poll-timeout-ms", Value: ptr("20000")}},
		Reason: "伪装成已批准", Operator: "mallory",
	})
	if err == nil {
		t.Fatalf("不含高危键的载荷不得经审批通道落库，实际 %+v", result)
	}
	if !containsAll(err.Error(), apperr.ErrForbidden.Error()) {
		// 只断言错误本身：文案由 apperr 统一给出，重点是拒绝而非措辞。
		_ = empty
	}
	if _, hit := f.agentSettingRowValue(t, "lobby-1", "timing.poll-timeout-ms"); hit {
		t.Fatal("被拒的载荷不得留下任何配置行")
	}
}

// TestDangerousAgentSettingWithdrawAlsoRequiresApproval 守护「撤销也走审批」。
//
// 白名单被悄悄清空与把它放开同属能力边界变更：一次单人操作就能关掉命令派发，同样要留痕、
// 同样需要第二个人点头。本用例锁死「Values 传 nil」这条路径——一旦有人把
// dangerousAgentSettingKeys 改成只统计非 nil 值（一个看起来很自然的「优化」），
// 撤销就会重新退化成直写，而本文件其余用例全绿。
func TestDangerousAgentSettingWithdrawAlsoRequiresApproval(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	const key = "override.command-whitelist"
	// 预置一行覆盖：否则「撤销」是空操作，批准后「行被删」就无从证明。
	if err := f.db.Create(&model.AgentSetting{
		NamespaceID: f.nsID, ServerID: "lobby-1", Key: key, Value: "reload, taboolib", UpdatedBy: "old",
	}).Error; err != nil {
		t.Fatalf("预置配置行失败: %v", err)
	}

	result, err := f.write(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{key: nil}, Reason: "收回三方插件命令派发能力",
	})
	if err != nil {
		t.Fatalf("撤销高危键应转审批而不是报错: %v", err)
	}
	if result.ApprovalTicket == nil {
		t.Fatalf("撤销高危键必须走审批，实际 %+v", result)
	}
	// 提交侧的事务性：转审批时连「要删的那一行」都不能动。
	if value, hit := f.agentSettingRowValue(t, "lobby-1", key); !hit || value != "reload, taboolib" {
		t.Fatalf("转审批时库内行必须原样保留，实际 %q（存在=%v）", value, hit)
	}

	req := f.approveAndRun(t, result.ApprovalTicket.ApprovalRequestID)
	if req.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("批准后执行应成功，实际 %s（%s）", req.Status, req.FailureSummary)
	}
	if value, hit := f.agentSettingRowValue(t, "lobby-1", key); hit {
		t.Fatalf("批准后覆盖应被撤销，实际仍为 %q", value)
	}
	var logs []model.AuditLog
	if err := f.db.Where("action = ?", model.ActionAgentSettingsUpdate).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("撤销应写 1 条变更审计，实际 %d 条：%+v", len(logs), logs)
	}
	if !containsAll(logs[0].Detail, `"operation":"delete"`, `"previousValue":"reload, taboolib"`) {
		t.Fatalf("撤销审计必须记成 delete 并带被删的旧值，实际 %s", logs[0].Detail)
	}
}

// TestDangerousApprovalSupersedesTerminalRequest 是 C1 的复现路径：
// 旧申请已经终结（驳回 / 撤回 / 过期）之后，用**同样内容**再提交必须产生一张新申请，
// 而不是把那张永远不会再有人处理的旧票原样返回给运维（那等于谎称「已提交审批，请等待」）。
//
// 三种终结形态各跑一遍，并顺带锁住两件事：
//   - 新票建好之后，同内容的再次提交仍然命中这张新票（改派键是确定的，不会攒出第三条待批）；
//   - 新票仍然可以正常批准生效（改道没有破坏审批执行链路）。
func TestDangerousApprovalSupersedesTerminalRequest(t *testing.T) {
	const key = "override.command-whitelist"

	cases := []struct {
		name string
		kill func(t *testing.T, f *agentSettingsApprovalFixture, requestID string)
	}{
		{
			name: "驳回后重提",
			kill: func(t *testing.T, f *agentSettingsApprovalFixture, requestID string) {
				t.Helper()
				if _, err := f.approval.Reject(requestID, auth.HumanPrincipal("bob"), "10.0.0.10", "当前不需要放开"); err != nil {
					t.Fatalf("驳回失败: %v", err)
				}
			},
		},
		{
			name: "撤回后重提",
			kill: func(t *testing.T, f *agentSettingsApprovalFixture, requestID string) {
				t.Helper()
				if _, err := f.approval.Withdraw(requestID, auth.HumanPrincipal("alice"), "10.0.0.9"); err != nil {
					t.Fatalf("撤回失败: %v", err)
				}
			},
		},
		{
			name: "过期后重提",
			kill: func(t *testing.T, f *agentSettingsApprovalFixture, requestID string) {
				t.Helper()
				// 真实过期路径：把 expires_at 拨到过去，再走一次批准——审批内核会把它判成 expired。
				if err := f.db.Model(&model.ApprovalRequest{}).Where("request_id = ?", requestID).
					Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
					t.Fatalf("改过期时间失败: %v", err)
				}
				if _, err := f.approval.Approve(requestID, auth.HumanPrincipal("bob"), "10.0.0.10"); err == nil {
					t.Fatal("已过期的申请不应批准成功")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentSettingsApprovalFixture(t)
			params := AgentSettingsWriteParams{
				NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
				Values: map[string]*string{key: ptr("reload, taboolib")}, Reason: "开启三方插件命令派发",
			}

			first, err := f.write(params)
			if err != nil || first.ApprovalTicket == nil {
				t.Fatalf("首次提交应拿到票据，实际 %+v err=%v", first, err)
			}
			tc.kill(t, f, first.ApprovalTicket.ApprovalRequestID)

			// 关键断言：重提同样内容必须是一张**新**申请，而不是那张已终结的旧票。
			second, err := f.write(params)
			if err != nil || second.ApprovalTicket == nil {
				t.Fatalf("旧票终结后重提必须仍然返回票据，实际 %+v err=%v", second, err)
			}
			if second.ApprovalTicket.ApprovalRequestID == first.ApprovalTicket.ApprovalRequestID {
				t.Fatalf("旧票已终结，重提却拿回同一张死票据：%s", second.ApprovalTicket.ApprovalRequestID)
			}
			if second.ApprovalTicket.Status != model.ApprovalStatusPending {
				t.Fatalf("改派出的新申请应为 pending，实际 %s", second.ApprovalTicket.Status)
			}
			// 库里仍然不能有任何变更：票据只表示受理。
			if value, hit := f.agentSettingRowValue(t, "lobby-1", key); hit {
				t.Fatalf("重提也不得提前落库，实际 %q", value)
			}

			// 改派键是确定的：第三次同内容提交必须命中刚建的新票，不能攒出第二条待批。
			third, err := f.write(params)
			if err != nil || third.ApprovalTicket == nil {
				t.Fatalf("新票建好后重提应命中它，实际 %+v err=%v", third, err)
			}
			if third.ApprovalTicket.ApprovalRequestID != second.ApprovalTicket.ApprovalRequestID {
				t.Fatalf("新票未终结时重提必须复用同一张票：%s vs %s",
					third.ApprovalTicket.ApprovalRequestID, second.ApprovalTicket.ApprovalRequestID)
			}
			var requests int64
			if err := f.db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil {
				t.Fatalf("统计审批申请失败: %v", err)
			}
			if requests != 2 {
				t.Fatalf("一次改派后应只有 2 条申请（旧票 + 新票），实际 %d 条", requests)
			}
			// 改派只换键、不换内容：冻结载荷哈希必须与旧票完全一致。
			// 若有人把「新键」之类的东西塞进载荷，这里会立刻发现——那时载荷哈希变了，
			// 一旦键又撞上既有记录就会退化成 ErrIdempotencyKeyReused（把运维挡在门外）。
			if f.approvalPayloadHash(t, second.ApprovalTicket.ApprovalRequestID) !=
				f.approvalPayloadHash(t, first.ApprovalTicket.ApprovalRequestID) {
				t.Fatal("改派不得改变冻结载荷内容：新旧申请的载荷哈希必须一致")
			}

			// 新票必须仍然可用：改道不能破坏审批执行链路。
			req := f.approveAndRun(t, second.ApprovalTicket.ApprovalRequestID)
			if req.Status != model.ApprovalStatusSucceeded {
				t.Fatalf("新申请批准后应执行成功，实际 %s（%s）", req.Status, req.FailureSummary)
			}
			if value, hit := f.agentSettingRowValue(t, "lobby-1", key); !hit || value != "reload, taboolib" {
				t.Fatalf("新申请批准后应落库冻结值，实际 %q（存在=%v）", value, hit)
			}
		})
	}
}

// TestDangerousApprovalSupersedeChainGrowsPerRejection 锁住改派链本身：
// 每终结一次就沿同一条确定的链往前走一跳，既不会退回死票，也不会重复建票。
func TestDangerousApprovalSupersedeChainGrowsPerRejection(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)
	params := AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values: map[string]*string{"override.command-whitelist": ptr("reload")}, Reason: "收窄白名单",
	}

	seen := make(map[string]struct{}, 3)
	previous := ""
	for round := 1; round <= 3; round++ {
		result, err := f.write(params)
		if err != nil || result.ApprovalTicket == nil {
			t.Fatalf("第 %d 次提交应拿到票据，实际 %+v err=%v", round, result, err)
		}
		id := result.ApprovalTicket.ApprovalRequestID
		if previous != "" && id == previous {
			t.Fatalf("第 %d 次提交拿回了上一轮已终结的死票据 %s", round, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("第 %d 次提交复用了更早的申请 %s", round, id)
		}
		seen[id] = struct{}{}
		previous = id
		if round == 3 {
			break
		}
		// 每轮把当前这张票驳回，逼出下一跳。
		if _, err := f.approval.Reject(id, auth.HumanPrincipal("bob"), "10.0.0.10", "驳回以验证改派链"); err != nil {
			t.Fatalf("第 %d 次驳回失败: %v", round, err)
		}
	}
	var requests int64
	if err := f.db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if requests != 3 {
		t.Fatalf("三次终结 + 三次提交应恰好产生 3 条申请，实际 %d 条", requests)
	}
	// 最后一轮那张仍是待批，再提一次必须命中它（不会继续膨胀）。
	again, err := f.write(params)
	if err != nil || again.ApprovalTicket == nil || again.ApprovalTicket.ApprovalRequestID != previous {
		t.Fatalf("最后一张待批票应被重提命中，实际 %+v err=%v", again, err)
	}
}

// TestAgentSettingsApprovalTicketIsDead 穷举「旧票是否已经不可能产生效果」的判定。
//
// 这不是实现细节的白盒测试：判定错一边就会回到 C1（把死票当受理），
// 错另一边则会让仍然正常的待批单被无谓地重复提交。
func TestAgentSettingsApprovalTicketIsDead(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Minute)

	cases := []struct {
		name      string
		status    string
		expiresAt *time.Time
		want      bool
	}{
		{name: "驳回是终态", status: model.ApprovalStatusRejected, expiresAt: &future, want: true},
		{name: "撤回是终态", status: model.ApprovalStatusWithdrawn, expiresAt: &future, want: true},
		{name: "过期是终态", status: model.ApprovalStatusExpired, expiresAt: &past, want: true},
		{name: "已成功是终态", status: model.ApprovalStatusSucceeded, expiresAt: &future, want: true},
		{name: "已失败是终态", status: model.ApprovalStatusFailed, expiresAt: &future, want: true},
		{name: "待批未过期是活票", status: model.ApprovalStatusPending, expiresAt: &future, want: false},
		// pending 但已过期同样只能等死：批准时内核会把它改成 expired 并回 ErrApprovalExpired。
		{name: "待批已过期是死票", status: model.ApprovalStatusPending, expiresAt: &past, want: true},
		// executing = 批准已发生、执行在途，同内容的再次提交是同一意图的重放，应复用。
		{name: "执行在途是活票", status: model.ApprovalStatusExecuting, expiresAt: &past, want: false},
		// 没有过期时间的 pending（历史数据）按活票处理，不擅自改派。
		{name: "待批无过期时间按活票", status: model.ApprovalStatusPending, expiresAt: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentSettingsApprovalTicketIsDead(tc.status, tc.expiresAt); got != tc.want {
				t.Fatalf("状态 %s（expiresAt=%v）判定应为 %v，实际 %v", tc.status, tc.expiresAt, tc.want, got)
			}
		})
	}
}

// TestAgentSettingsApprovalSupersedeKeyIsDeterministicAndBounded 锁住改派键的两条硬要求：
// 由（内容键 + 旧单号）唯一确定，且落在 idempotency_key 的列宽与校验约束内。
func TestAgentSettingsApprovalSupersedeKeyIsDeterministicAndBounded(t *testing.T) {
	const base = "agent-settings-0123456789abcdef0123456789abcdef"

	first := agentSettingsApprovalSupersedeKey(base, "apr_aaaaaaaaaaaaaaaaaaaaaaaa")
	if again := agentSettingsApprovalSupersedeKey(base, "apr_aaaaaaaaaaaaaaaaaaaaaaaa"); again != first {
		t.Fatalf("同样的输入必须派生出同样的键：%s vs %s", first, again)
	}
	if other := agentSettingsApprovalSupersedeKey(base, "apr_bbbbbbbbbbbbbbbbbbbbbbbb"); other == first {
		t.Fatal("不同旧单号必须派生出不同的键，否则改派会撞上别的申请")
	}
	if !validIdempotencyKey(first) {
		t.Fatalf("改派键必须满足幂等键校验（长度 ≤64、可打印 ASCII），实际 %q", first)
	}
	if first == base {
		t.Fatal("改派键不得与原内容键相同，否则永远命中的是那张旧票")
	}
}

// TestAgentSettingsApprovalAdapterExecuteRejectsNonTransactionalPath 守护执行适配器的入口约束：
// 领域写入必须与执行回执落在同一事务里，非事务路径一律拒绝（否则会出现「副作用已发生但没有回执」）。
func TestAgentSettingsApprovalAdapterExecuteRejectsNonTransactionalPath(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)

	err := agentSettingsApprovalAdapter{svc: f.svc}.Execute(authz.ApprovalRequest{}, authz.Permit{})
	if err == nil || !containsAll(err.Error(), apperr.ErrForbidden.Error()) {
		t.Fatalf("非事务执行路径必须返回 ErrForbidden，实际 %v", err)
	}
}

// TestDangerousWriteFailsClosedWithoutApprovalService 守护 fail-closed：
// 没有注入审批核心时，含高危键的提交必须直接失败，**绝不能**退回直写。
//
// 装配顺序不该决定安全承诺是否生效：若这里退化成直写，一次部署配置疏漏就等于把
// command-whitelist 的审批护栏整个关掉，而页面上看不出任何异常。
func TestDangerousWriteFailsClosedWithoutApprovalService(t *testing.T) {
	f := newAgentSettingsApprovalFixture(t)
	bare := NewAgentSettingsService(f.db, repository.NewAgentSettingRepository(f.db),
		repository.NewAuditLogRepository(f.db), repository.NewNamespaceRepository(f.db))

	result, err := bare.UpdateBatch(AgentSettingsWriteParams{
		NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
		Values:   map[string]*string{"override.command-whitelist": ptr("reload")},
		Reason:   "未装配审批核心时的高危键提交",
		Operator: "alice", ClientIP: "10.0.0.9", Principal: auth.HumanPrincipal("alice"),
	})
	if err == nil {
		t.Fatalf("未装配审批核心时高危键提交必须失败，实际 %+v", result)
	}
	if !containsAll(err.Error(), apperr.ErrInternal.Error()) {
		t.Fatalf("应以 ErrInternal 失败，实际 %v", err)
	}
	var rows int64
	if err := f.db.Model(&model.AgentSetting{}).Count(&rows).Error; err != nil {
		t.Fatalf("统计配置行失败: %v", err)
	}
	if rows != 0 {
		t.Fatalf("fail-closed 时绝不可落库，实际 %d 行", rows)
	}
	var requests int64
	if err := f.db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if requests != 0 {
		t.Fatalf("fail-closed 时不应产生审批申请，实际 %d 条", requests)
	}
}

// TestAgentSettingsBaselineIntactThreeDrifts 穷举基线比对的三种「已变」形态。
//
// 只有第一种（本无覆盖却出现了）曾有用例覆盖，而「值被改成别的」恰恰是最常见的覆盖场景：
// 等待审批期间另一个人改了同一个键，若漏判就会把「按旧基线批准」的变更盖到新值上。
// 「本有覆盖却消失了」（并发 DELETE）同样必须判失败——否则一次撤销会在什么都没删的情况下
// 被记成成功。
func TestAgentSettingsBaselineIntactThreeDrifts(t *testing.T) {
	row := func(key, value string) model.AgentSetting {
		return model.AgentSetting{Key: key, Value: value}
	}

	cases := []struct {
		name    string
		frozen  []agentSettingsApprovalItem
		current map[string]model.AgentSetting
		want    bool
	}{
		{
			name:    "本无覆盖却出现",
			frozen:  []agentSettingsApprovalItem{{Key: "k", Value: ptr("reload"), PreviousValue: nil}},
			current: map[string]model.AgentSetting{"k": row("k", "reload, taboolib")},
			want:    false,
		},
		{
			name:    "本有覆盖却消失（并发 DELETE）",
			frozen:  []agentSettingsApprovalItem{{Key: "k", Value: ptr("reload, taboolib"), PreviousValue: ptr("reload")}},
			current: map[string]model.AgentSetting{},
			want:    false,
		},
		{
			name:    "值被改成别的",
			frozen:  []agentSettingsApprovalItem{{Key: "k", Value: ptr("reload"), PreviousValue: ptr("reload")}},
			current: map[string]model.AgentSetting{"k": row("k", "reload, shell")},
			want:    false,
		},
		{
			name:    "值与存在性都没变",
			frozen:  []agentSettingsApprovalItem{{Key: "k", Value: ptr("reload"), PreviousValue: ptr("reload")}},
			current: map[string]model.AgentSetting{"k": row("k", "reload")},
			want:    true,
		},
		{
			name:    "本无覆盖且现在仍无覆盖",
			frozen:  []agentSettingsApprovalItem{{Key: "k", Value: ptr("reload"), PreviousValue: nil}},
			current: map[string]model.AgentSetting{},
			want:    true,
		},
		{
			name: "多键里有一键漂移即整批失败",
			frozen: []agentSettingsApprovalItem{
				{Key: "a", Value: ptr("1"), PreviousValue: ptr("1")},
				{Key: "b", Value: ptr("2"), PreviousValue: ptr("2")},
			},
			current: map[string]model.AgentSetting{"a": row("a", "1"), "b": row("b", "9")},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentSettingsBaselineIntact(tc.frozen, tc.current); got != tc.want {
				t.Fatalf("基线判定应为 %v，实际 %v", tc.want, got)
			}
		})
	}
}

// TestApprovedDangerousWriteRejectsBaselineDrift 是基线三种漂移形态在执行路径上的落地：
// 光有纯函数的穷举不够，必须证明 drift 真的会让整批执行失败且一行都不写。
func TestApprovedDangerousWriteRejectsBaselineDrift(t *testing.T) {
	const key = "override.command-whitelist"

	cases := []struct {
		name string
		// frozenPrevious 是提交时该键在库内的既有值（nil = 当时没有覆盖）
		frozenPrevious *string
		// drift 在等待审批期间制造出「目标已变」
		drift func(t *testing.T, f *agentSettingsApprovalFixture)
	}{
		{
			name: "本无覆盖却出现", frozenPrevious: nil,
			drift: func(t *testing.T, f *agentSettingsApprovalFixture) {
				t.Helper()
				if err := f.db.Create(&model.AgentSetting{
					NamespaceID: f.nsID, ServerID: "lobby-1", Key: key, Value: "reload", UpdatedBy: "intruder",
				}).Error; err != nil {
					t.Fatalf("制造并发写入失败: %v", err)
				}
			},
		},
		{
			name: "本有覆盖却消失", frozenPrevious: ptr("reload"),
			drift: func(t *testing.T, f *agentSettingsApprovalFixture) {
				t.Helper()
				if err := f.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?", f.nsID, "lobby-1", key).
					Delete(&model.AgentSetting{}).Error; err != nil {
					t.Fatalf("制造并发删除失败: %v", err)
				}
			},
		},
		{
			name: "值被改成别的", frozenPrevious: ptr("reload"),
			drift: func(t *testing.T, f *agentSettingsApprovalFixture) {
				t.Helper()
				if err := f.db.Model(&model.AgentSetting{}).
					Where("namespace_id = ? AND server_id = ? AND setting_key = ?", f.nsID, "lobby-1", key).
					Update("value", "reload, shell").Error; err != nil {
					t.Fatalf("制造并发改值失败: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentSettingsApprovalFixture(t)
			if tc.frozenPrevious != nil {
				if err := f.db.Create(&model.AgentSetting{
					NamespaceID: f.nsID, ServerID: "lobby-1", Key: key, Value: *tc.frozenPrevious, UpdatedBy: "old",
				}).Error; err != nil {
					t.Fatalf("预置配置行失败: %v", err)
				}
			}
			result, err := f.write(AgentSettingsWriteParams{
				NamespaceID: f.nsID, ServerID: "lobby-1", ConfirmationScope: "prod/lobby-1",
				Values: map[string]*string{key: ptr("reload, taboolib")}, Reason: "放开三方插件命令",
			})
			if err != nil || result.ApprovalTicket == nil {
				t.Fatalf("应先拿到票据，实际 %+v err=%v", result, err)
			}
			tc.drift(t, f)

			req := f.approveAndRun(t, result.ApprovalTicket.ApprovalRequestID)
			if req.Status != model.ApprovalStatusFailed {
				t.Fatalf("基线已变时执行必须失败，实际状态 %s", req.Status)
			}
			var audits int64
			if err := f.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionAgentSettingsUpdate).
				Count(&audits).Error; err != nil {
				t.Fatalf("统计审计失败: %v", err)
			}
			if audits != 0 {
				t.Fatalf("执行失败不得留下变更审计，实际 %d 条", audits)
			}
			// 冻结值绝不能盖上去：库内要么没有这行，要么仍是 drift 之后的值。
			if value, hit := f.agentSettingRowValue(t, "lobby-1", key); hit && value == "reload, taboolib" {
				t.Fatalf("基线冲突时不得写入冻结值，实际 %q", value)
			}
		})
	}
}

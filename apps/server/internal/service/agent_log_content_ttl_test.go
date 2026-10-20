package service

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// setLogContentExpiry 直接改库写入正文到期时间，用于构造「已过期 / 未过期」两种前置状态。
func setLogContentExpiry(t *testing.T, db *gorm.DB, commandID uint, expiresAt time.Time) {
	t.Helper()
	if err := db.Model(&model.AgentCommand{}).Where("id = ?", commandID).Update("log_content_expires_at", expiresAt).Error; err != nil {
		t.Fatalf("设置日志正文到期时间失败: %v", err)
	}
}

// TestReceiveLogsWritesExpiry 回传成功必须写入日志正文的独立到期时间。
//
// 缺陷形态（issue #145）：done 是终态，ExpireStale 只收敛 pending/fetched/ready，
// 成功回传的 log_content 因而永无回收路径。修复后回传即写 log_content_expires_at，
// 由清扫器与读路径共同遵守。断言「不为 nil」是本用例的核心：一旦漏写，正文就重新变回永久滞留。
func TestReceiveLogsWritesExpiry(t *testing.T) {
	db := newCommandSvcTestDB(t)
	repo := repository.NewAgentCommandRepository(db)
	logs := NewAgentLogService(db, repo, repository.NewAuditLogRepository(db))
	logs.SetSensitiveAccessGrants(NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))

	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeTailLogs,
		Status: model.CommandStatusFetched, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建 fetched 日志命令失败: %v", err)
	}

	before := time.Now().UTC()
	if err := logs.ReceiveLogs(agentauth.Identity{Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity"},
		cmd.ID, []AgentLogLine{{Level: "info", Text: "一行日志"}}, "127.0.0.1"); err != nil {
		t.Fatalf("回传日志失败: %v", err)
	}

	stored, err := repo.FindByID(cmd.ID)
	if err != nil || stored == nil {
		t.Fatalf("回传后应能取到命令: cmd=%+v err=%v", stored, err)
	}
	if stored.Status != model.CommandStatusDone || stored.LogContent == "" {
		t.Fatalf("回传后应为 done 且有正文，实际 status=%s content=%q", stored.Status, stored.LogContent)
	}
	if stored.LogContentExpiresAt == nil {
		t.Fatal("回传成功必须写入 log_content_expires_at，否则正文永无回收路径")
	}
	// 到期时间应落在「回传时刻 + agentLogContentTTL」附近；用宽松区间避开时钟抖动。
	wantMin := before.Add(agentLogContentTTL - time.Minute)
	wantMax := time.Now().UTC().Add(agentLogContentTTL + time.Minute)
	if stored.LogContentExpiresAt.Before(wantMin) || stored.LogContentExpiresAt.After(wantMax) {
		t.Fatalf("到期时间应约为回传时刻 + %s，实际 %s", agentLogContentTTL, stored.LogContentExpiresAt)
	}
}

// TestReadLogContentRespectsExpiry 读路径必须自己兜住到期：
// 未过期可读正文并记一条读取审计；已过期不返回正文、状态仍如实 done、且不写「成功读取正文」审计。
//
// 为什么读路径要独立判断：清扫器周期与 TTL 同量级，过期正文最坏还能多读一整个周期；
// 清扫器一旦故障就变成永久可读。所以不能只依赖清扫。
func TestReadLogContentRespectsExpiry(t *testing.T) {
	const content = `[{"level":"info","text":"过期前后的正文"}]`

	db := newCommandSvcTestDB(t)
	ensureReadScopeTables(t, db)
	seedReadScopeServer(t, db, "prod", "lobby-live")
	seedReadScopeServer(t, db, "prod", "lobby-expired")
	seedReadScopeServer(t, db, "prod", "lobby-latest")
	repo := repository.NewAgentCommandRepository(db)
	logs := NewAgentLogService(db, repo, repository.NewAuditLogRepository(db))

	seedDone := func(serverID string) *model.AgentCommand {
		t.Helper()
		cmd := &model.AgentCommand{
			NamespaceCode: "prod", ServerID: serverID, Type: model.CommandTypeTailLogs,
			Status: model.CommandStatusDone, LogContent: content, Operator: "alice",
		}
		if err := db.Create(cmd).Error; err != nil {
			t.Fatalf("创建 done 日志命令失败（%s）: %v", serverID, err)
		}
		return cmd
	}

	// ① 未过期：可读正文 + 记一条读取审计。
	live := seedDone("lobby-live")
	setLogContentExpiry(t, db, live.ID, time.Now().UTC().Add(time.Minute))

	got, err := logs.ReadCommand("prod", "lobby-live", live.ID, "alice", "127.0.0.1")
	if err != nil {
		t.Fatalf("未过期正文应可读: %v", err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Text != "过期前后的正文" || got.Status != model.CommandStatusDone {
		t.Fatalf("未过期应返回正文与 done 状态，实际 %+v", got)
	}
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 1 {
		t.Fatalf("读到正文应记一条读取审计，实际 %d 条", n)
	}

	// ② 已过期（ReadCommand）：不返回正文、状态仍 done、不新增审计。
	expired := seedDone("lobby-expired")
	setLogContentExpiry(t, db, expired.ID, time.Now().UTC().Add(-time.Second))

	got, err = logs.ReadCommand("prod", "lobby-expired", expired.ID, "alice", "127.0.0.1")
	if err != nil {
		t.Fatalf("已过期读正文应正常返回空结果而不是报错: %v", err)
	}
	if len(got.Lines) != 0 {
		t.Fatalf("已过期不得返回任何日志行，实际 %+v", got.Lines)
	}
	if got.Status != model.CommandStatusDone {
		t.Fatalf("已过期命令状态仍应如实为 done，实际 %s", got.Status)
	}
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 1 {
		t.Fatalf("已过期读取不得新增成功读取审计（期望仍为 1 条），实际 %d 条", n)
	}

	// ③ 已过期（ReadLatest）：独立的 serverId 保证「最近一条」就是这条，不依赖跨用例的 ID 顺序。
	latest := seedDone("lobby-latest")
	setLogContentExpiry(t, db, latest.ID, time.Now().UTC().Add(-time.Second))

	got, err = logs.ReadLatest("prod", "lobby-latest", "alice", "127.0.0.1")
	if err != nil {
		t.Fatalf("ReadLatest 对已过期正文应正常返回: %v", err)
	}
	if got.CommandID != latest.ID {
		t.Fatalf("ReadLatest 应命中该实例唯一那条命令，实际 commandId=%d 期望 %d", got.CommandID, latest.ID)
	}
	if len(got.Lines) != 0 || got.Status != model.CommandStatusDone {
		t.Fatalf("ReadLatest 应只返回 done 状态、不带正文，实际 %+v", got)
	}
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 1 {
		t.Fatalf("ReadLatest 读取已过期正文不得新增成功审计，实际 %d 条", n)
	}
}

// TestClearExpiredLogContents 清扫只清已到期正文，未到期的必须原样保留。
//
// 这是「清理器不许误伤」的护栏：全部清空同样能让「过期正文被清掉」的断言通过，
// 所以必须同时断言未到期那条仍然保留且可读。
func TestClearExpiredLogContents(t *testing.T) {
	const content = `[{"level":"info","text":"清扫用正文"}]`

	db := newCommandSvcTestDB(t)
	ensureReadScopeTables(t, db)
	seedReadScopeServer(t, db, "prod", "lobby-live")
	repo := repository.NewAgentCommandRepository(db)
	svc := newCommandSvc(db)
	logs := NewAgentLogService(db, repo, repository.NewAuditLogRepository(db))

	seed := func(serverID string, expiry time.Time) *model.AgentCommand {
		t.Helper()
		cmd := &model.AgentCommand{
			NamespaceCode: "prod", ServerID: serverID, Type: model.CommandTypeTailLogs,
			Status: model.CommandStatusDone, LogContent: content, Operator: "alice",
		}
		if err := db.Create(cmd).Error; err != nil {
			t.Fatalf("创建 %s 的日志命令失败: %v", serverID, err)
		}
		setLogContentExpiry(t, db, cmd.ID, expiry)
		return cmd
	}
	expired := seed("lobby-expired", time.Now().UTC().Add(-time.Second))
	live := seed("lobby-live", time.Now().UTC().Add(time.Hour))

	cleared, err := svc.ClearExpiredLogContents(time.Now().UTC())
	if err != nil || cleared != 1 {
		t.Fatalf("应只清一条到期正文: cleared=%d err=%v", cleared, err)
	}

	gotExpired, err := repo.FindByID(expired.ID)
	if err != nil || gotExpired == nil {
		t.Fatalf("读取已过期命令失败: %+v err=%v", gotExpired, err)
	}
	if gotExpired.LogContent != "" || gotExpired.LogContentExpiresAt != nil {
		t.Fatalf("到期正文与期限列都应清空，实际 content=%q expiry=%v", gotExpired.LogContent, gotExpired.LogContentExpiresAt)
	}
	if gotExpired.Status != model.CommandStatusDone {
		t.Fatalf("清理正文不得改动命令状态（done 供无正文的审计与观测使用），实际 %s", gotExpired.Status)
	}
	gotLive, err := repo.FindByID(live.ID)
	if err != nil || gotLive == nil {
		t.Fatalf("读取未到期命令失败: %+v err=%v", gotLive, err)
	}
	if gotLive.LogContent != content || gotLive.LogContentExpiresAt == nil {
		t.Fatalf("未到期正文必须原样保留，实际 content=%q expiry=%v", gotLive.LogContent, gotLive.LogContentExpiresAt)
	}
	// 保留期内的正文清扫后仍应可读——确认清扫没有把正文清成「读了也拿不到」。
	res, err := logs.ReadCommand("prod", "lobby-live", live.ID, "alice", "127.0.0.1")
	if err != nil || len(res.Lines) != 1 || res.Lines[0].Text != "清扫用正文" {
		t.Fatalf("未到期正文清扫后仍应可读，实际 %+v err=%v", res, err)
	}
}

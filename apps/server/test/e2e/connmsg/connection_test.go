//go:build e2e

// FR-145 连接明细采集 wire 端到端：真 BungeeCord + BeaconAgentProxy(role=bungee) 上，连接采集探针直接驱动
// agent 已装配的真采集入口 BungeeConnectionListener.tracker 喂 open/换服/close，走真有界缓冲 →
// 真 ConnectionReportCoordinator → 真 BeaconApiClient.reportConnectionsBatch → 真控制面 /connections/batch，
// 端到端验证连接采集的 wire 与 conn_detail 会话行落库（open 插入、close 更新同行、时长/断因/首末后端/换服数正确）。
//
// 除「真玩家登入触发 BC 事件」这一段（harness 无真玩家，留真机）外，wire 与落库全由真实代码路径覆盖。
// 队列满 429 / 未确认 403 / 孤儿 open 经新 bootId 补 close 见文末「留真机 / 已由单测集成覆盖」说明。
//
// 编排相位：build → namespace → agent(真 BungeeCord，role=bungee + 连接采集探针) → approve → online →
// 等探针注入 + 直读 conn_detail 日表断言 open→closed。
package connmsg_e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	connNamespace = "p5conn"
	// 与 serveProxy 默认 serverId 对齐。
	connServerID = "e2e-bungee-1"

	connLogPrefixCP = "beacon-connmsg-conn"
	connLogPrefixMC = "bungeecord-connmsg-conn"
	connSQLiteDB    = "beacon-e2e-connmsg-conn.db"

	// 与 ConnectionInjectE2EProbe 中常量一致（Go 侧按此 player_uuid 查会话行）。
	connPlayerUUID   = "0192e2e0-0000-7000-8000-00000000c0de"
	connFirstBackend = "e2e-backend-a"
	connLastBackend  = "e2e-backend-b"

	connPendingWait = 18 * time.Minute
	connOnlineWait  = 6 * time.Minute
	// 探针注入 open→（隔观察窗）close 后，经上报循环（5s 周期）+ 异步入库，给足 4 分钟到「会话行 closed」。
	connPersistWait = 4 * time.Minute
	// 探针注入活性观测（tracker 就绪并喂事件）给 3 分钟余量。
	connInjectWait = 3 * time.Minute
)

// TestConnectionWireE2E 按相位编排 FR-145 真采集入口连接明细 wire 端到端。
func TestConnectionWireE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	sqliteDB := filepath.Join(repoRoot, ".tmp", connSQLiteDB)
	_ = removeIfExists(sqliteDB)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（SQLite）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret,
		BootstrapToken: "legacy-token-unused-by-v2",
		LogPrefix:      connLogPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp)

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	ns := createNamespace(t, base, adminToken, connNamespace, "FR-145 连接采集 wire e2e")
	t.Logf("已建 v2 namespace id=%d", ns.ID)

	t.Log("== 起 BungeeCord 代理 + 真 BeaconAgentProxy（role=bungee + 连接采集探针）==")
	proxyEnv := harness.AgentGradleEnv(base, ns.AccessToken, connNamespace, connServerID, "127.0.0.1:25577")
	proxyEnv["BEACON_E2E_CONNINJECT"] = "1"
	bungee, err := harness.StartGradleTask(repoRoot, ":agent-e2e:serveProxy", nil, proxyEnv, connLogPrefixMC)
	if err != nil {
		t.Fatalf("起 BungeeCord 失败：%v", err)
	}
	harness.CleanupGradle(t, bungee)

	t.Log("== 等真 agent(proxy) v2 注册进 pending（首跑含下载/构建，耐心等）==")
	identityID := waitPendingIdentity(t, base, adminToken, ns.ID, connServerID, connPendingWait, bungee)
	t.Logf("观测到 pending 身份 identityId=%s", identityID)

	t.Log("== approve 使身份 active 并等 online ==")
	approveIdentity(t, base, adminToken, identityID, bungee)
	waitIdentityStatus(t, base, adminToken, ns.ID, connServerID, "active", connPendingWait, bungee)
	if err := harness.WaitInstanceOnline(base, adminToken, connNamespace, connServerID, connOnlineWait, bungee); err != nil {
		t.Fatalf("active 后代理应 online（见 .tmp/%s.out.log）：%v", connLogPrefixMC, err)
	}

	obsPath := filepath.Join(harness.ProxyRunDir(repoRoot), "plugins", "BeaconE2EProxy", "e2e-conninject.log")

	// ① 探针注入活性：真采集入口就绪并喂了 open。
	obs := waitMarkSource(t, obsPath, "INJECTED_OPEN", connInjectWait, "连接采集探针注入 open 事件", bungee)
	t.Logf("PASS 探针注入 open：%s", obs.rest)

	// ② 直读 conn_detail 当日表：会话行最终 closed，时长/断因/首末后端/换服数正确、归属该 proxy。
	db := openE2EDB(t, sqliteDB)
	var row connDetailRow
	err = waitUntil(connPersistWait, bungee, func(context.Context) bool {
		r, found := queryConnByPlayer(db, connPlayerUUID)
		if found && r.Status == model.ConnStatusClosed {
			row = r
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("conn_detail 未在 %s 内出现 player_uuid=%s 的 closed 会话行：%v", connPersistWait, connPlayerUUID, err)
	}
	if row.ProxyServerID != connServerID {
		t.Fatalf("会话行归属 proxy 应为 %s，实际 %s", connServerID, row.ProxyServerID)
	}
	if row.CloseKind != model.ConnCloseKindQuit {
		t.Fatalf("close_kind 应为 %q，实际 %q", model.ConnCloseKindQuit, row.CloseKind)
	}
	if row.DurationMs == nil || *row.DurationMs <= 0 {
		t.Fatalf("close 应结算正向 duration_ms，实际 %v", row.DurationMs)
	}
	// 末后端与换服数（close 事件更新落库）。
	if row.LastBackendServerID != connLastBackend {
		t.Fatalf("末后端应为 %s，实际 %s", connLastBackend, row.LastBackendServerID)
	}
	if row.BackendSwitchCount != 1 {
		t.Fatalf("一次换服 backend_switch_count 应为 1，实际 %d", row.BackendSwitchCount)
	}
	// 首后端（spec §3.2「首个后端子服」必须记录）。
	// 说明：open 事件在玩家登入代理时即发出、此刻尚未连任何后端，故 open 会话行 first_backend 恒为空；
	// 首后端只由 close 事件携带（tracker 会话摘要）。控制面须在 close 更新时把 first_backend 落到该行——
	// 本断言按 spec 期望值 e2e-backend-a 校验。若此处失败为空，即命中真实 P5a 缺陷：
	// repository/conn_detail_repo.go 的 upsertConnCloses DoUpdates 列表遗漏 first_backend_server_id
	// （仅含 last_backend_server_id），导致 close 更新既有 open 行时首后端被静默丢弃、永不落库。
	if row.FirstBackendServerID != connFirstBackend {
		t.Fatalf("首后端应为 %s，实际 %q —— 疑似控制面缺陷：upsertConnCloses 的 DoUpdates 未含 first_backend_server_id，"+
			"close 更新 open 行时首后端被丢弃（spec §3.2 要求记录首个后端子服）", connFirstBackend, row.FirstBackendServerID)
	}
	t.Logf("PASS 连接采集落库：conn_id=%s status=closed close_kind=%s duration_ms=%d first=%s last=%s switch=%d",
		row.ConnID, row.CloseKind, *row.DurationMs, row.FirstBackendServerID, row.LastBackendServerID, row.BackendSwitchCount)
}

// ---- conn_detail 直读 ----

// connDetailRow 是 conn_detail 当日表断言用的行投影（列名按 GORM snake_case 映射）。
type connDetailRow struct {
	ConnID               string
	PlayerUUID           string
	PlayerName           string
	ClientIP             string
	ProxyServerID        string
	Status               string
	CloseKind            string
	DurationMs           *int64
	FirstBackendServerID string
	LastBackendServerID  string
	BackendSwitchCount   int
}

// queryConnByPlayer 查当日 conn_detail 表某 player_uuid 的会话行（取最新一条）；表不存在返回未命中。
func queryConnByPlayer(db *gorm.DB, playerUUID string) (connDetailRow, bool) {
	name := store.DailyTableName("conn_detail", time.Now().UTC())
	if !db.Migrator().HasTable(name) {
		return connDetailRow{}, false
	}
	var rows []connDetailRow
	if err := db.Table(name).Where("player_uuid = ?", playerUUID).Order("opened_at DESC").Find(&rows).Error; err != nil {
		return connDetailRow{}, false
	}
	if len(rows) == 0 {
		return connDetailRow{}, false
	}
	return rows[0], true
}

//go:build e2e

// FR-149/150 跨服消息 wire 端到端：真 Bukkit agent 经门面 messaging().send/call/on 走 HttpMessageTransport →
// 真控制面 /messages/send + /messages/poll + /messages/ack，端到端验证消息收发的 wire 与终态落库。
//
// 单 agent 自寻址（目标 = 本服 serverId）即可覆盖完整传输链：send 上行 → 控制面受理入队 → 本服长轮询取走
// dispatched → 交本机 on 处理器 delivered → 回执落 msg_trace（终态才落库）。RPC 亦自寻址：请求 correlationId
// 自引用其 messageId、handler 回信、响应 correlationId=请求 messageId，Future 往返完成。失败路径以玩家寻址
// 落空（随机 UUID 名册无此人 → failed(player_not_online)）覆盖。
//
// 编排相位：build → namespace → agent(真 Paper，messaging.enabled=true + 消息探针) → approve → online →
// 等探针观测 + 直读 msg_trace/msg_payload 日表断言。铁律：只调既有门面 + admin API + GORM 直读，
// 绝不旁路或弱化任一约束来「让断言通过」；wire 不一致即失败暴露、不改测试蒙混。
package connmsg_e2e

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	msgNamespace = "p5msg"
	// 与 runServer 默认 serverId 对齐。
	msgServerID = "e2e-bukkit-1"
	// MC 监听端口：避让 25565/25566/25568/25569/25570 已占用，本用例用 25571。
	msgMCPort = "25571"

	msgLogPrefixCP = "beacon-connmsg-msg"
	msgLogPrefixMC = "paper-connmsg-msg"
	msgSQLiteDB    = "beacon-e2e-connmsg-msg.db"

	// 探针业务消息类型（与 MessagingE2EProbe 中常量一致）。
	typeMsg   = "beacon-e2e-msg"
	typeRPC   = "beacon-e2e-rpc"
	typeMiss  = "beacon-e2e-miss"
	typeBcast = "beacon-e2e-bcast"

	// 首跑含 gradle 冷编译全 agent 模块 + 下载 Paper + 运行期依赖，给足到「pending 身份出现」。
	msgPendingWait = 18 * time.Minute
	msgOnlineWait  = 6 * time.Minute
	// 探针每 ~2s 驱动一次收发；approve 后一两轮即完成，给 3 分钟余量。
	msgObsWait = 3 * time.Minute
	// 终态记录异步入库（写入通道 ~500ms 攒批 flush），给 2 分钟余量。
	msgPersistWait = 2 * time.Minute
)

// TestMessageWireE2E 按相位编排 FR-149/150 真门面消息 wire 端到端。
func TestMessageWireE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	sqliteDB := filepath.Join(repoRoot, ".tmp", msgSQLiteDB)
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
		LogPrefix:      msgLogPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	defer cp.Stop()

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	ns := createNamespace(t, base, adminToken, msgNamespace, "FR-149 消息 wire e2e")
	t.Logf("已建 v2 namespace id=%d", ns.ID)

	t.Log("== 起 Paper 子服 + 真 BeaconAgent（messaging.enabled=true + 消息探针）==")
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:runServer", []string{
		"-Pe2eMcPort=" + msgMCPort,
		"-Pe2eBeaconEndpoint=" + base,
		"-Pe2eBootstrapToken=" + ns.AccessToken,
		"-Pe2eNamespace=" + msgNamespace,
		"-Pe2eServerId=" + msgServerID,
		"-Pe2eMessaging",
	}, msgLogPrefixMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	defer paper.Stop()

	t.Log("== 等真 agent v2 注册进 pending（首跑含下载/构建，耐心等）==")
	identityID := waitPendingIdentity(t, base, adminToken, ns.ID, msgServerID, msgPendingWait)
	t.Logf("观测到 pending 身份 identityId=%s", identityID)

	t.Log("== approve 使身份 active 并等 online ==")
	approveIdentity(t, base, adminToken, identityID)
	waitIdentityStatus(t, base, adminToken, ns.ID, msgServerID, "active", msgPendingWait)
	if err := harness.WaitInstanceOnline(base, adminToken, msgNamespace, msgServerID, msgOnlineWait); err != nil {
		t.Fatalf("active 后应衔接 legacy 数据面 online（见 .tmp/%s.out.log）：%v", msgLogPrefixMC, err)
	}

	obsPath := filepath.Join(repoRoot, ".tmp", "e2e-run", "bukkit", "plugins", "BeaconE2E", "e2e-messaging.log")

	t.Run("directed_delivered", func(t *testing.T) { runDirectedDelivered(t, obsPath, sqliteDB) })
	t.Run("rpc_roundtrip", func(t *testing.T) { runRPCRoundtrip(t, obsPath, sqliteDB) })
	t.Run("player_not_online", func(t *testing.T) { runPlayerNotOnline(t, obsPath, sqliteDB) })
	t.Run("broadcast_delivered", func(t *testing.T) { runBroadcastDelivered(t, obsPath, sqliteDB) })
}

// runDirectedDelivered 断言定向 send 自寻址端到端 delivered：本机 on 收到 + msg_trace/msg_payload 落库。
func runDirectedDelivered(t *testing.T, obsPath, sqliteDB string) {
	// ① 探针本机 on 处理器已收到自寻址的定向消息（wire 收发闭环 API 侧证据）。
	obs := waitMarkSource(t, obsPath, "MSG_RECEIVED", msgObsWait, "本机 on 收到自寻址定向消息")
	t.Logf("PASS on 收到定向消息：%s", obs.rest)

	// ② 直读 msg_trace 当日表：定向（target_kind=server）终态 delivered、单跳、链路完整、payload 落库。
	db := openE2EDB(t, sqliteDB)
	var row msgTraceRow
	ok := waitUntil(msgPersistWait, func() bool {
		rows := queryTraces(db, typeMsg, "server")
		for _, r := range rows {
			if r.Status == model.MsgStatusDelivered {
				row = r
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("msg_trace 未在 %s 内出现 msg_type=%s target_kind=server status=delivered 的行", msgPersistWait, typeMsg)
	}
	if row.SourceServerID != msgServerID || row.ResolvedServerID != msgServerID {
		t.Fatalf("定向自寻址应 source=resolved=%s，实际 source=%s resolved=%s", msgServerID, row.SourceServerID, row.ResolvedServerID)
	}
	if row.HopCount != 1 {
		t.Fatalf("经控制面单跳中转 hop_count 应为 1，实际 %d（hops=%s）", row.HopCount, row.Hops)
	}
	if row.DurationMs == nil || *row.DurationMs < 0 {
		t.Fatalf("delivered 应结算非负 duration_ms，实际 %v", row.DurationMs)
	}
	assertHopsComplete(t, row.Hops)
	if !row.PayloadStored {
		t.Fatalf("payload_stored 应为 true，实际 false（message_id=%s）", row.MessageID)
	}
	// ③ 元数据 / payload 分表：msg_payload 当日表存在对应行。
	if !payloadExists(db, row.MessageID) {
		t.Fatalf("msg_payload 当日表应有 message_id=%s 的对应行（元数据/payload 同事务分表落库）", row.MessageID)
	}
	t.Logf("PASS 定向 delivered：message_id=%s hop_count=%d duration_ms=%d hops 完整、payload 分表落库", row.MessageID, row.HopCount, *row.DurationMs)
}

// runRPCRoundtrip 断言 RPC call 自寻址往返：Future 完成 + msg_trace 请求/响应两行经 correlationId 关联、均 delivered。
func runRPCRoundtrip(t *testing.T, obsPath, sqliteDB string) {
	obs := waitMarkSource(t, obsPath, "RPC_REPLY", msgObsWait, "RPC call Future 往返完成")
	t.Logf("PASS RPC Future 完成：%s", obs.rest)

	db := openE2EDB(t, sqliteDB)
	var request, response msgTraceRow
	ok := waitUntil(msgPersistWait, func() bool {
		rows := queryTraces(db, typeRPC, "")
		var req, resp msgTraceRow
		var haveReq, haveResp bool
		for _, r := range rows {
			if r.Status != model.MsgStatusDelivered {
				continue
			}
			if r.CorrelationID != "" && r.CorrelationID == r.MessageID {
				req, haveReq = r, true // 请求：correlationId 自引用其 messageId
			} else if r.CorrelationID != "" && r.CorrelationID != r.MessageID {
				resp, haveResp = r, true // 响应：correlationId = 请求 messageId
			}
		}
		if haveReq && haveResp && resp.CorrelationID == req.MessageID {
			request, response = req, resp
			return true
		}
		return false
	})
	if !ok {
		t.Fatalf("msg_trace 未在 %s 内出现 RPC 请求(自引用)+响应(回填请求 messageId) 两行且均 delivered（type=%s）", msgPersistWait, typeRPC)
	}
	if request.HopCount != 1 || response.HopCount != 1 {
		t.Fatalf("RPC 两条均应单跳 hop_count=1，实际 请求=%d 响应=%d", request.HopCount, response.HopCount)
	}
	t.Logf("PASS RPC 往返：请求 message_id=%s（correlationId 自引用）↔ 响应 message_id=%s（correlationId=%s）均 delivered",
		request.MessageID, response.MessageID, response.CorrelationID)
}

// runPlayerNotOnline 断言玩家寻址落空：随机 UUID 名册无此人 → msg_trace failed(player_not_online)。
func runPlayerNotOnline(t *testing.T, obsPath, sqliteDB string) {
	obs := waitMarkSource(t, obsPath, "PLAYER_MISS_SENT", msgObsWait, "玩家寻址落空已发送")
	t.Logf("PASS 玩家寻址落空已发送：%s", obs.rest)

	db := openE2EDB(t, sqliteDB)
	var row msgTraceRow
	ok := waitUntil(msgPersistWait, func() bool {
		rows := queryTraces(db, typeMiss, "player")
		for _, r := range rows {
			if r.Status == model.MsgStatusFailed {
				row = r
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("msg_trace 未在 %s 内出现 msg_type=%s target_kind=player status=failed 的行", msgPersistWait, typeMiss)
	}
	if row.FailReason != model.MsgFailPlayerNotOnline {
		t.Fatalf("玩家寻址落空 fail_reason 应为 %q，实际 %q", model.MsgFailPlayerNotOnline, row.FailReason)
	}
	t.Logf("PASS 玩家寻址落空：message_id=%s status=failed fail_reason=%s", row.MessageID, row.FailReason)
}

// runBroadcastDelivered 断言广播 publish 含自身语义端到端 delivered（FR-180 / ADR-0065）：
// 本机 subscribe 收到经控制面 fan-out 回投的自身广播 + msg_trace 落「一行」聚合广播行
// （target_kind=broadcast、无 zone、单机在线集合仅本服 → fanout_total=1/delivered_count=1）。
func runBroadcastDelivered(t *testing.T, obsPath, sqliteDB string) {
	// ① 探针本机 subscribe 处理器已收到自身广播（广播收发闭环 + 含自身语义 API 侧证据）。
	obs := waitMarkSource(t, obsPath, "BCAST_RECEIVED", msgObsWait, "本机 subscribe 收到自身广播")
	t.Logf("PASS subscribe 收到广播：%s", obs.rest)

	// ② 直读 msg_trace 当日表：广播（target_kind=broadcast）终态 delivered 的聚合行。
	db := openE2EDB(t, sqliteDB)
	var row msgTraceRow
	ok := waitUntil(msgPersistWait, func() bool {
		for _, r := range queryTraces(db, typeBcast, "broadcast") {
			if r.Status == model.MsgStatusDelivered {
				row = r
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("msg_trace 未在 %s 内出现 msg_type=%s target_kind=broadcast status=delivered 的行", msgPersistWait, typeBcast)
	}

	if row.SourceServerID != msgServerID {
		t.Fatalf("广播 source 应为 %s，实际 %s", msgServerID, row.SourceServerID)
	}

	// ③ 广播聚合不变量：一条广播落「一行」聚合、fan-out 各目标计数进聚合列（防 ×N 写放大，ADR-0065）。
	//    单机在线集合仅本服 → 每行 fanout_total=1/delivered_count=1、failed/expired=0、无 zone 定向 target_zone 空、payload 只分表存一份。
	//    探针收到自身广播前可能按轮重发（与定向 send 自愈同型），各次 publish 是独立广播、各落一行且各自 fanout=1，
	//    故按「≥1 行 delivered 且每行聚合恒等」断言（比恰一行更健壮，同样证得单目标聚合与无 ×N 放大）。
	deliveredRows := 0
	for _, r := range queryTraces(db, typeBcast, "broadcast") {
		if r.Status != model.MsgStatusDelivered {
			continue
		}
		deliveredRows++
		if r.FanoutTotal == nil || *r.FanoutTotal != 1 {
			t.Fatalf("单机在线集合每条广播 fanout_total 应为 1，实际 %v（message_id=%s）", r.FanoutTotal, r.MessageID)
		}
		if r.DeliveredCount == nil || *r.DeliveredCount != 1 {
			t.Fatalf("每条广播 delivered_count 应为 1，实际 %v（message_id=%s）", r.DeliveredCount, r.MessageID)
		}
		if r.FailedCount == nil || *r.FailedCount != 0 || r.ExpiredCount == nil || *r.ExpiredCount != 0 {
			t.Fatalf("每条广播 failed/expired 聚合计数应为 0，实际 failed=%v expired=%v（message_id=%s）", r.FailedCount, r.ExpiredCount, r.MessageID)
		}
		if r.TargetZone != nil {
			t.Fatalf("全 namespace 广播 target_zone 应为空，实际 %q（message_id=%s）", *r.TargetZone, r.MessageID)
		}
		// payload 只存一份分表落库（广播 payload 不因 fan-out 放大）。
		if !r.PayloadStored || !payloadExists(db, r.MessageID) {
			t.Fatalf("广播 payload 应分表落库一份（message_id=%s）", r.MessageID)
		}
	}
	if deliveredRows < 1 {
		t.Fatalf("应至少一行 delivered 广播聚合，实际 %d", deliveredRows)
	}
	t.Logf("PASS 广播 delivered：%d 行聚合、每行 fanout_total=1/delivered_count=1、无 zone、payload 分表落库", deliveredRows)
}

// ---- msg_trace / msg_payload 直读 ----

// msgTraceRow 是 msg_trace 当日表断言用的行投影（列名按 GORM snake_case 映射）。
type msgTraceRow struct {
	MessageID        string
	MsgType          string
	TargetKind       string
	SourceServerID   string
	ResolvedServerID string
	CorrelationID    string
	Status           string
	FailReason       string
	HopCount         int
	Hops             string
	DurationMs       *int64
	PayloadStored    bool
	// 广播聚合列（仅 target_kind=broadcast 行非空，FR-180）
	TargetZone     *string
	FanoutTotal    *int
	DeliveredCount *int
	FailedCount    *int
	ExpiredCount   *int
}

// queryTraces 查当日 msg_trace 表某 msgType（targetKind 非空时再按 target_kind 过滤）；表不存在返回空。
func queryTraces(db *gorm.DB, msgType, targetKind string) []msgTraceRow {
	name := store.DailyTableName("msg_trace", time.Now().UTC())
	if !db.Migrator().HasTable(name) {
		return nil
	}
	q := db.Table(name).Where("msg_type = ?", msgType)
	if targetKind != "" {
		q = q.Where("target_kind = ?", targetKind)
	}
	var rows []msgTraceRow
	if err := q.Find(&rows).Error; err != nil {
		return nil
	}
	return rows
}

// payloadExists 查当日 msg_payload 表是否有指定 message_id 的行（元数据/payload 分表落库证据）。
func payloadExists(db *gorm.DB, messageID string) bool {
	name := store.DailyTableName("msg_payload", time.Now().UTC())
	if !db.Migrator().HasTable(name) {
		return false
	}
	var count int64
	if err := db.Table(name).Where("message_id = ?", messageID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// assertHopsComplete 断言 hops 链路事件文本含 sent/received/dispatched/delivered 四个环节（spec §3.3）。
func assertHopsComplete(t *testing.T, hops string) {
	t.Helper()
	for _, ev := range []string{`"event":"sent"`, `"event":"received"`, `"event":"dispatched"`, `"event":"delivered"`} {
		if !contains(hops, ev) {
			t.Fatalf("hops 链路应含 %s，实际 hops=%s", ev, hops)
		}
	}
}

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
)

// fakeAssetInstances 是在线校验的假实现：online=true 视目标在册，否则 INSTANCE_NOT_FOUND。
type fakeAssetInstances struct{ online bool }

func (f fakeAssetInstances) Get(_, _ string) (*runtime.Instance, error) {
	if f.online {
		return &runtime.Instance{}, nil
	}
	return nil, apperr.ErrInstanceNotFound
}

// newAssetSvcTestDB 打开独立命名的内存 sqlite（单连接串行化，避免 shared-cache 并发 busy）并迁移相关表。
func newAssetSvcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := "file:assetsvc_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.FileAsset{},
		&model.AgentCommand{}, &model.Setting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newAssetSvc 构造被测服务（真 longpoll.Hub 承载结果 waiter，notifier=nil 由 agent 模拟器直接轮询拉命令）。
func newAssetSvc(db *gorm.DB, online bool) *AssetPreviewService {
	return NewAssetPreviewService(db, repository.NewAgentCommandRepository(db),
		repository.NewFileAssetRepository(db), repository.NewSettingRepository(db),
		repository.NewAuditLogRepository(db), longpoll.NewHub(), nil, fakeAssetInstances{online: online})
}

// seedPreviewServer 插入 server 行（namespace 按 code 复用或首建），返回 server 行主键 id（file_asset.server_id 引用）。
func seedPreviewServer(t *testing.T, db *gorm.DB, nsCode, serverID string) uint {
	t.Helper()
	var ns model.Namespace
	if err := db.Where("code = ?", nsCode).First(&ns).Error; err != nil {
		ns = model.Namespace{Code: nsCode, Name: nsCode}
		if e := db.Create(&ns).Error; e != nil {
			t.Fatalf("建 namespace 失败: %v", e)
		}
	}
	srv := model.Server{NamespaceID: ns.ID, ServerID: serverID, Kind: "backend"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}
	return srv.ID
}

// seedAsset 插入一行 file_asset 清单。
func seedAsset(t *testing.T, db *gorm.DB, serverRow uint, path, sha string, size int64, isText bool) {
	t.Helper()
	if err := db.Create(&model.FileAsset{
		NamespaceID: 1, ServerID: serverRow, Path: path, Ext: "yml", SHA256: sha,
		Size: size, IsText: isText, ScannedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("建 file_asset 失败: %v", err)
	}
}

// startAgentSim 后台模拟 agent：轮询 pending asset-read 命令 → CAS fetched → 按 respond(path) 回传内容。
func startAgentSim(t *testing.T, db *gorm.DB, svc *AssetPreviewService, respond func(path string) AssetContentPayload) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		cmdRepo := repository.NewAgentCommandRepository(db)
		for {
			select {
			case <-done:
				return
			default:
			}
			var cmd model.AgentCommand
			if err := db.Where("type = ? AND status = ?", model.CommandTypeAssetRead, model.CommandStatusPending).
				Order("id asc").First(&cmd).Error; err == nil {
				if ok, _ := cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); ok {
					var pl assetReadPayload
					_ = json.Unmarshal([]byte(cmd.Payload), &pl)
					_ = svc.ReceiveContent(cmd.NamespaceCode, cmd.ServerID, cmd.ID, respond(pl.Path))
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() { close(done) }
}

func textReply(path string) AssetContentPayload {
	return AssetContentPayload{Content: "内容:" + path}
}

// TestPreviewSuccess 文本预览成功：返回内容 + 元数据，记 asset.preview 审计，且内容绝不落库 / 不入审计 detail。
func TestPreviewSuccess(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "plugins/Essentials/config.yml", "abc", 42, true)
	stop := startAgentSim(t, db, svc, textReply)
	defer stop()

	res, err := svc.Preview(context.Background(), PreviewParams{
		ServerID: "lobby-1", Path: "plugins/Essentials/config.yml", Operator: "alice", ClientIP: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("预览应成功: %v", err)
	}
	if res.Content == nil || *res.Content != "内容:plugins/Essentials/config.yml" {
		t.Fatalf("应返回 agent 回传内容，实际 %v", res.Content)
	}
	if res.Binary || res.Truncated || res.Sensitive {
		t.Fatalf("普通文本不应二进制 / 截断 / 敏感，实际 %+v", res)
	}
	if countAudit(t, db, model.ActionAssetPreview) != 1 {
		t.Fatal("应记一条 asset.preview 审计")
	}
	// 内容绝不入审计 detail、绝不落命令任何列。
	if auditDetailContains(t, db, model.ActionAssetPreview, "内容:") {
		t.Fatal("审计 detail 绝不应含文件内容")
	}
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusDone {
		t.Fatalf("命令应 done，实际 %+v", cmds)
	}
	if cmds[0].BrowseResult != "" || cmds[0].LogContent != "" || cmds[0].ImprintContent != "" || strings.Contains(cmds[0].ResultDetail, "内容:") {
		t.Fatal("文件内容绝不应落 agent_command 任何列（不落库）")
	}
	if strings.Contains(cmds[0].Payload, "内容:") {
		t.Fatal("命令载荷不应含内容（仅 path/maxBytes）")
	}
}

// TestPreviewBinary agent 权威判定二进制 → content 为 nil、仅回元数据。
func TestPreviewBinary(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "plugins/Essentials.jar", "bin", 999, false)
	stop := startAgentSim(t, db, svc, func(string) AssetContentPayload {
		return AssetContentPayload{Binary: true}
	})
	defer stop()

	res, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "plugins/Essentials.jar", Operator: "a"})
	if err != nil {
		t.Fatalf("二进制预览应成功回元数据: %v", err)
	}
	if !res.Binary || res.Content != nil {
		t.Fatalf("二进制应 content=nil、binary=true，实际 %+v", res)
	}
	// sha256/size 取清单权威值（全文哈希），非 agent 前缀读。
	if res.SHA256 != "bin" || res.Size != 999 {
		t.Fatalf("二进制元数据应取清单 sha256/size，实际 %+v", res)
	}
}

// TestPreviewTruncated 超限截断 → truncated=true 透传。
func TestPreviewTruncated(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "big.yml", "big", 1<<20, true)
	stop := startAgentSim(t, db, svc, func(string) AssetContentPayload {
		return AssetContentPayload{Truncated: true, Content: "前512KiB"}
	})
	defer stop()

	res, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "big.yml", Operator: "a"})
	if err != nil || !res.Truncated {
		t.Fatalf("应 truncated=true，实际 res=%+v err=%v", res, err)
	}
}

// TestPreviewNotFound 清单缺该文件 / 服务器不可解析 → asset_not_found。
func TestPreviewNotFound(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	seedPreviewServer(t, db, "prod", "lobby-1")

	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "missing.yml", Operator: "a"}); err != apperr.ErrAssetNotFound {
		t.Fatalf("清单缺文件应 asset_not_found，实际 %v", err)
	}
	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "ghost", Path: "x.yml", Operator: "a"}); err != apperr.ErrAssetNotFound {
		t.Fatalf("未知 server 应 asset_not_found，实际 %v", err)
	}
	if len(allCommands(t, db)) != 0 {
		t.Fatal("存在性校验未过不应建命令")
	}
}

// TestPreviewSensitiveGuard 命中敏感清单无 reason → 403 且不建命令 / 不审计；填 reason → 放行 + 审计带 sensitiveOverride。
func TestPreviewSensitiveGuard(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "plugins/Beacon/config.yml", "sec", 10, true) // 命中 plugins/Beacon/**

	// 无 reason → 403，不建命令、不审计。
	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "plugins/Beacon/config.yml", Operator: "a"}); err != apperr.ErrAssetSensitivePath {
		t.Fatalf("敏感无 reason 应 asset_sensitive_path，实际 %v", err)
	}
	if len(allCommands(t, db)) != 0 || countAudit(t, db, model.ActionAssetPreview) != 0 {
		t.Fatal("敏感拒绝不应建命令 / 审计")
	}

	// 填 reason → 放行 + 审计带 sensitiveOverride + 原因。
	stop := startAgentSim(t, db, svc, textReply)
	defer stop()
	res, err := svc.Preview(context.Background(), PreviewParams{
		ServerID: "lobby-1", Path: "plugins/Beacon/config.yml", Reason: "排查登录异常", Operator: "a",
	})
	if err != nil || !res.Sensitive {
		t.Fatalf("填 reason 应放行且 sensitive=true，实际 res=%+v err=%v", res, err)
	}
	if !auditDetailContains(t, db, model.ActionAssetPreview, "sensitiveOverride") || !auditDetailContains(t, db, model.ActionAssetPreview, "排查登录异常") {
		t.Fatal("敏感放行审计应含 sensitiveOverride + 原因原文")
	}
}

// TestPreviewOffline 目标 agent 离线 → asset_agent_offline，不建命令。
func TestPreviewOffline(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, false) // 离线
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "a.yml", "x", 1, true)

	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "a.yml", Operator: "a"}); err != apperr.ErrAssetAgentOffline {
		t.Fatalf("离线应 asset_agent_offline，实际 %v", err)
	}
	if len(allCommands(t, db)) != 0 {
		t.Fatal("离线不应建命令")
	}
}

// TestPreviewTimeout 在线但 agent 不回传（无模拟器）→ asset_preview_timeout。
func TestPreviewTimeout(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "a.yml", "x", 1, true)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := svc.Preview(ctx, PreviewParams{ServerID: "lobby-1", Path: "a.yml", Operator: "a"}); err != apperr.ErrAssetPreviewTimeout {
		t.Fatalf("无回传应 asset_preview_timeout，实际 %v", err)
	}
}

// TestPreviewInvalidParam 缺 serverId / path → INVALID_PARAM。
func TestPreviewInvalidParam(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "", Path: "a", Operator: "a"}); err != apperr.ErrInvalidParam {
		t.Fatalf("缺 serverId 应 INVALID_PARAM，实际 %v", err)
	}
}

// TestPreviewReadFailed agent 回传 error（清单存在但现取失败）→ asset_read_failed，命令 failed。
func TestPreviewReadFailed(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "gone.yml", "x", 1, true)
	stop := startAgentSim(t, db, svc, func(string) AssetContentPayload {
		return AssetContentPayload{Error: "目标不存在或不可读"}
	})
	defer stop()

	if _, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "gone.yml", Operator: "a"}); err != apperr.ErrAssetReadFailed {
		t.Fatalf("agent 读失败应 asset_read_failed，实际 %v", err)
	}
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusFailed {
		t.Fatalf("读失败命令应 failed，实际 %+v", cmds)
	}
}

// TestDiffIdentical 两侧清单哈希相同 → 短路 identical，不取内容（无命令），记 asset.diff。
func TestDiffIdentical(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	l := seedPreviewServer(t, db, "prod", "lobby-1")
	rr := seedPreviewServer(t, db, "prod", "lobby-2")
	seedAsset(t, db, l, "a.yml", "same", 10, true)
	seedAsset(t, db, rr, "a.yml", "same", 10, true)

	res, err := svc.Diff(context.Background(), DiffParams{
		Left: AssetRef{"lobby-1", "a.yml"}, Right: AssetRef{"lobby-2", "a.yml"}, Operator: "a",
	})
	if err != nil || !res.Identical {
		t.Fatalf("同哈希应 identical，实际 res=%+v err=%v", res, err)
	}
	if len(allCommands(t, db)) != 0 {
		t.Fatal("identical 短路不应取内容 / 建命令")
	}
	if countAudit(t, db, model.ActionAssetDiff) != 1 {
		t.Fatal("应记一条 asset.diff 审计")
	}
}

// TestDiffDifferent 两侧不同哈希 → 并行取内容返回双侧。
func TestDiffDifferent(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	l := seedPreviewServer(t, db, "prod", "lobby-1")
	rr := seedPreviewServer(t, db, "prod", "lobby-2")
	seedAsset(t, db, l, "a.yml", "h1", 10, true)
	seedAsset(t, db, rr, "a.yml", "h2", 10, true)
	stop := startAgentSim(t, db, svc, textReply)
	defer stop()

	res, err := svc.Diff(context.Background(), DiffParams{
		Left: AssetRef{"lobby-1", "a.yml"}, Right: AssetRef{"lobby-2", "a.yml"}, Operator: "a",
	})
	if err != nil || res.Identical || res.Left == nil || res.Right == nil {
		t.Fatalf("不同哈希应返回双侧内容，实际 res=%+v err=%v", res, err)
	}
	if res.Left.Content == "" || res.Right.Content == "" {
		t.Fatal("两侧内容应非空")
	}
}

// TestDiffUnsupported 任一侧二进制（!isText）或超 512KiB → asset_diff_unsupported（早拒，不建命令）。
func TestDiffUnsupported(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	l := seedPreviewServer(t, db, "prod", "lobby-1")
	rr := seedPreviewServer(t, db, "prod", "lobby-2")
	seedAsset(t, db, l, "a.jar", "h1", 10, false) // 二进制
	seedAsset(t, db, rr, "a.jar", "h2", 10, true)
	if _, err := svc.Diff(context.Background(), DiffParams{Left: AssetRef{"lobby-1", "a.jar"}, Right: AssetRef{"lobby-2", "a.jar"}, Operator: "a"}); err != apperr.ErrAssetDiffUnsupported {
		t.Fatalf("二进制侧应 asset_diff_unsupported，实际 %v", err)
	}

	db2 := newAssetSvcTestDB(t)
	svc2 := newAssetSvc(db2, true)
	l2 := seedPreviewServer(t, db2, "prod", "s1")
	r2 := seedPreviewServer(t, db2, "prod", "s2")
	seedAsset(t, db2, l2, "big.yml", "h1", 1<<20, true) // 超 512KiB
	seedAsset(t, db2, r2, "big.yml", "h2", 10, true)
	if _, err := svc2.Diff(context.Background(), DiffParams{Left: AssetRef{"s1", "big.yml"}, Right: AssetRef{"s2", "big.yml"}, Operator: "a"}); err != apperr.ErrAssetDiffUnsupported {
		t.Fatalf("超限侧应 asset_diff_unsupported，实际 %v", err)
	}
	if len(allCommands(t, db2)) != 0 {
		t.Fatal("早拒不应建命令")
	}
}

// TestDiffSensitiveGuard 任一侧敏感无 reason → 403。
func TestDiffSensitiveGuard(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)
	l := seedPreviewServer(t, db, "prod", "lobby-1")
	rr := seedPreviewServer(t, db, "prod", "lobby-2")
	seedAsset(t, db, l, "plugins/Beacon/config.yml", "h1", 10, true) // 敏感
	seedAsset(t, db, rr, "plugins/Beacon/config.yml", "h2", 10, true)
	if _, err := svc.Diff(context.Background(), DiffParams{Left: AssetRef{"lobby-1", "plugins/Beacon/config.yml"}, Right: AssetRef{"lobby-2", "plugins/Beacon/config.yml"}, Operator: "a"}); err != apperr.ErrAssetSensitivePath {
		t.Fatalf("敏感无 reason 应 asset_sensitive_path，实际 %v", err)
	}
}

// TestSensitiveRulesLifecycle 默认 → PUT 自定义（审计）→ GET 回读 → 清空关闭保护。
func TestSensitiveRulesLifecycle(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)

	def, err := svc.GetSensitiveRules()
	if err != nil || len(def) != len(defaultSensitivePatterns) {
		t.Fatalf("无存储应返回内置默认，实际 %v err=%v", def, err)
	}

	got, err := svc.PutSensitiveRules([]string{"**/*.secret", " ", "plugins/X/**"}, "alice", "10.0.0.1")
	if err != nil || len(got) != 2 {
		t.Fatalf("PUT 应归一为 2 条，实际 %v err=%v", got, err)
	}
	if countAudit(t, db, model.ActionAssetSensitiveRuleUpdate) != 1 {
		t.Fatal("PUT 应记一条 asset.sensitive_rule_update 审计")
	}
	reread, _ := svc.GetSensitiveRules()
	if len(reread) != 2 || reread[0] != "**/*.secret" {
		t.Fatalf("GET 应回读 PUT 的规则，实际 %v", reread)
	}

	// 坏 glob（嵌套重复算子）拒绝落库。
	if _, err := svc.PutSensitiveRules([]string{"a???b"}, "a", ""); err != apperr.ErrInvalidParam {
		t.Fatalf("坏 glob 应 INVALID_PARAM，实际 %v", err)
	}

	// 清空 → 关闭保护：原敏感路径现无 reason 也可预览。
	if _, err := svc.PutSensitiveRules([]string{}, "a", ""); err != nil {
		t.Fatalf("清空应成功: %v", err)
	}
	empty, _ := svc.GetSensitiveRules()
	if len(empty) != 0 {
		t.Fatalf("清空后应为空清单，实际 %v", empty)
	}
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "plugins/Beacon/config.yml", "x", 10, true)
	stop := startAgentSim(t, db, svc, textReply)
	defer stop()
	res, err := svc.Preview(context.Background(), PreviewParams{ServerID: "lobby-1", Path: "plugins/Beacon/config.yml", Operator: "a"})
	if err != nil || res.Sensitive {
		t.Fatalf("清空保护后原敏感路径应可无 reason 预览且 sensitive=false，实际 res=%+v err=%v", res, err)
	}
}

// TestReceiveContentGuards 回传守卫：不存在 / 类型不符 / 归属不符 / 非 fetched 一律拒。
func TestReceiveContentGuards(t *testing.T) {
	db := newAssetSvcTestDB(t)
	svc := newAssetSvc(db, true)

	if err := svc.ReceiveContent("prod", "lobby-1", 99999, AssetContentPayload{}); err != apperr.ErrCommandNotFound {
		t.Fatalf("不存在命令应 ErrCommandNotFound，实际 %v", err)
	}
	// 建一条 asset-read 命令处 fetched，用错误归属回传。
	cmdRepo := repository.NewAgentCommandRepository(db)
	cmd := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeAssetRead, Status: model.CommandStatusFetched, Operator: "a", Payload: "{}"}
	if err := cmdRepo.Create(cmd); err != nil {
		t.Fatalf("建命令失败: %v", err)
	}
	if err := svc.ReceiveContent("prod", "other-server", cmd.ID, AssetContentPayload{}); err != apperr.ErrCommandNotFound {
		t.Fatalf("归属不符应 ErrCommandNotFound，实际 %v", err)
	}
}

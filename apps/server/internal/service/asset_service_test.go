package service

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// newAssetTestService 打开内存 sqlite、迁移相关表并构造 AssetService，返回 (db, svc, namespaceID, registry)。
// registry 是运行态注册表（在线真源）：重扫的在线判定只认它，需要「在线服」的用例用 seedRuntimeInstance 登记。
func newAssetTestService(t *testing.T) (*gorm.DB, *AssetService, uint, *runtime.Registry) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Namespace{}, &model.Server{}, &model.ServerTag{}, &model.AgentIdentity{},
		&model.FileAsset{}, &model.FileAssetScan{}, &model.AgentCommand{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移文件资产相关表失败: %v", err)
	}
	ns := model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	registry := runtime.NewRegistry()
	svc := NewAssetService(db, registry,
		repository.NewFileAssetRepository(db), repository.NewFileAssetScanRepository(db),
		repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))
	return db, svc, ns.ID, registry
}

// seedRuntimeInstance 在运行态注册表登记一台实例（namespace 用 code，与生产注册同口径），并按目标状态推进健康状态机：
// online 直接可用；degraded / lost / offline 由心跳陈旧度经 SweepExpired 推到位（阈值 15s / 30s / 60s）。
func seedRuntimeInstance(t *testing.T, registry *runtime.Registry, namespaceCode, serverID, status string) {
	t.Helper()
	now := time.Now().UTC()
	age := time.Duration(0)
	switch status {
	case runtime.StatusDegraded:
		age = 20 * time.Second
	case runtime.StatusLost:
		age = 40 * time.Second
	case runtime.StatusOffline:
		age = 70 * time.Second
	}
	if _, err := registry.Register(&runtime.Instance{Namespace: namespaceCode, ServerID: serverID, Role: "bukkit"}, time.Minute, now.Add(-age)); err != nil {
		t.Fatalf("登记运行实例 %s/%s 失败: %v", namespaceCode, serverID, err)
	}
	if age > 0 {
		registry.SweepExpired(now, 15*time.Second, 30*time.Second, time.Minute)
	}
}

// seedServer 建一台 server 行；online=true 时补一条 active 身份（供 rescan 目标定位与 ApplyManifest 定位）。
// 注意：active 身份**不再**代表在线，需要判在线的用例请再用 seedRuntimeInstance 登记运行实例。
func seedServer(t *testing.T, db *gorm.DB, namespaceID uint, serverID string, online bool) model.Server {
	t.Helper()
	server := model.Server{NamespaceID: namespaceID, ServerID: serverID, Kind: model.ServerKindBackend}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("建 server %s 失败: %v", serverID, err)
	}
	if online {
		if err := db.Create(&model.AgentIdentity{
			IdentityID: "id-" + serverID, NamespaceID: namespaceID, ServerID: model.NullableServerID(serverID),
			Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("建 active 身份失败: %v", err)
		}
	}
	return server
}

func identityFor(namespaceID uint, serverID string) agentauth.Identity {
	return agentauth.Identity{NamespaceID: namespaceID, Namespace: "prod", ServerID: serverID, Kind: model.ServerKindBackend}
}

func upsert(path, sha string, size, mtime int64) ManifestUpsert {
	return ManifestUpsert{Path: path, SHA256: sha, Size: size, MtimeMs: mtime}
}

// mustApply 上报并断言成功（测试准备用；失败即 Fatal）。
func mustApply(t *testing.T, svc *AssetService, p ManifestReportParams) ManifestReportResult {
	t.Helper()
	res, err := svc.ApplyManifest(p)
	if err != nil {
		t.Fatalf("上报应成功: %v", err)
	}
	return res
}

const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const shaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestApplyManifest_FullThenDelta(t *testing.T) {
	db, svc, nsID, _ := newAssetTestService(t)
	seedServer(t, db, nsID, "lobby-1", true)
	id := identityFor(nsID, "lobby-1")

	// 全量首报（单批 eof）。
	res, err := svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeFull, UploadID: "u1", Seq: 0, EOF: true,
		Upserts: []ManifestUpsert{upsert("plugins/A/config.yml", shaA, 10, 100), upsert("server.properties", shaB, 20, 200)},
	})
	if err != nil {
		t.Fatalf("全量上报失败: %v", err)
	}
	if res.FileCount != 2 || res.Digest == "" {
		t.Fatalf("全量应回 fileCount=2 + 非空摘要，实际 %+v", res)
	}
	var count int64
	db.Model(&model.FileAsset{}).Count(&count)
	if count != 2 {
		t.Fatalf("应落 2 行资产，实际 %d", count)
	}
	// ext 由 path 推导落库。
	var cfg model.FileAsset
	db.Where("path = ?", "plugins/A/config.yml").First(&cfg)
	if cfg.Ext != "yml" {
		t.Fatalf("ext 应推导为 yml，实际 %q", cfg.Ext)
	}
	baseDigest := res.Digest

	// 增量：改一文件 + 删一文件。
	res2, err := svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeDelta, BaseDigest: baseDigest,
		Upserts: []ManifestUpsert{upsert("plugins/A/config.yml", shaC, 11, 111)},
		Deleted: []string{"server.properties"},
	})
	if err != nil {
		t.Fatalf("增量上报失败: %v", err)
	}
	if res2.FileCount != 1 {
		t.Fatalf("增量后应剩 1 文件，实际 %d", res2.FileCount)
	}
	db.Where("path = ?", "plugins/A/config.yml").First(&cfg)
	if cfg.SHA256 != shaC || cfg.Size != 11 {
		t.Fatalf("增量应更新内容，实际 sha=%s size=%d", cfg.SHA256, cfg.Size)
	}

	// 基线失配 → 409。
	_, err = svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeDelta, BaseDigest: "stale",
		Upserts: []ManifestUpsert{upsert("plugins/A/config.yml", shaA, 1, 1)},
	})
	if !errors.Is(err, apperr.ErrAssetManifestOutOfSync) {
		t.Fatalf("基线失配应回 ErrAssetManifestOutOfSync，实际 %v", err)
	}
}

func TestApplyManifest_FullSharded(t *testing.T) {
	db, svc, nsID, _ := newAssetTestService(t)
	seedServer(t, db, nsID, "lobby-1", true)
	id := identityFor(nsID, "lobby-1")

	// 分片 1（未 eof）→ 未收齐、空摘要。
	res, err := svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeFull, UploadID: "u1", Seq: 0, EOF: false,
		Upserts: []ManifestUpsert{upsert("plugins/A/a.yml", shaA, 1, 1)},
	})
	if err != nil || res.Digest != "" {
		t.Fatalf("未收齐分片应回空摘要，got=%+v err=%v", res, err)
	}
	var count int64
	db.Model(&model.FileAsset{}).Count(&count)
	if count != 0 {
		t.Fatalf("未收齐前不应落库，实际 %d 行", count)
	}
	// 分片 2（eof）→ 收齐整体替换。
	res, err = svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeFull, UploadID: "u1", Seq: 1, EOF: true,
		Upserts: []ManifestUpsert{upsert("plugins/B/b.yml", shaB, 2, 2)},
	})
	if err != nil || res.FileCount != 2 {
		t.Fatalf("收齐应落 2 行，got=%+v err=%v", res, err)
	}
	db.Model(&model.FileAsset{}).Count(&count)
	if count != 2 {
		t.Fatalf("收齐后应落 2 行，实际 %d", count)
	}
}

func TestSearch_GuardsAndFilters(t *testing.T) {
	db, svc, nsID, _ := newAssetTestService(t)
	seedServer(t, db, nsID, "lobby-1", true)
	id := identityFor(nsID, "lobby-1")
	_, err := svc.ApplyManifest(ManifestReportParams{
		Identity: id, Mode: manifestModeFull, UploadID: "u1", Seq: 0, EOF: true,
		Upserts: []ManifestUpsert{
			upsert("plugins/A/config.yml", shaA, 10, 100),
			upsert("plugins/B/data.json", shaB, 20, 200),
			upsert("server.properties", shaC, 30, 300),
		},
	})
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	// namespaceId 缺失 → 400。
	if _, err := svc.Search(AssetSearchParams{}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 namespaceId 应 400，实际 %v", err)
	}
	// 裸 name（无索引条件）→ 400。
	if _, err := svc.Search(AssetSearchParams{NamespaceID: nsID, Name: "config"}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("裸 name 查询应 400，实际 %v", err)
	}
	// 整 namespace 搜索 → 3 行，serverId 回填业务串。
	res, err := svc.Search(AssetSearchParams{NamespaceID: nsID})
	if err != nil || res.Total != 3 || len(res.Items) != 3 {
		t.Fatalf("整 namespace 应 3 行，got total=%d len=%d err=%v", res.Total, len(res.Items), err)
	}
	if res.Items[0].ServerID != "lobby-1" {
		t.Fatalf("serverId 应回填业务串 lobby-1，实际 %q", res.Items[0].ServerID)
	}
	// 按扩展名过滤。
	res, _ = svc.Search(AssetSearchParams{NamespaceID: nsID, Ext: "yml"})
	if res.Total != 1 || res.Items[0].Path != "plugins/A/config.yml" {
		t.Fatalf("ext=yml 应命中 1 行 config.yml，got total=%d", res.Total)
	}
	// 路径前缀 + name 组合。
	res, _ = svc.Search(AssetSearchParams{NamespaceID: nsID, PathPrefix: "plugins/", Name: "data"})
	if res.Total != 1 || res.Items[0].Path != "plugins/B/data.json" {
		t.Fatalf("prefix+name 应命中 data.json，got total=%d", res.Total)
	}
	// 不存在的 serverId → 空结果（不报错）。
	res, err = svc.Search(AssetSearchParams{NamespaceID: nsID, ServerID: "ghost"})
	if err != nil || res.Total != 0 {
		t.Fatalf("不存在 serverId 应空结果，got total=%d err=%v", res.Total, err)
	}
}

func TestScanStatus(t *testing.T) {
	db, svc, nsID, _ := newAssetTestService(t)
	seedServer(t, db, nsID, "lobby-1", true)
	id := identityFor(nsID, "lobby-1")
	mustApply(t, svc, ManifestReportParams{
		Identity: id, Mode: manifestModeFull, UploadID: "u1", Seq: 0, EOF: true, Truncated: true, ScanDurationMs: 42,
		Upserts: []ManifestUpsert{upsert("plugins/A/a.yml", shaA, 10, 100)},
	})
	res, err := svc.ScanStatus(ScanStatusParams{NamespaceID: nsID})
	if err != nil || res.Total != 1 {
		t.Fatalf("概要应 1 行，got total=%d err=%v", res.Total, err)
	}
	item := res.Items[0]
	if item.ServerID != "lobby-1" || item.FileCount != 1 || !item.Truncated || item.ScanDurationMs != 42 {
		t.Fatalf("概要字段不符，实际 %+v", item)
	}
	if _, err := svc.ScanStatus(ScanStatusParams{}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 namespaceId 应 400，实际 %v", err)
	}
}

func TestCompare(t *testing.T) {
	db, svc, nsID, _ := newAssetTestService(t)
	seedServer(t, db, nsID, "s1", true)
	seedServer(t, db, nsID, "s2", true)
	seedServer(t, db, nsID, "s3", true) // s3 无此文件 → missing
	path := "plugins/A/config.yml"
	// s1 与 s2 同哈希（多数派），单独令 s2 与 s1 一致、s3 无。
	mustApply(t, svc, ManifestReportParams{Identity: identityFor(nsID, "s1"), Mode: manifestModeFull, UploadID: "a", Seq: 0, EOF: true,
		Upserts: []ManifestUpsert{upsert(path, shaA, 10, 100)}})
	mustApply(t, svc, ManifestReportParams{Identity: identityFor(nsID, "s2"), Mode: manifestModeFull, UploadID: "b", Seq: 0, EOF: true,
		Upserts: []ManifestUpsert{upsert(path, shaA, 10, 101)}})
	// s3 有别的文件、无 path。
	mustApply(t, svc, ManifestReportParams{Identity: identityFor(nsID, "s3"), Mode: manifestModeFull, UploadID: "c", Seq: 0, EOF: true,
		Upserts: []ManifestUpsert{upsert("plugins/A/other.yml", shaB, 5, 5)}})

	res, err := svc.Compare(CompareParams{NamespaceID: nsID, Path: path})
	if err != nil {
		t.Fatalf("比对失败: %v", err)
	}
	if len(res.Groups) != 1 || len(res.Groups[0].Servers) != 2 {
		t.Fatalf("应 1 组 2 成员，实际 groups=%d", len(res.Groups))
	}
	if len(res.Missing) != 1 || res.Missing[0] != "s3" {
		t.Fatalf("缺失服应为 [s3]，实际 %v", res.Missing)
	}
	// 缺参 → 400。
	if _, err := svc.Compare(CompareParams{NamespaceID: nsID}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 path 应 400，实际 %v", err)
	}
}

// TestRescan_OnlineOfflineSplit 在线 / 离线分流：只有运行态注册表内可用（online / degraded）的目标才建命令。
func TestRescan_OnlineOfflineSplit(t *testing.T) {
	db, svc, nsID, registry := newAssetTestService(t)
	seedServer(t, db, nsID, "online-1", true)
	seedServer(t, db, nsID, "offline-1", false)
	seedRuntimeInstance(t, registry, "prod", "online-1", runtime.StatusOnline)

	res, err := svc.Rescan(RescanParams{
		NamespaceID: nsID, ServerIDs: []string{"online-1", "offline-1"}, Force: true, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("应回 2 条结果，实际 %d", len(res.Results))
	}
	byID := map[string]RescanServerResult{}
	for _, r := range res.Results {
		byID[r.ServerID] = r
	}
	if byID["online-1"].Offline || byID["online-1"].CommandID == nil {
		t.Fatalf("online-1 应下发命令、非离线，实际 %+v", byID["online-1"])
	}
	if !byID["offline-1"].Offline || byID["offline-1"].CommandID != nil {
		t.Fatalf("offline-1 应标离线、无命令，实际 %+v", byID["offline-1"])
	}
	// 只对在线服建命令（1 条 asset-rescan，force=true）。
	var cmds []model.AgentCommand
	db.Find(&cmds)
	if len(cmds) != 1 || cmds[0].Type != model.CommandTypeAssetRescan || cmds[0].ServerID != "online-1" {
		t.Fatalf("应仅对 online-1 建 asset-rescan 命令，实际 %+v", cmds)
	}
	// 一条 asset.rescan 审计。
	var audits []model.AuditLog
	db.Where("action = ?", model.ActionAssetRescan).Find(&audits)
	if len(audits) != 1 {
		t.Fatalf("应记 1 条 asset.rescan 审计，实际 %d", len(audits))
	}
	// 目标数 / 上限守卫。
	if _, err := svc.Rescan(RescanParams{NamespaceID: nsID, ServerIDs: nil}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("空目标应 400，实际 %v", err)
	}
}

// TestRescan_OfflineWhenIdentityActiveButProcessGone 锁定重扫在线判定取运行时口径：
// agent_identity.status=active 只代表已接入，进程被杀后身份仍停在 active（旧实现据此判在线），
// 会为该服建 asset-rescan 命令并让 agent 干等超时——正是生产验收踩到的假阳性。
func TestRescan_OfflineWhenIdentityActiveButProcessGone(t *testing.T) {
	db, svc, nsID, registry := newAssetTestService(t)
	// 三台都有 active 身份：degraded 心跳陈旧仍算在线；lost 已超 TTL；死进程已不在注册表。
	seedServer(t, db, nsID, "degraded-1", true)
	seedServer(t, db, nsID, "lost-1", true)
	seedServer(t, db, nsID, "dead-1", true)
	seedRuntimeInstance(t, registry, "prod", "degraded-1", runtime.StatusDegraded)
	seedRuntimeInstance(t, registry, "prod", "lost-1", runtime.StatusLost)

	res, err := svc.Rescan(RescanParams{
		NamespaceID: nsID, ServerIDs: []string{"degraded-1", "lost-1", "dead-1"}, Force: true, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	byID := map[string]RescanServerResult{}
	for _, r := range res.Results {
		byID[r.ServerID] = r
	}
	if byID["degraded-1"].Offline || byID["degraded-1"].CommandID == nil {
		t.Fatalf("degraded 仍算在线、应下发命令，实际 %+v", byID["degraded-1"])
	}
	if !byID["lost-1"].Offline || byID["lost-1"].CommandID != nil {
		t.Fatalf("lost 应标离线、无命令，实际 %+v", byID["lost-1"])
	}
	if !byID["dead-1"].Offline || byID["dead-1"].CommandID != nil {
		t.Fatalf("身份 active 但注册表无实例应标离线（旧口径会误判在线），实际 %+v", byID["dead-1"])
	}
	// 只对 degraded-1 建命令：死进程 / lost 不得留下等不到回传的命令。
	var cmds []model.AgentCommand
	db.Find(&cmds)
	if len(cmds) != 1 || cmds[0].ServerID != "degraded-1" {
		t.Fatalf("应仅对 degraded-1 建 asset-rescan 命令，实际 %+v", cmds)
	}
}

// TestRescan_OfflineWhenNamespaceMismatch 注册表按 namespace code 分桶，而在线键用 namespace id：
// 同名 serverId 只在别的 namespace 在线时，本 namespace 的重扫必须判离线（防 code/id 混用导致串环境点亮）。
func TestRescan_OfflineWhenNamespaceMismatch(t *testing.T) {
	db, svc, nsID, registry := newAssetTestService(t)
	other := model.Namespace{Code: "other", Name: "其他"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("建第二个 namespace 失败: %v", err)
	}
	seedServer(t, db, nsID, "shared-1", true)
	if err := db.Create(&model.Server{NamespaceID: other.ID, ServerID: "shared-1", Kind: model.ServerKindBackend}).Error; err != nil {
		t.Fatalf("建另一 namespace 的 server 失败: %v", err)
	}
	seedRuntimeInstance(t, registry, other.Code, "shared-1", runtime.StatusOnline)

	res, err := svc.Rescan(RescanParams{NamespaceID: nsID, ServerIDs: []string{"shared-1"}, Force: true, Operator: "admin"})
	if err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	if len(res.Results) != 1 || !res.Results[0].Offline || res.Results[0].CommandID != nil {
		t.Fatalf("prod 的 shared-1 应判离线（在线的同名实例属 other），实际 %+v", res.Results)
	}
	// 反向：other namespace 的重扫应命中它自己的在线实例（证明 code→id 换算生效，未把 code 当 id）。
	res, err = svc.Rescan(RescanParams{NamespaceID: other.ID, ServerIDs: []string{"shared-1"}, Force: true, Operator: "admin"})
	if err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Offline || res.Results[0].CommandID == nil {
		t.Fatalf("other 的 shared-1 应下发命令，实际 %+v", res.Results)
	}
}

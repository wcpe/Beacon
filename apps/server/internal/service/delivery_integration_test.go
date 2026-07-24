//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// p9OnlineInstances 是文件资产安全通道在线校验的假实现（集成测试恒在线，命令回执由模拟 agent 驱动）。
type p9OnlineInstances struct{}

func (p9OnlineInstances) Get(_, _ string) (*runtime.Instance, error) { return &runtime.Instance{}, nil }

// p9Fixture 是交付编排 M1 集成测试基座（真实 MySQL）：拓扑五层 + 模板源 / 目标 + 双服务。
type p9Fixture struct {
	db      *gorm.DB
	orders  *service.DeliveryOrderService
	diff    *service.DeliveryDiffService
	preview *service.AssetPreviewService
	health  *healthview.Store
	ns      model.Namespace
	zone    model.Zone
	src     model.Server
	target  model.Server
}

// newP9Fixture 建独立测试库（beacon_p9delivery）、种拓扑与身份、装配双服务（设置服务用真实 store）。
func newP9Fixture(t *testing.T) *p9Fixture {
	t.Helper()
	db := testsupport.OpenTestDB(t, "p9delivery")
	f := &p9Fixture{db: db, health: healthview.NewStore()}

	f.ns = model.Namespace{Code: "p9", Name: "P9 交付"}
	mustCreate(t, db, &f.ns)
	cluster := model.BCCluster{NamespaceID: f.ns.ID, Name: "bc-p9"}
	mustCreate(t, db, &cluster)
	region := model.Region{BCClusterID: cluster.ID, Name: "region-p9"}
	mustCreate(t, db, &region)
	f.zone = model.Zone{RegionID: region.ID, Name: "zone-p9"}
	mustCreate(t, db, &f.zone)
	f.src = model.Server{NamespaceID: f.ns.ID, ServerID: "p9-src", Kind: model.ServerKindBackend, ZoneID: &f.zone.ID}
	f.target = model.Server{NamespaceID: f.ns.ID, ServerID: "p9-t1", Kind: model.ServerKindBackend, ZoneID: &f.zone.ID}
	mustCreate(t, db, &f.src)
	mustCreate(t, db, &f.target)
	for _, sid := range []string{"p9-src", "p9-t1"} {
		mustCreate(t, db, &model.AgentIdentity{
			IdentityID: "idn-" + sid, NamespaceID: f.ns.ID, ServerID: sid,
			Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC(),
		})
	}
	f.health.ReplaceAll([]healthview.View{
		{NamespaceID: f.ns.ID, ServerID: "p9-src", Kind: model.ServerKindBackend, Score: 95, Level: healthview.LevelHealthy},
		{NamespaceID: f.ns.ID, ServerID: "p9-t1", Kind: model.ServerKindBackend, Score: 92, Level: healthview.LevelHealthy},
	})

	auditRepo := repository.NewAuditLogRepository(db)
	settings, err := service.NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	orderRepo := repository.NewChangeOrderRepository(db)
	f.preview = service.NewAssetPreviewService(db, repository.NewAgentCommandRepository(db),
		repository.NewFileAssetRepository(db), repository.NewSettingRepository(db),
		auditRepo, longpoll.NewHub(), nil, p9OnlineInstances{})
	f.orders = service.NewDeliveryOrderService(db, orderRepo,
		repository.NewConfigLayerVersionRepository(db), auditRepo, settings, f.health)
	f.diff = service.NewDeliveryDiffService(db, orderRepo,
		repository.NewFileAssetRepository(db), auditRepo, f.preview, f.health)
	return f
}

// seedP9Asset 插一行文件资产快照。
func seedP9Asset(t *testing.T, f *p9Fixture, serverRow uint, path, sha string, size int64) {
	t.Helper()
	mustCreate(t, f.db, &model.FileAsset{
		NamespaceID: f.ns.ID, ServerID: serverRow, Path: path, Ext: "yml", SHA256: sha,
		Size: size, IsText: true, ScannedAt: time.Now().UTC(),
	})
}

// startP9AgentSim 后台模拟 agent：轮询 pending asset-read 命令 → CAS fetched → 回传固定文本内容。
func startP9AgentSim(t *testing.T, f *p9Fixture) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		cmdRepo := repository.NewAgentCommandRepository(f.db)
		for {
			select {
			case <-done:
				return
			default:
			}
			var cmd model.AgentCommand
			if err := f.db.Where("type = ? AND status = ?", model.CommandTypeAssetRead, model.CommandStatusPending).
				Order("id asc").First(&cmd).Error; err == nil {
				if ok, _ := cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); ok {
					var payload struct {
						Path string `json:"path"`
					}
					_ = json.Unmarshal([]byte(cmd.Payload), &payload)
					_ = f.preview.ReceiveContent(cmd.NamespaceCode, cmd.ServerID, cmd.ID,
						service.AssetContentPayload{Content: "P9内容:" + payload.Path})
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return func() { close(done) }
}

// TestP9DeliveryOrderFullChain 集成验证（真实 MySQL）组单 → diff-scan → 挂配置版本 → submit → approve 全链：
// 状态推进、差异项落库、from 锚点、审计按 orderId 可完整追溯（FR-162 验收 1/2/3 + FR-168 验收 25 的 M1 段）。
func TestP9DeliveryOrderFullChain(t *testing.T) {
	f := newP9Fixture(t)

	// 组单：模板源 + 扫描范围 + zone 筛选。
	title := "P9 集成链路单"
	src := "p9-src"
	scanDir := "plugins/"
	detail, err := f.orders.Create(f.ns.ID, service.ChangeOrderInput{
		Title: &title, SourceServerID: &src, ScanDir: &scanDir,
		Selector: &service.ChangeSelector{Zones: []uint{f.zone.ID}},
	}, "ops-chen", "10.0.0.9")
	if err != nil {
		t.Fatalf("组单失败: %v", err)
	}
	orderID := detail.ID

	// 源 / 目标快照：a 覆盖、b 新增、gone 删除。
	mustCreate(t, f.db, &model.FileAssetScan{NamespaceID: f.ns.ID, ServerID: f.src.ID,
		ManifestDigest: "d", FileCount: 2, ScannedAt: time.Now().UTC()})
	seedP9Asset(t, f, f.src.ID, "plugins/a.yml", "sha-new", 10)
	seedP9Asset(t, f, f.src.ID, "plugins/b.yml", "sha-b", 20)
	seedP9Asset(t, f, f.target.ID, "plugins/a.yml", "sha-old", 9)
	seedP9Asset(t, f, f.target.ID, "plugins/gone.yml", "sha-g", 5)

	scan, err := f.diff.DiffScan(orderID, "ops-chen", "10.0.0.9")
	if err != nil {
		t.Fatalf("diff-scan 失败: %v", err)
	}
	actions := map[string]string{}
	for _, item := range scan.Items {
		actions[*item.Path] = *item.Action
	}
	if actions["plugins/a.yml"] != model.ChangeItemActionUpdate ||
		actions["plugins/b.yml"] != model.ChangeItemActionAdd ||
		actions["plugins/gone.yml"] != model.ChangeItemActionDelete || len(actions) != 3 {
		t.Fatalf("差异动作不符: %v", actions)
	}

	// 挂配置版本（链首版 → from 锚点 null）。
	file := model.ConfigFile{NamespaceID: f.ns.ID, Name: "plugins/Foo/config.yml", Format: "yaml"}
	mustCreate(t, f.db, &file)
	v1 := model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone,
		ScopeRefID: f.zone.ID, VersionNo: 1, Content: "a: 1"}
	mustCreate(t, f.db, &v1)
	detail, err = f.orders.Update(orderID, service.ChangeOrderInput{
		ConfigChanges: &[]service.ChangeConfigInput{
			{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone.ID, ConfigToVersionID: v1.ID},
		},
	}, "ops-chen", "10.0.0.9")
	if err != nil {
		t.Fatalf("挂配置版本失败: %v", err)
	}
	if len(detail.Items) != 4 {
		t.Fatalf("应 3 文件项 + 1 配置项，实际 %d", len(detail.Items))
	}

	// 影响预览：目标 1 台、文件 3、配置作用域 1。
	impact, err := f.diff.Impact(orderID, 1, 20)
	if err != nil {
		t.Fatalf("影响预览失败: %v", err)
	}
	if impact.Summary.TargetTotal != 1 || impact.Summary.FileTotal != 3 || impact.Summary.ConfigScopeCount != 1 {
		t.Fatalf("影响汇总不符: %+v", impact.Summary)
	}
	if len(impact.Targets.Items) != 1 || impact.Targets.Items[0].ServerID != "p9-t1" ||
		!impact.Targets.Items[0].Online || len(impact.Targets.Items[0].ConfigScopes) != 1 {
		t.Fatalf("逐目标行不符: %+v", impact.Targets.Items)
	}

	// submit → pending_approval；创建人自批被分离拒绝；他人审批通过。
	if detail, err = f.orders.Submit(orderID, "ops-chen", "10.0.0.9"); err != nil ||
		detail.Status != model.ChangeOrderStatusPendingApproval {
		t.Fatalf("提交失败: %v / %+v", err, detail)
	}
	if _, err := f.orders.Approve(orderID, "", "ops-chen", "10.0.0.9"); err == nil {
		t.Fatal("创建人自批应被审批分离拒绝")
	}
	if detail, err = f.orders.Approve(orderID, "影响面已确认", "admin", "10.0.0.9"); err != nil ||
		detail.Status != model.ChangeOrderStatusApproved {
		t.Fatalf("审批失败: %v / %+v", err, detail)
	}

	// 审计贯通：按 orderId 过滤得完整链路（create / update×2 / submit / approve）。
	var audits []model.AuditLog
	if err := f.db.Where("target_type = ? AND target_ref = ?",
		model.TargetTypeChangeOrder, strconv.FormatUint(uint64(orderID), 10)).
		Order("id asc").Find(&audits).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	byAction := map[string]int{}
	for _, entry := range audits {
		byAction[entry.Action]++
		if entry.Detail == "" || entry.NamespaceCode != "p9" {
			t.Fatalf("审计 detail / namespace 缺失: %+v", entry)
		}
	}
	if byAction[model.ActionDeliveryOrderCreate] != 1 || byAction[model.ActionDeliveryOrderSubmit] != 1 ||
		byAction[model.ActionDeliveryOrderApprove] != 1 || byAction[model.ActionDeliveryOrderUpdate] != 2 {
		t.Fatalf("审计链路不完整: %v", byAction)
	}

	// 事件派生与当前状态对齐。
	events, err := f.orders.Events(orderID)
	if err != nil || events.Events[len(events.Events)-1].Status != model.ChangeOrderStatusApproved {
		t.Fatalf("事件末条应 approved: %v / %+v", err, events)
	}
}

// TestP9DeliveryFileDiffContract 集成验证（真实 MySQL + 模拟 agent 回执）file-diff 正式契约：
// update 项 after=源内容 / before=目标内容 / serverId 回填；内容经文件资产安全通道并记 asset.preview 审计。
func TestP9DeliveryFileDiffContract(t *testing.T) {
	f := newP9Fixture(t)
	title := "P9 file-diff 契约单"
	src := "p9-src"
	scanDir := "plugins/"
	detail, err := f.orders.Create(f.ns.ID, service.ChangeOrderInput{
		Title: &title, SourceServerID: &src, ScanDir: &scanDir,
		Selector: &service.ChangeSelector{Zones: []uint{f.zone.ID}},
	}, "ops-chen", "")
	if err != nil {
		t.Fatalf("组单失败: %v", err)
	}
	mustCreate(t, f.db, &model.FileAssetScan{NamespaceID: f.ns.ID, ServerID: f.src.ID,
		ManifestDigest: "d", FileCount: 1, ScannedAt: time.Now().UTC()})
	seedP9Asset(t, f, f.src.ID, "plugins/upd.yml", "sha-new", 10)
	seedP9Asset(t, f, f.target.ID, "plugins/upd.yml", "sha-old", 11)
	scan, err := f.diff.DiffScan(detail.ID, "ops-chen", "")
	if err != nil || len(scan.Items) != 1 {
		t.Fatalf("diff-scan 失败: %v / %+v", err, scan)
	}

	stop := startP9AgentSim(t, f)
	defer stop()
	view, err := f.diff.FileDiff(context.Background(), detail.ID, scan.Items[0].ID, "", "", "admin", "10.0.0.9")
	if err != nil {
		t.Fatalf("file-diff 失败: %v", err)
	}
	if view.Path != "plugins/upd.yml" || view.ChangeType != "modified" || view.Binary || view.Truncated {
		t.Fatalf("file-diff 形态不符: %+v", view)
	}
	if view.After == nil || *view.After != "P9内容:plugins/upd.yml" ||
		view.Before == nil || *view.Before != "P9内容:plugins/upd.yml" {
		t.Fatalf("file-diff 双侧内容不符: %+v", view)
	}
	if view.ServerID == nil || *view.ServerID != "p9-t1" {
		t.Fatalf("file-diff serverId 应回填 p9-t1: %+v", view)
	}
	var previews int64
	if err := f.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionAssetPreview).
		Count(&previews).Error; err != nil || previews != 2 {
		t.Fatalf("应记 2 条 asset.preview 审计（源 + 目标），实际 %d err=%v", previews, err)
	}
}

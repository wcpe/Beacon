package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// seedDeliveryAsset 插一行文件资产（业务事实：某服某路径的最新快照）。
func seedDeliveryAsset(t *testing.T, env *deliveryTestEnv, nsID, serverRow uint, path, sha string, size int64, isText bool) {
	t.Helper()
	mustCreate(t, env.db, &model.FileAsset{
		NamespaceID: nsID, ServerID: serverRow, Path: path, Ext: "yml", SHA256: sha,
		Size: size, IsText: isText, ScannedAt: time.Now().UTC(),
	})
}

// seedDeliveryScan 插某服扫描概要行（diff-scan 的快照时间锚点）。
func seedDeliveryScan(t *testing.T, env *deliveryTestEnv, nsID, serverRow uint, at time.Time) {
	t.Helper()
	mustCreate(t, env.db, &model.FileAssetScan{
		NamespaceID: nsID, ServerID: serverRow, ManifestDigest: "d", FileCount: 1, ScannedAt: at,
	})
}

// —— selector 解析（spec §4.3.1）——

// TestResolveChangeTargetsSelectorSemantics all / zones / regions / servers / excludes 语义与合格性过滤。
func TestResolveChangeTargetsSelectorSemantics(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	// 追加不合格候选：proxy / 未分配 zone / 身份 pending / 身份 disabled。
	seedDeliveryServer(t, env.db, f.nsID, "proxy-1", model.ServerKindProxy, nil, model.AgentIdentityStatusActive)
	seedDeliveryServer(t, env.db, f.nsID, "unassigned-1", model.ServerKindBackend, nil, model.AgentIdentityStatusActive)
	seedDeliveryServer(t, env.db, f.nsID, "pending-1", model.ServerKindBackend, &f.zone1ID, model.AgentIdentityStatusPending)
	seedDeliveryServer(t, env.db, f.nsID, "disabled-1", model.ServerKindBackend, &f.zone1ID, model.AgentIdentityStatusDisabled)

	ids := func(selector ChangeSelector, source string) []string {
		t.Helper()
		targets, err := resolveChangeTargets(env.db, f.nsID, selector, source)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		out := make([]string, 0, len(targets))
		for _, srv := range targets {
			out = append(out, srv.ServerID)
		}
		return out
	}

	// all=true：全部合格 backend（src-1/t-1/t-2），减模板源 → t-1,t-2（字典序）；不合格候选全被滤除。
	if got := ids(ChangeSelector{All: true}, "src-1"); strings.Join(got, ",") != "t-1,t-2" {
		t.Fatalf("all 解析应 t-1,t-2，实际 %v", got)
	}
	// zones：仅 zone1 → src-1,t-1（无源排除）。
	if got := ids(ChangeSelector{Zones: []uint{f.zone1ID}}, ""); strings.Join(got, ",") != "src-1,t-1" {
		t.Fatalf("zone1 解析应 src-1,t-1，实际 %v", got)
	}
	// regions：展开大区下全部小区 → 三台。
	if got := ids(ChangeSelector{Regions: []uint{f.regionID}}, ""); strings.Join(got, ",") != "src-1,t-1,t-2" {
		t.Fatalf("region 解析应含三台，实际 %v", got)
	}
	// servers 点名 ∪ zones，再减 excludes。
	if got := ids(ChangeSelector{Zones: []uint{f.zone2ID}, Servers: []string{"t-1"}, Excludes: []string{"t-2"}}, ""); strings.Join(got, ",") != "t-1" {
		t.Fatalf("并集减 excludes 应只剩 t-1，实际 %v", got)
	}
	// 点名不合格候选：被合格性过滤静默滤除（不报错）。
	if got := ids(ChangeSelector{Servers: []string{"proxy-1", "unassigned-1", "pending-1", "disabled-1", "t-1"}}, ""); strings.Join(got, ",") != "t-1" {
		t.Fatalf("不合格候选应被滤除，实际 %v", got)
	}
}

// TestResolveChangeTargetsCrossNamespaceRejected 引用异 namespace / 不存在实体直接校验失败（FR-162）。
func TestResolveChangeTargetsCrossNamespaceRejected(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	// 另一 namespace 的拓扑与服。
	otherNS := seedDeliveryNamespace(t, env.db, "test")
	otherCluster := model.BCCluster{NamespaceID: otherNS, Name: "bc-x"}
	mustCreate(t, env.db, &otherCluster)
	otherRegion := model.Region{BCClusterID: otherCluster.ID, Name: "region-x"}
	mustCreate(t, env.db, &otherRegion)
	otherZone := model.Zone{RegionID: otherRegion.ID, Name: "zone-x"}
	mustCreate(t, env.db, &otherZone)
	seedDeliveryServer(t, env.db, otherNS, "other-1", model.ServerKindBackend, &otherZone.ID, model.AgentIdentityStatusActive)

	cases := []ChangeSelector{
		{Zones: []uint{otherZone.ID}},       // 异 ns 小区
		{Regions: []uint{otherRegion.ID}},   // 异 ns 大区
		{Servers: []string{"other-1"}},      // 异 ns 服务器
		{Excludes: []string{"other-1"}},     // 异 ns excludes 同样拒绝
		{Zones: []uint{99999}},              // 不存在小区
		{Servers: []string{"ghost-server"}}, // 不存在服务器
	}
	for i, selector := range cases {
		_, err := resolveChangeTargets(env.db, f.nsID, selector, "")
		_ = mustAppErr(t, err, "selector_cross_namespace", http.StatusBadRequest)
		if err == nil {
			t.Fatalf("用例 %d 应拒绝", i)
		}
	}
}

// —— 差异算法（spec §4.2 + 已拍板决策 2）——

// TestDiffScanUnionSemantics 差异并集语义：add / update（胜 add）/ delete / 相同跳过 / 前缀过滤 / 排序稳定。
func TestDiffScanUnionSemantics(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID) // 既有配置项须在重扫后保留

	snapAt := time.Now().UTC().Truncate(time.Second)
	seedDeliveryScan(t, env, f.nsID, f.srcRow, snapAt)
	// 源清单（plugins/ 内 4 个 + scanDir 外 1 个）。
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/a.yml", "sha-a", 10, true)  // t1 有同 hash、t2 缺 → add
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/b.yml", "sha-b", 20, true)  // t1 异 hash、t2 缺 → update（胜 add）
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/c.yml", "sha-c", 30, true)  // 双方同 hash → 跳过
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/d.yml", "sha-d", 40, true)  // 双方均缺 → add
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "config/out.yml", "sha-x", 50, true) // scanDir 外 → 不参与
	// 目标清单。
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/a.yml", "sha-a", 10, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/b.yml", "sha-b-old", 22, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/c.yml", "sha-c", 30, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/legacy.yml", "sha-l", 5, true) // 源无 → delete
	seedDeliveryAsset(t, env, f.nsID, f.t2Row, "plugins/c.yml", "sha-c", 30, true)
	seedDeliveryAsset(t, env, f.nsID, f.t2Row, "config/out.yml", "sha-y", 60, true) // scanDir 外 → 不参与 delete

	view, err := env.diff.DiffScan(order.ID, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("diff-scan 失败: %v", err)
	}
	if view.Status != "done" || view.DiffSnapshotAt == nil || !view.DiffSnapshotAt.Equal(snapAt) {
		t.Fatalf("快照时间应取源扫描时间 %v，实际 %+v", snapAt, view)
	}
	got := map[string]string{}
	for _, item := range view.Items {
		if item.Kind != model.ChangeItemKindFileDiff || item.Path == nil || item.Action == nil {
			t.Fatalf("差异项形状不符: %+v", item)
		}
		got[*item.Path] = *item.Action
	}
	want := map[string]string{
		"plugins/a.yml":      model.ChangeItemActionAdd,
		"plugins/b.yml":      model.ChangeItemActionUpdate,
		"plugins/d.yml":      model.ChangeItemActionAdd,
		"plugins/legacy.yml": model.ChangeItemActionDelete,
	}
	if len(got) != len(want) {
		t.Fatalf("差异项应 %v，实际 %v", want, got)
	}
	for path, action := range want {
		if got[path] != action {
			t.Fatalf("路径 %s 应 %s，实际 %s", path, action, got[path])
		}
	}
	// delete 项无源侧 hash / 大小；add/update 项带源侧事实。
	for _, item := range view.Items {
		if *item.Action == model.ChangeItemActionDelete {
			if item.SHA256 != nil || item.SizeBytes != nil {
				t.Fatalf("delete 项 sha/size 应为 null: %+v", item)
			}
		} else if item.SHA256 == nil || item.SizeBytes == nil {
			t.Fatalf("add/update 项应带源侧 sha/size: %+v", item)
		}
	}

	// 配置项保留 + 落库 items = 4 文件 + 1 配置；重扫审计入账。
	detail, err := env.orders.Get(order.ID)
	if err != nil || len(detail.Items) != 5 {
		t.Fatalf("单内应 5 项（4 文件 + 1 配置）: %v / %d", err, len(detail.Items))
	}
	if !auditDetailContains(t, env.db, model.ActionDeliveryOrderUpdate, `"diffScan":true`) {
		t.Fatal("重扫应记 delivery.order.update 审计（diffScan 标记）")
	}

	// 重复扫描幂等替换（不叠加）。
	if _, err := env.diff.DiffScan(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	detail, _ = env.orders.Get(order.ID)
	if len(detail.Items) != 5 {
		t.Fatalf("重扫应整组替换而非叠加，实际 %d 项", len(detail.Items))
	}
}

// TestDiffScanSelectorUnsetFallback 向导「先扫差异、后定范围」步序：selector 未设时
// 对照集回退为 namespace 内全部合格目标（真机首验暴露的恒 0 项缺陷回归）；显式空选取不受影响。
func TestDiffScanSelectorUnsetFallback(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	// 创建时不带 selector（向导第 2 步扫差异时范围尚未选择）。
	order, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title:          strPtr("先扫后选范围"),
		SourceServerID: strPtr("src-1"),
		ScanDir:        strPtr("plugins/"),
	}, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("创建无 selector draft 失败: %v", err)
	}

	snapAt := time.Now().UTC().Truncate(time.Second)
	seedDeliveryScan(t, env, f.nsID, f.srcRow, snapAt)
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/new.yml", "sha-new", 10, true) // 目标均缺 → add
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/old.yml", "sha-old", 5, true)   // 源无 → delete

	view, err := env.diff.DiffScan(order.ID, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("selector 未设时 diff-scan 应回退全合格目标集: %v", err)
	}
	got := map[string]string{}
	for _, item := range view.Items {
		got[*item.Path] = *item.Action
	}
	if got["plugins/new.yml"] != model.ChangeItemActionAdd || got["plugins/old.yml"] != model.ChangeItemActionDelete {
		t.Fatalf("未设 selector 应对全合格目标算出差异，实际 %v", got)
	}

	// 对照：显式设 selector 时仍严格按所设范围（不回退）。
	sel := ChangeSelector{Servers: []string{"t-2"}}
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{Selector: &sel}, "ops-chen", ""); err != nil {
		t.Fatalf("挂 selector 失败: %v", err)
	}
	view, err = env.diff.DiffScan(order.ID, "ops-chen", "")
	if err != nil {
		t.Fatalf("显式 selector 重扫失败: %v", err)
	}
	for _, item := range view.Items {
		if *item.Path == "plugins/old.yml" {
			t.Fatal("显式 selector 只含 t-2 时不应再出现 t-1 独有的 delete 项")
		}
	}
}

// TestDiffScanGuards 差异扫描守卫：无源 / 源无快照 / 非 draft。
func TestDiffScanGuards(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)

	noSource, err := env.orders.Create(f.nsID, ChangeOrderInput{Title: strPtr("纯配置单")}, "ops", "")
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	_ = mustAppErr(t, mustErr(env.diff.DiffScan(noSource.ID, "ops", "")), "missing_source", http.StatusBadRequest)

	// 源从未上报快照 → source_snapshot_missing（防误判「源为空」生成全删差异）。
	order := createDraftOrder(t, f)
	_ = mustAppErr(t, mustErr(env.diff.DiffScan(order.ID, "ops", "")), "source_snapshot_missing", http.StatusConflict)

	// 显式 selector 解析出 0 目标（只点名模板源自身，源被自动排除）：有快照时返回空差异
	// （并集语义的自然结果；selector 未设的回退语义见 TestDiffScanSelectorUnsetFallback）。
	seedDeliveryScan(t, env, f.nsID, f.srcRow, time.Now().UTC())
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/a.yml", "sha-a", 10, true)
	onlySource := ChangeSelector{Servers: []string{"src-1"}}
	emptySel, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title: strPtr("空目标"), SourceServerID: strPtr("src-1"), ScanDir: strPtr("plugins/"),
		Selector: &onlySource,
	}, "ops", "")
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	view, err := env.diff.DiffScan(emptySel.ID, "ops", "")
	if err != nil || len(view.Items) != 0 {
		t.Fatalf("显式 selector 解析 0 目标应产出空差异: %v / %+v", err, view)
	}
}

// —— 影响预览（spec §4.2.2）——

// TestPlanImpactBatches percent 向上取整 + 末批兜底；count 固定台数 + 剩余进末批；可复现。
func TestPlanImpactBatches(t *testing.T) {
	percent := planImpactBatches(model.BatchModePercent, []int{5, 20, 75}, 10)
	if len(percent) != 3 || percent[0].Count != 1 || percent[1].Count != 2 || percent[2].Count != 7 {
		t.Fatalf("percent [5,20,75]×10 应 [1,2,7]，实际 %+v", percent)
	}
	short := planImpactBatches(model.BatchModePercent, []int{10}, 10)
	if len(short) != 2 || short[0].Count != 1 || short[1].Count != 9 {
		t.Fatalf("百分比不足 100 应自动补剩余末批，实际 %+v", short)
	}
	count := planImpactBatches(model.BatchModeCount, []int{1, 10}, 5)
	if len(count) != 2 || count[0].Count != 1 || count[1].Count != 4 {
		t.Fatalf("count [1,10]×5 应 [1,4]，实际 %+v", count)
	}
	if got := planImpactBatches(model.BatchModeCount, []int{3}, 0); len(got) != 0 {
		t.Fatalf("0 目标应无批次，实际 %+v", got)
	}
}

// TestImpactSummaryAndTargets 汇总（去重传输字节 / 批次预览 / 快照时间）+ 逐目标现算计数 + 配置命中 + 在线健康。
func TestImpactSummaryAndTargets(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)

	// 配置文件挂 zone1 作用域版本（t-1 命中、t-2 不命中）。
	file := model.ConfigFile{NamespaceID: f.nsID, Name: "plugins/Foo/config.yml", Format: "yaml"}
	mustCreate(t, env.db, &file)
	v1 := model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: f.zone1ID, VersionNo: 1, Content: "a: 1"}
	mustCreate(t, env.db, &v1)
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{
		{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone1ID, ConfigToVersionID: v1.ID},
	}}, "ops", ""); err != nil {
		t.Fatalf("挂配置版本失败: %v", err)
	}

	// 源两文件（同内容同 sha → 传输去重）+ 目标差异；t-2 离线（不在健康视图）。
	snapAt := time.Now().UTC().Truncate(time.Second)
	seedDeliveryScan(t, env, f.nsID, f.srcRow, snapAt)
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/a.yml", "sha-same", 100, true)
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/b.yml", "sha-same", 100, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/a.yml", "sha-old", 90, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/stale.yml", "sha-s", 5, true)
	markDeliveryOnline(env.health, f.nsID, "src-1", "t-1")
	if _, err := env.diff.DiffScan(order.ID, "ops", ""); err != nil {
		t.Fatalf("diff-scan 失败: %v", err)
	}

	view, err := env.diff.Impact(order.ID, 1, 20)
	if err != nil {
		t.Fatalf("impact 失败: %v", err)
	}
	assertImpactSummary(t, view.Summary, snapAt)
	assertImpactTargets(t, f, view, v1.ID)
}

// assertImpactSummary 断言影响预览汇总：目标数 / 文件数 / 字节 / 去重传输字节 / 配置作用域 / 快照时间 / 批次预览。
func assertImpactSummary(t *testing.T, s ChangeImpactSummaryView, snapAt time.Time) {
	t.Helper()
	// 3 文件项（a update、b add、stale delete）+ 1 配置项；totalBytes=200、transferBytes 去重=100。
	if s.TargetTotal != 2 || s.FileTotal != 3 || s.TotalBytes != 200 || s.TransferBytes != 100 || s.ConfigScopeCount != 1 {
		t.Fatalf("汇总不符: %+v", s)
	}
	if s.SnapshotAt == nil || !s.SnapshotAt.Equal(snapAt) {
		t.Fatalf("快照时间不符: %+v", s.SnapshotAt)
	}
	if len(s.Batches) == 0 || s.Batches[0].BatchNo != 1 {
		t.Fatalf("批次预览缺失: %+v", s.Batches)
	}
}

// assertImpactTargets 断言逐目标行：字典序、现算四计数、在线 / 健康、配置作用域命中。
func assertImpactTargets(t *testing.T, f *deliveryFixture, view *ChangeImpactView, toVersionID uint) {
	t.Helper()
	if view.Targets.Total != 2 || len(view.Targets.Items) != 2 {
		t.Fatalf("逐目标分页不符: %+v", view.Targets)
	}
	t1, t2 := view.Targets.Items[0], view.Targets.Items[1]
	if t1.ServerID != "t-1" || t2.ServerID != "t-2" {
		t.Fatalf("目标应按字典序: %+v", view.Targets.Items)
	}
	// t-1：a 覆盖、b 新增、stale 删除；在线 healthy；命中 zone1 配置作用域（from null → to v1）。
	if t1.UpdateCount != 1 || t1.AddCount != 1 || t1.DeleteCount != 1 || t1.SkipCount != 0 || !t1.Online || t1.Level != "healthy" {
		t.Fatalf("t-1 行不符: %+v", t1)
	}
	if len(t1.ConfigScopes) != 1 || t1.ConfigScopes[0].ScopeKind != model.ConfigScopeZone ||
		t1.ConfigScopes[0].ScopeID != f.zone1ID || t1.ConfigScopes[0].ToVersionID == nil || *t1.ConfigScopes[0].ToVersionID != toVersionID {
		t.Fatalf("t-1 配置命中不符: %+v", t1.ConfigScopes)
	}
	// t-2：空清单 → a/b 均新增；离线 unknown；zone2 不命中配置作用域。
	if t2.AddCount != 2 || t2.UpdateCount != 0 || t2.DeleteCount != 0 || t2.Online || t2.Level != "unknown" {
		t.Fatalf("t-2 行不符: %+v", t2)
	}
	if len(t2.ConfigScopes) != 0 {
		t.Fatalf("t-2 不应命中配置作用域: %+v", t2.ConfigScopes)
	}
}

// —— 变更项文件内容预览（file-diff 正式契约）——

// seedFileDiffOrder 组一单三种差异项（update/add/delete）并铺双端内容资产。
func seedFileDiffOrder(t *testing.T, env *deliveryTestEnv, f *deliveryFixture) *ChangeOrderDetailView {
	t.Helper()
	order := createDraftOrder(t, f)
	seedDeliveryScan(t, env, f.nsID, f.srcRow, time.Now().UTC())
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/upd.yml", "sha-new", 10, true)
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/new.yml", "sha-add", 12, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/upd.yml", "sha-old", 11, true)
	seedDeliveryAsset(t, env, f.nsID, f.t1Row, "plugins/gone.yml", "sha-del", 9, true)
	if _, err := env.diff.DiffScan(order.ID, "ops", ""); err != nil {
		t.Fatalf("diff-scan 失败: %v", err)
	}
	detail, err := env.orders.Get(order.ID)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	return detail
}

// findItemByPath 按路径取变更项 id。
func findItemByPath(t *testing.T, detail *ChangeOrderDetailView, path string) uint {
	t.Helper()
	for _, item := range detail.Items {
		if item.Path != nil && *item.Path == path {
			return item.ID
		}
	}
	t.Fatalf("未找到差异项 %s", path)
	return 0
}

// TestFileDiffContract update / add / delete 三形态契约：before/after 语义、serverId 回填、审计走文件资产域。
func TestFileDiffContract(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	detail := seedFileDiffOrder(t, env, f)
	stop := startAgentSim(t, env.db, env.preview, textReply)
	defer stop()
	ctx := context.Background()

	// update：after=源内容、before=目标内容、serverId=t-1（首个差异目标）。
	upd, err := env.diff.FileDiff(ctx, detail.ID, findItemByPath(t, detail, "plugins/upd.yml"), "", "", "admin", "")
	if err != nil {
		t.Fatalf("update file-diff 失败: %v", err)
	}
	if upd.ChangeType != "modified" || upd.Binary || upd.Truncated {
		t.Fatalf("update 形态不符: %+v", upd)
	}
	if upd.After == nil || *upd.After != "内容:plugins/upd.yml" || upd.Before == nil || *upd.Before != "内容:plugins/upd.yml" {
		t.Fatalf("update 双侧内容不符: %+v", upd)
	}
	if upd.ServerID == nil || *upd.ServerID != "t-1" {
		t.Fatalf("update serverId 应回填 t-1，实际 %v", upd.ServerID)
	}

	// add：before=null、after=源内容；差异目标 = 首个缺该文件的目标（t-1 也缺 → t-1）。
	add, err := env.diff.FileDiff(ctx, detail.ID, findItemByPath(t, detail, "plugins/new.yml"), "", "", "admin", "")
	if err != nil {
		t.Fatalf("add file-diff 失败: %v", err)
	}
	if add.ChangeType != "added" || add.Before != nil || add.After == nil || *add.After != "内容:plugins/new.yml" {
		t.Fatalf("add 形态不符: %+v", add)
	}

	// delete：after=null、before=目标内容、serverId=仍持有文件的 t-1。
	del, err := env.diff.FileDiff(ctx, detail.ID, findItemByPath(t, detail, "plugins/gone.yml"), "", "", "admin", "")
	if err != nil {
		t.Fatalf("delete file-diff 失败: %v", err)
	}
	if del.ChangeType != "removed" || del.After != nil || del.Before == nil || *del.Before != "内容:plugins/gone.yml" {
		t.Fatalf("delete 形态不符: %+v", del)
	}
	if del.ServerID == nil || *del.ServerID != "t-1" {
		t.Fatalf("delete serverId 应回填 t-1，实际 %v", del.ServerID)
	}

	// 内容查看审计走文件资产域（asset.preview），detail 绝不含文件内容。
	if got := countAudit(t, env.db, model.ActionAssetPreview); got == 0 {
		t.Fatal("file-diff 取内容应记 asset.preview 审计")
	}
	if auditDetailContains(t, env.db, model.ActionAssetPreview, "内容:") {
		t.Fatal("审计 detail 绝不应含文件内容")
	}

	// 显式 serverId：必须在目标集内。
	if _, err := env.diff.FileDiff(ctx, detail.ID, findItemByPath(t, detail, "plugins/upd.yml"), "src-1", "", "admin", ""); err == nil {
		t.Fatal("目标集外 serverId 应拒绝")
	}
	explicit, err := env.diff.FileDiff(ctx, detail.ID, findItemByPath(t, detail, "plugins/upd.yml"), "t-2", "", "admin", "")
	if err != nil {
		t.Fatalf("显式 t-2 应放行: %v", err)
	}
	// t-2 无该文件 → before=null（目标缺文件的真实事实）。
	if explicit.ServerID == nil || *explicit.ServerID != "t-2" || explicit.Before != nil {
		t.Fatalf("显式 t-2 形态不符: %+v", explicit)
	}
}

// TestFileDiffBinaryAndSensitive 二进制项不取内容（前后皆 null、无命令下发）；敏感路径无 reason 403。
func TestFileDiffBinaryAndSensitive(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedDeliveryScan(t, env, f.nsID, f.srcRow, time.Now().UTC())
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/Foo.jar", "sha-jar", 1024, false)   // 二进制
	seedDeliveryAsset(t, env, f.nsID, f.srcRow, "plugins/db-secret.yml", "sha-sec", 8, true) // 命中默认敏感规则
	if _, err := env.diff.DiffScan(order.ID, "ops", ""); err != nil {
		t.Fatalf("diff-scan 失败: %v", err)
	}
	detail, err := env.orders.Get(order.ID)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}

	binary, err := env.diff.FileDiff(context.Background(), order.ID, findItemByPath(t, detail, "plugins/Foo.jar"), "", "", "admin", "")
	if err != nil {
		t.Fatalf("二进制 file-diff 失败: %v", err)
	}
	if !binary.Binary || binary.Before != nil || binary.After != nil || binary.Truncated {
		t.Fatalf("二进制形态不符: %+v", binary)
	}
	if cmds := allCommands(t, env.db); len(cmds) != 0 {
		t.Fatalf("二进制项不应下发 asset-read 命令，实际 %d 条", len(cmds))
	}

	// 敏感路径（默认规则含 *secret*）无 reason → 403 asset_sensitive_path。
	_, err = env.diff.FileDiff(context.Background(), order.ID, findItemByPath(t, detail, "plugins/db-secret.yml"), "", "", "admin", "")
	_ = mustAppErr(t, err, "asset_sensitive_path", http.StatusForbidden)
}

// TestFileDiffItemGuards 非 file_diff 项 / 不存在项 → item_not_found；单不存在 → change_order_not_found。
func TestFileDiffItemGuards(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)
	detail, err := env.orders.Get(order.ID)
	if err != nil || len(detail.Items) != 1 {
		t.Fatalf("铺数据失败: %v", err)
	}

	ctx := context.Background()
	_, err = env.diff.FileDiff(ctx, order.ID, detail.Items[0].ID, "", "", "admin", "")
	_ = mustAppErr(t, err, "item_not_found", http.StatusNotFound)
	_, err = env.diff.FileDiff(ctx, order.ID, 99999, "", "", "admin", "")
	_ = mustAppErr(t, err, "item_not_found", http.StatusNotFound)
	_, err = env.diff.FileDiff(ctx, 99999, 1, "", "", "admin", "")
	_ = mustAppErr(t, err, "change_order_not_found", http.StatusNotFound)
}

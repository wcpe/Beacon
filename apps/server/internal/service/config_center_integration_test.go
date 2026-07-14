//go:build integration

package service_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// p7cfgFixture 是配置中心 V2 集成测试的五层实体基座。
type p7cfgFixture struct {
	db             *gorm.DB
	svc            *service.ConfigCenterService
	nsA, nsB       model.Namespace
	cluster        model.BCCluster
	region         model.Region
	zone           model.Zone
	serverAssigned model.Server // 已分配 zone 的子服（完整五层链）
	serverFree     model.Server // 未分配 zone 的子服（只有 namespace + server 两层）
	serverB        model.Server // 另一 namespace 的子服（跨 ns 拒绝用）
}

// newP7cfgFixture 建独立测试库（beacon_p7cfg）并种五层实体。
func newP7cfgFixture(t *testing.T) *p7cfgFixture {
	t.Helper()
	db := testsupport.OpenTestDB(t, "p7cfg")
	f := &p7cfgFixture{db: db}
	f.svc = service.NewConfigCenterService(db,
		repository.NewConfigFileRepository(db), repository.NewConfigLayerVersionRepository(db),
		repository.NewAuditLogRepository(db))

	f.nsA = model.Namespace{Code: "p7a", Name: "P7 主环境"}
	f.nsB = model.Namespace{Code: "p7b", Name: "P7 隔离环境"}
	mustCreate(t, db, &f.nsA)
	mustCreate(t, db, &f.nsB)
	f.cluster = model.BCCluster{NamespaceID: f.nsA.ID, Name: "bc-main"}
	mustCreate(t, db, &f.cluster)
	f.region = model.Region{BCClusterID: f.cluster.ID, Name: "region-east"}
	mustCreate(t, db, &f.region)
	f.zone = model.Zone{RegionID: f.region.ID, Name: "zone-1"}
	mustCreate(t, db, &f.zone)
	f.serverAssigned = model.Server{NamespaceID: f.nsA.ID, ServerID: "lobby-1", Kind: model.ServerKindBackend, ZoneID: &f.zone.ID}
	mustCreate(t, db, &f.serverAssigned)
	f.serverFree = model.Server{NamespaceID: f.nsA.ID, ServerID: "free-1", Kind: model.ServerKindBackend}
	mustCreate(t, db, &f.serverFree)
	f.serverB = model.Server{NamespaceID: f.nsB.ID, ServerID: "b-1", Kind: model.ServerKindBackend}
	mustCreate(t, db, &f.serverB)
	return f
}

// mustCreate 插入实体，失败即 Fatal。
func mustCreate(t *testing.T, db *gorm.DB, v any) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("种子实体失败: %v", err)
	}
}

// createFile 建配置文件，失败即 Fatal。
func (f *p7cfgFixture) createFile(t *testing.T, req service.CreateConfigFileRequest) *service.ConfigFileView {
	t.Helper()
	view, err := f.svc.CreateFile(req, "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("建配置文件失败: %v", err)
	}
	return view
}

// save 保存新版本，失败即 Fatal。
func (f *p7cfgFixture) save(t *testing.T, fileID uint, level string, refID uint, content string, basedOn *uint) *service.ConfigSaveResultView {
	t.Helper()
	res, err := f.svc.SaveVersion(fileID, service.SaveVersionRequest{
		ScopeLevel: level, ScopeRefID: refID, Content: content, BasedOnVersionID: basedOn,
	}, "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("保存 %s/%d 版本失败: %v", level, refID, err)
	}
	return res
}

// wantCode 断言错误业务码。
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("应报 %s，得到 %v", code, err)
	}
}

// yamlValue 从 yaml 文本取顶层键值。
func yamlValue(t *testing.T, content, key string) any {
	t.Helper()
	parsed, err := merge.Parse(merge.FormatYAML, content)
	if err != nil {
		t.Fatalf("解析有效内容失败: %v", err)
	}
	m, _ := parsed.(map[string]any)
	return m[key]
}

// TestP7cfgFiveLayerOverrideAndProvenance 验收 §7-1/3：五层低→高覆盖顺序、未分配 zone 只受两层影响、
// 逐键来源解释与删键列表逐项正确。
func TestP7cfgFiveLayerOverrideAndProvenance(t *testing.T) {
	f := newP7cfgFixture(t)
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/It/config.yml", Format: merge.FormatYAML,
	})
	// 每层设 winner=<层名> + 各自独有键；server 层用 null 删掉 namespace 的 doomed 键
	f.save(t, file.ID, model.ConfigScopeNamespace, f.nsA.ID, "winner: ns\nfromNs: 1\ndoomed: bye", nil)
	f.save(t, file.ID, model.ConfigScopeBCCluster, f.cluster.ID, "winner: bc\nfromBc: 1", nil)
	f.save(t, file.ID, model.ConfigScopeRegion, f.region.ID, "winner: region\nfromRegion: 1", nil)
	f.save(t, file.ID, model.ConfigScopeZone, f.zone.ID, "winner: zone\nfromZone: 1", nil)
	f.save(t, file.ID, model.ConfigScopeServer, f.serverAssigned.ID, "winner: server\nfromServer: 1\ndoomed: null", nil)

	// 已分配 server：五层齐参与，高层覆盖低层
	view, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{ServerRef: f.serverAssigned.ServerID})
	if err != nil {
		t.Fatalf("有效解析失败: %v", err)
	}
	if got := yamlValue(t, view.EffectiveContent, "winner"); got != "server" {
		t.Fatalf("五层覆盖顺序错误：winner = %v，期望 server", got)
	}
	for _, key := range []string{"fromNs", "fromBc", "fromRegion", "fromZone", "fromServer"} {
		if yamlValue(t, view.EffectiveContent, key) == nil {
			t.Fatalf("有效结果缺各层独有键 %s", key)
		}
	}
	if yamlValue(t, view.EffectiveContent, "doomed") != nil {
		t.Fatal("server 层 null 删键未生效")
	}
	if len(view.Layers) != 5 {
		t.Fatalf("层摘要应 5 层，实际 %d", len(view.Layers))
	}
	if merge.Sha256Hex(view.EffectiveContent) != view.EffectiveHash {
		t.Fatal("无敏感路径时 effectiveHash 应等于内容 sha256")
	}
	assertProvenance(t, view, f)

	// 未分配 zone 的 server：只受 namespace + server 两层影响
	freeView, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{ServerRef: f.serverFree.ServerID})
	if err != nil {
		t.Fatalf("未分配 server 有效解析失败: %v", err)
	}
	if got := yamlValue(t, freeView.EffectiveContent, "winner"); got != "ns" {
		t.Fatalf("未分配 zone 的 server 不应吃到中间层，winner = %v", got)
	}
	if yamlValue(t, freeView.EffectiveContent, "fromZone") != nil {
		t.Fatal("未分配 zone 的 server 吃到了 zone 层键")
	}
	if len(freeView.Layers) != 2 {
		t.Fatalf("未分配 server 层摘要应 2 层，实际 %d", len(freeView.Layers))
	}

	// 假想目标：zone 粒度预览到 zone 层为止
	zoneView, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{ZoneID: f.zone.ID})
	if err != nil {
		t.Fatalf("zone 假想目标解析失败: %v", err)
	}
	if got := yamlValue(t, zoneView.EffectiveContent, "winner"); got != "zone" {
		t.Fatalf("zone 假想目标 winner = %v，期望 zone", got)
	}
}

// assertProvenance 断言逐键来源与删键列表（验收 §7-3）。
func assertProvenance(t *testing.T, view *service.ConfigEffectiveView, f *p7cfgFixture) {
	t.Helper()
	byPath := map[string]service.ConfigProvenanceEntryView{}
	for _, entry := range view.Provenance {
		byPath[entry.Path] = entry
	}
	winner := byPath["winner"]
	if winner.ScopeLevel != model.ConfigScopeServer || winner.ScopeRefID != f.serverAssigned.ID ||
		winner.ScopeName != f.serverAssigned.ServerID || winner.VersionNo != 1 {
		t.Fatalf("winner 来源解释错误: %+v", winner)
	}
	if byPath["fromZone"].ScopeLevel != model.ConfigScopeZone || byPath["fromZone"].ScopeName != f.zone.Name {
		t.Fatalf("fromZone 来源解释错误: %+v", byPath["fromZone"])
	}
	if byPath["fromNs"].ScopeLevel != model.ConfigScopeNamespace {
		t.Fatalf("fromNs 来源解释错误: %+v", byPath["fromNs"])
	}
	if len(view.DeletedKeys) != 1 || view.DeletedKeys[0].Path != "doomed" ||
		view.DeletedKeys[0].ScopeLevel != model.ConfigScopeServer {
		t.Fatalf("删键列表错误: %+v", view.DeletedKeys)
	}
}

// TestP7cfgCrossNamespaceRejected 验收 §7-4：跨 namespace 写入 / 解析一律 CONFIG_SCOPE_MISMATCH。
func TestP7cfgCrossNamespaceRejected(t *testing.T) {
	f := newP7cfgFixture(t)
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/Iso/config.yml", Format: merge.FormatYAML,
	})
	// 向 A 的文件写 B 的 server scope
	_, err := f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: model.ConfigScopeServer, ScopeRefID: f.serverB.ID, Content: "a: 1",
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_SCOPE_MISMATCH")
	// 向 A 的文件写 B 的 namespace scope
	_, err = f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: f.nsB.ID, Content: "a: 1",
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_SCOPE_MISMATCH")
	// 以 B 的 server 做有效解析
	_, err = f.svc.Effective(file.ID, service.ConfigEffectiveTarget{ServerRef: f.serverB.ServerID})
	wantCode(t, err, "CONFIG_SCOPE_MISMATCH")
	// B 的文件用 A 的 zone scope（zone 归属链最终落在 A）
	fileB := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsB.ID, Name: "plugins/Iso/config.yml", Format: merge.FormatYAML,
	})
	_, err = f.svc.SaveVersion(fileB.ID, service.SaveVersionRequest{
		ScopeLevel: model.ConfigScopeZone, ScopeRefID: f.zone.ID, Content: "a: 1",
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_SCOPE_MISMATCH")
}

// TestP7cfgVersionChainRollbackRevoke 验收 §7-7：版本只增不改、回退生成新版本、撤销后不参与合并、
// rollback 可恢复、并发保存旧基线 409。
func TestP7cfgVersionChainRollbackRevoke(t *testing.T) {
	f := newP7cfgFixture(t)
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/Chain/config.yml", Format: merge.FormatYAML,
	})
	ns := model.ConfigScopeNamespace
	v1 := f.save(t, file.ID, ns, f.nsA.ID, "a: 1", nil)
	v2 := f.save(t, file.ID, ns, f.nsA.ID, "a: 2", &v1.VersionID)

	// 并发旧基线：basedOn 仍传 v1 → 409
	_, err := f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: ns, ScopeRefID: f.nsA.ID, Content: "a: 3", BasedOnVersionID: &v1.VersionID,
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_VERSION_CONFLICT")
	// 内容与 head 相同 → 无变化拒绝
	_, err = f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: ns, ScopeRefID: f.nsA.ID, Content: "a: 2", BasedOnVersionID: &v2.VersionID,
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_NO_CHANGE")

	// 回退到 v1：生成 v3，内容等于 v1、based_on 指向 v1
	v3, err := f.svc.RollbackVersion(v1.VersionID, "", "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("回退失败: %v", err)
	}
	if v3.VersionNo != 3 || v3.ContentHash != v1.ContentHash {
		t.Fatalf("回退版本号 / hash 错误: %+v", v3)
	}
	detail, err := f.svc.GetVersion(v3.VersionID)
	if err != nil {
		t.Fatalf("取回退版本失败: %v", err)
	}
	if detail.BasedOnVersionID == nil || *detail.BasedOnVersionID != v1.VersionID {
		t.Fatal("回退版本 based_on 未指向来源历史版本")
	}
	if !strings.Contains(detail.Remark, "回退自 v1") {
		t.Fatalf("回退备注应自动注明来源: %q", detail.Remark)
	}
	// 再次回退 v1（内容与 head 相同）→ 无变化拒绝
	_, err = f.svc.RollbackVersion(v1.VersionID, "", "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_NO_CHANGE")

	// 撤销层贡献 → v4 removal，层不再参与合并
	revoke, err := f.svc.RemoveScopeContribution(file.ID, ns, f.nsA.ID, "维护窗口撤掉基线", "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("撤销层失败: %v", err)
	}
	if !revoke.IsRemoval || revoke.VersionNo != 4 {
		t.Fatalf("撤销版本形状错误: %+v", revoke)
	}
	view, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{})
	if err != nil {
		t.Fatalf("撤销后有效解析失败: %v", err)
	}
	if view.EffectiveContent != "" {
		t.Fatalf("撤销后该层不应再贡献，实际内容 %q", view.EffectiveContent)
	}
	// head 已撤销再撤 → 400
	_, err = f.svc.RemoveScopeContribution(file.ID, ns, f.nsA.ID, "再撤一次", "it-admin", "127.0.0.1")
	wantCode(t, err, "INVALID_PARAM")

	// rollback 恢复贡献：回退 v2 → v5，effective 恢复 a: 2
	v5, err := f.svc.RollbackVersion(v2.VersionID, "恢复基线", "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("撤销后回退恢复失败: %v", err)
	}
	if v5.VersionNo != 5 {
		t.Fatalf("恢复版本号错误: %+v", v5)
	}
	view, err = f.svc.Effective(file.ID, service.ConfigEffectiveTarget{})
	if err != nil {
		t.Fatalf("恢复后有效解析失败: %v", err)
	}
	if got := yamlValue(t, view.EffectiveContent, "a"); got != 2 {
		t.Fatalf("恢复后 a = %v，期望 2", got)
	}

	// 不可变：历史 v1 行原样未动
	v1Detail, err := f.svc.GetVersion(v1.VersionID)
	if err != nil {
		t.Fatalf("取 v1 失败: %v", err)
	}
	if v1Detail.VersionNo != 1 || v1Detail.ContentHash != v1.ContentHash {
		t.Fatal("历史版本被改动，违反不可变链")
	}
	// 版本列表新 → 旧
	list, err := f.svc.ListVersions(file.ID, ns, f.nsA.ID, 1, 10)
	if err != nil {
		t.Fatalf("列版本失败: %v", err)
	}
	if list.Total != 5 || list.Items[0].VersionNo != 5 || list.Items[4].VersionNo != 1 {
		t.Fatalf("版本列表排序 / 总数错误: total=%d", list.Total)
	}

	// P9 接缝：进程内明文渲染按 scope→versionId 覆盖参与合并
	pinned, _, err := f.svc.EffectivePlaintext(file.ID, service.ConfigEffectiveTarget{},
		[]service.ConfigScopePin{{ScopeLevel: ns, ScopeRefID: f.nsA.ID, VersionID: v1.VersionID}})
	if err != nil {
		t.Fatalf("pin 渲染失败: %v", err)
	}
	if got := yamlValue(t, pinned, "a"); got != 1 {
		t.Fatalf("pin v1 渲染 a = %v，期望 1", got)
	}
}

// TestP7cfgSensitiveMaskingFullChain 验收 §7-8：版本详情 / 有效预览 / diff / 审计 detail 全链脱敏、
// 占位符回填 hash 一致、服务端日志搜不到哨兵明文。
func TestP7cfgSensitiveMaskingFullChain(t *testing.T) {
	const sentinel = "P7-SENTINEL-4f8a2c"
	var logBuf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(restore)

	f := newP7cfgFixture(t)
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/Vault/config.yml", Format: merge.FormatYAML,
		SensitivePaths: []string{"database.password"},
	})
	ns := model.ConfigScopeNamespace
	v1 := f.save(t, file.ID, ns, f.nsA.ID, "database:\n  password: "+sentinel+"\nport: 3306", nil)

	// 版本详情脱敏
	detail, err := f.svc.GetVersion(v1.VersionID)
	if err != nil {
		t.Fatalf("取版本失败: %v", err)
	}
	if strings.Contains(detail.Content, sentinel) || !strings.Contains(detail.Content, service.ConfigMaskedPlaceholder) {
		t.Fatalf("版本详情未脱敏: %s", detail.Content)
	}
	// 有效预览脱敏 + hash 基于明文（单层 merge 归一化不变 → 等于 head contentHash）
	view, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{})
	if err != nil {
		t.Fatalf("有效解析失败: %v", err)
	}
	if strings.Contains(view.EffectiveContent, sentinel) {
		t.Fatal("有效预览泄露明文")
	}
	if view.EffectiveHash != v1.ContentHash {
		t.Fatalf("effectiveHash 应基于脱敏前明文（= head contentHash）：%s vs %s", view.EffectiveHash, v1.ContentHash)
	}
	// 占位符回填：v2 改 port、密码提交占位符
	v2 := f.save(t, file.ID, ns, f.nsA.ID,
		"database:\n  password: "+service.ConfigMaskedPlaceholder+"\nport: 3307", &v1.VersionID)
	// 直接提交明文同内容 → CONFIG_NO_CHANGE，证明回填后与明文 hash 一致
	_, err = f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: ns, ScopeRefID: f.nsA.ID,
		Content: "database:\n  password: " + sentinel + "\nport: 3307", BasedOnVersionID: &v2.VersionID,
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_NO_CHANGE")
	// diff 输出脱敏
	diff, err := f.svc.Diff(file.ID, "version:"+uintStr(v1.VersionID), "version:"+uintStr(v2.VersionID))
	if err != nil {
		t.Fatalf("diff 失败: %v", err)
	}
	if strings.Contains(diff.UnifiedDiff, sentinel) {
		t.Fatal("diff 输出泄露明文")
	}
	// 新文件首版就交占位符 → 无可回填
	empty := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/Vault2/config.yml", Format: merge.FormatYAML,
		SensitivePaths: []string{"token"},
	})
	_, err = f.svc.SaveVersion(empty.ID, service.SaveVersionRequest{
		ScopeLevel: ns, ScopeRefID: f.nsA.ID, Content: "token: " + service.ConfigMaskedPlaceholder,
	}, "it-admin", "127.0.0.1")
	wantCode(t, err, "CONFIG_SENSITIVE_PLACEHOLDER_INVALID")

	// 审计 detail 全程无明文（键路径级摘要）
	var audits []model.AuditLog
	if err := f.db.Find(&audits).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	for _, entry := range audits {
		if strings.Contains(entry.Detail, sentinel) {
			t.Fatalf("审计 detail 泄露明文: %s", entry.Detail)
		}
	}
	// 服务端日志全文搜不到哨兵明文
	if strings.Contains(logBuf.String(), sentinel) {
		t.Fatal("服务端日志泄露敏感明文")
	}
}

// uintStr 把 uint 转十进制字符串（diff 描述符拼接用）。
func uintStr(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

// TestP7cfgTrashLifecycle 验收 §7-9：回收站闭环——软删屏蔽、恢复完整、重名 409、purge 连带删链 + 审计摘要。
func TestP7cfgTrashLifecycle(t *testing.T) {
	f := newP7cfgFixture(t)
	const name = "plugins/Trash/config.yml"
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: name, Format: merge.FormatYAML,
	})
	f.save(t, file.ID, model.ConfigScopeNamespace, f.nsA.ID, "a: 1", nil)

	if err := f.svc.TrashFile(file.ID, "下线", "it-admin", "127.0.0.1"); err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	// 常规列表不出现
	list, err := f.svc.ListFiles(service.ConfigFileListQuery{NamespaceID: f.nsA.ID})
	if err != nil {
		t.Fatalf("列文件失败: %v", err)
	}
	for _, item := range list.Items {
		if item.ID == file.ID {
			t.Fatal("软删文件仍出现在常规列表")
		}
	}
	// 其余端点一律 404
	if _, err := f.svc.GetFileDetail(file.ID); !errors.Is(err, apperr.ErrConfigFileNotFound) {
		t.Fatalf("软删后详情应 404: %v", err)
	}
	if _, err := f.svc.Effective(file.ID, service.ConfigEffectiveTarget{}); !errors.Is(err, apperr.ErrConfigFileNotFound) {
		t.Fatalf("软删后 effective 应 404: %v", err)
	}
	if _, _, err := f.svc.EffectivePlaintext(file.ID, service.ConfigEffectiveTarget{}, nil); !errors.Is(err, apperr.ErrConfigFileNotFound) {
		t.Fatalf("软删后进程内明文渲染同样拒绝: %v", err)
	}
	if _, err := f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: f.nsA.ID, Content: "a: 2",
	}, "it-admin", "127.0.0.1"); !errors.Is(err, apperr.ErrConfigFileNotFound) {
		t.Fatalf("软删后保存应 404: %v", err)
	}
	// 版本链数据保留
	var versionCount int64
	f.db.Model(&model.ConfigLayerVersion{}).Where("config_file_id = ?", file.ID).Count(&versionCount)
	if versionCount != 1 {
		t.Fatalf("软删后版本链应保留，实际 %d 行", versionCount)
	}
	// 同名可先删后建；此时恢复被占用 → 409
	replacement := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: name, Format: merge.FormatYAML,
	})
	if _, err := f.svc.RestoreFile(file.ID, "it-admin", "127.0.0.1"); err == nil {
		t.Fatal("名称被占用时恢复应 409")
	} else {
		wantCode(t, err, "CONFIG_FILE_DUPLICATE")
	}
	// 占用者入回收站后恢复成功，覆盖链完整如初、可继续保存
	if err := f.svc.TrashFile(replacement.ID, "", "it-admin", "127.0.0.1"); err != nil {
		t.Fatalf("软删占用者失败: %v", err)
	}
	restored, err := f.svc.RestoreFile(file.ID, "it-admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if restored.DeletedAt != nil || restored.DeletedBy != nil {
		t.Fatal("恢复后软删标记未清空")
	}
	versions, err := f.svc.ListVersions(file.ID, model.ConfigScopeNamespace, f.nsA.ID, 1, 10)
	if err != nil || versions.Total != 1 {
		t.Fatalf("恢复后版本历史应完整: err=%v total=%d", err, versions.Total)
	}
	head := versions.Items[0].VersionID
	f.save(t, file.ID, model.ConfigScopeNamespace, f.nsA.ID, "a: 2", &head)

	// purge：未软删 → 400；软删后原因必填；执行后物理删除连带版本链
	if err := f.svc.PurgeFile(file.ID, "清理", "it-admin", "127.0.0.1"); err == nil {
		t.Fatal("未软删 purge 应 400")
	} else {
		wantCode(t, err, "CONFIG_FILE_NOT_TRASHED")
	}
	if err := f.svc.TrashFile(file.ID, "", "it-admin", "127.0.0.1"); err != nil {
		t.Fatalf("再次软删失败: %v", err)
	}
	if err := f.svc.PurgeFile(file.ID, "  ", "it-admin", "127.0.0.1"); err == nil {
		t.Fatal("purge 原因必填")
	} else {
		wantCode(t, err, "missing_reason")
	}
	if err := f.svc.PurgeFile(file.ID, "彻底清理旧插件配置", "it-admin", "127.0.0.1"); err != nil {
		t.Fatalf("purge 失败: %v", err)
	}
	var fileCount int64
	f.db.Model(&model.ConfigFile{}).Where("id = ?", file.ID).Count(&fileCount)
	f.db.Model(&model.ConfigLayerVersion{}).Where("config_file_id = ?", file.ID).Count(&versionCount)
	if fileCount != 0 || versionCount != 0 {
		t.Fatalf("purge 后应物理删除连带版本链: file=%d versions=%d", fileCount, versionCount)
	}
	// purge 审计摘要可追溯（文件名 + 各链最终版本号与 hash）
	var purgeAudit model.AuditLog
	if err := f.db.Where("action = ?", model.ActionConfigFilePurge).First(&purgeAudit).Error; err != nil {
		t.Fatalf("缺 purge 审计: %v", err)
	}
	if !strings.Contains(purgeAudit.TargetRef, name) || !strings.Contains(purgeAudit.Detail, "finalVersionNo") {
		t.Fatalf("purge 审计摘要不可追溯: ref=%s detail=%s", purgeAudit.TargetRef, purgeAudit.Detail)
	}
}

// TestP7cfgSchemaGateAndValidateParity 验收 §7-5：schema 违例保存被 400 阻断且不落库、
// validate 与保存校验结果一致、required 只在 namespace 层强制。
func TestP7cfgSchemaGateAndValidateParity(t *testing.T) {
	f := newP7cfgFixture(t)
	schema := `{"type":"object","required":["host"],"properties":{"host":{"type":"string"},"port":{"type":"integer","minimum":1}}}`
	file := f.createFile(t, service.CreateConfigFileRequest{
		NamespaceID: f.nsA.ID, Name: "plugins/Schema/config.yml", Format: merge.FormatYAML, SchemaJSON: schema,
	})
	ns := model.ConfigScopeNamespace
	bad := "host: db.local\nport: zero"

	// 保存违例被阻断且不落库
	_, err := f.svc.SaveVersion(file.ID, service.SaveVersionRequest{
		ScopeLevel: ns, ScopeRefID: f.nsA.ID, Content: bad,
	}, "it-admin", "127.0.0.1")
	var sve *service.ConfigSchemaViolationError
	if !errors.As(err, &sve) || len(sve.Violations) == 0 {
		t.Fatalf("schema 违例应带逐条 {path,message}: %v", err)
	}
	var count int64
	f.db.Model(&model.ConfigLayerVersion{}).Where("config_file_id = ?", file.ID).Count(&count)
	if count != 0 {
		t.Fatal("schema 违例内容不应落库")
	}
	// validate 与保存同引擎同结果
	validate, err := f.svc.ValidateContent(file.ID, ns, bad)
	if err != nil {
		t.Fatalf("validate 失败: %v", err)
	}
	if validate.Valid || len(validate.Errors) != len(sve.Violations) ||
		validate.Errors[0].Path != sve.Violations[0].Path {
		t.Fatalf("validate 与保存校验结果不一致: %+v vs %+v", validate.Errors, sve.Violations)
	}
	// required 只在 namespace 基线层强制：zone 层缺 host 放行
	zoneValidate, err := f.svc.ValidateContent(file.ID, model.ConfigScopeZone, "port: 8080")
	if err != nil || !zoneValidate.Valid {
		t.Fatalf("非基线层不应强制 required: err=%v errors=%+v", err, zoneValidate.Errors)
	}
	// namespace 基线层缺 host 违例
	nsValidate, err := f.svc.ValidateContent(file.ID, ns, "port: 8080")
	if err != nil || nsValidate.Valid {
		t.Fatal("基线层缺 required 应违例")
	}
}

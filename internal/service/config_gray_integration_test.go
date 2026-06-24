//go:build integration

package service_test

import (
	"errors"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/internal/service"
)

// grayStack 装配灰度集成测试栈：配置 / 灰度 / 有效配置（含灰度叠加）三件套 + 唤醒器。
// 返回 hub 供断言"唤醒只命中受影响 server"。
func grayStack(t *testing.T) (*service.ConfigService, *service.ConfigGrayService, *service.EffectiveService, *longpoll.Hub, *gorm.DB) {
	db := testDB(t)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	rr := repository.NewConfigRevisionRepository(db, noEncryptCipher())
	gr := repository.NewConfigGrayRepository(db, noEncryptCipher())
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	topoHub := longpoll.NewHub()
	cfg := service.NewConfigService(db, cr, rr, ar)
	gray := service.NewConfigGrayService(db, cfg, cr, gr, ar)
	eff := service.NewEffectiveService(cr, asg, gr, nil, hub)
	notifier := service.NewChangeNotifier(hub, fileHub, topoHub, longpoll.NewHub(), runtime.NewRegistry(), asg)
	cfg.SetNotifier(notifier)
	gray.SetNotifier(notifier)
	return cfg, gray, eff, hub, db
}

// mkGlobalItem 在 global 层建一个 yaml 配置项（稳定内容），返回 item。
func mkGlobalItem(t *testing.T, cfg *service.ConfigService, dataID, content string) *model.ConfigItem {
	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: dataID,
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML,
		Content: content, Operator: "alice", Comment: "首次",
	})
	if err != nil {
		t.Fatalf("建配置项失败: %v", err)
	}
	return item
}

// resolveDataID 解析某 serverId 对某 dataId 的有效内容（找不到返回空）。
func resolveDataID(t *testing.T, eff *service.EffectiveService, serverID, dataID string) string {
	res, err := eff.Resolve("prod", serverID, model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("解析有效配置失败 server=%s: %v", serverID, err)
	}
	for _, it := range res.Items {
		if it.DataID == dataID {
			return it.Content
		}
	}
	return ""
}

// TestGrayCohortInOutResolution：cohort 内解析到灰度内容、名单外解析到稳定内容，
// 且名单外结果逐字节等于无灰度时（灰度对名单外透明）。
func TestGrayCohortInOutResolution(t *testing.T) {
	cfg, gray, eff, _, _ := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")

	// 无灰度时 s1/s2 都拿稳定内容
	if got := resolveDataID(t, eff, "s1", "app.yml"); got != "v: stable\n" {
		t.Fatalf("无灰度 s1 应得稳定内容，实际 %q", got)
	}

	// 对 s1 发布灰度
	if _, err := gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}

	if got := resolveDataID(t, eff, "s1", "app.yml"); got != "v: gray\n" {
		t.Fatalf("cohort 内 s1 应解析到灰度内容，实际 %q", got)
	}
	if got := resolveDataID(t, eff, "s2", "app.yml"); got != "v: stable\n" {
		t.Fatalf("名单外 s2 应解析到稳定内容，实际 %q", got)
	}
	// 名单外 md5 应与稳定 md5 一致（逐字节等于无灰度）
	s2res, _ := eff.Resolve("prod", "s2", model.GlobalGroupCode)
	if s2res.Items[0].MD5 != merge.MD5Hex("v: stable\n") {
		t.Fatalf("名单外 md5 应等于稳定 md5，实际 %s", s2res.Items[0].MD5)
	}
}

// TestGrayPromote：promote 后 cohort 内外都解析到（原灰度）新稳定内容，
// 灰度清空、version+1、历史多一条。
func TestGrayPromote(t *testing.T) {
	cfg, gray, eff, _, _ := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")
	if _, err := gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}

	promoted, err := gray.Promote(item.ID, "bob", "晋升", "")
	if err != nil {
		t.Fatalf("晋升失败: %v", err)
	}
	if promoted.Version != 2 {
		t.Fatalf("晋升后版本应为 2，实际 %d", promoted.Version)
	}
	if promoted.Content != "v: gray\n" {
		t.Fatalf("晋升后稳定内容应为灰度内容，实际 %q", promoted.Content)
	}

	// cohort 内外都解析到新稳定内容
	if got := resolveDataID(t, eff, "s1", "app.yml"); got != "v: gray\n" {
		t.Fatalf("晋升后 s1 应得新稳定内容，实际 %q", got)
	}
	if got := resolveDataID(t, eff, "s2", "app.yml"); got != "v: gray\n" {
		t.Fatalf("晋升后 s2 应得新稳定内容，实际 %q", got)
	}

	// 灰度清空：再 promote 应 GRAY_NOT_FOUND
	if _, err := gray.Promote(item.ID, "bob", "再晋升", ""); !errors.Is(err, apperr.ErrGrayNotFound) {
		t.Fatalf("灰度应已清空，再晋升应得 GRAY_NOT_FOUND，实际 %v", err)
	}

	// 历史多一条（首发 v1 + 晋升 v2）
	revs, err := cfg.ListRevisions(item.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("历史应有 2 版，实际 %d err=%v", len(revs), err)
	}
}

// TestGrayAbort：abort 后 cohort 成员回到稳定内容，稳定 version 不变，灰度清空。
func TestGrayAbort(t *testing.T) {
	cfg, gray, eff, _, _ := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")
	if _, err := gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}
	// 确认灰度生效
	if got := resolveDataID(t, eff, "s1", "app.yml"); got != "v: gray\n" {
		t.Fatalf("abort 前 s1 应得灰度内容，实际 %q", got)
	}

	if err := gray.Abort(item.ID, "bob", "中止", ""); err != nil {
		t.Fatalf("中止失败: %v", err)
	}

	// s1 回到稳定内容
	if got := resolveDataID(t, eff, "s1", "app.yml"); got != "v: stable\n" {
		t.Fatalf("abort 后 s1 应回到稳定内容，实际 %q", got)
	}
	// 稳定 version 不变
	cur, _ := cfg.Get(item.ID)
	if cur.Version != 1 {
		t.Fatalf("abort 不应改稳定 version，实际 %d", cur.Version)
	}
	// 灰度清空：再 abort 应 GRAY_NOT_FOUND
	if err := gray.Abort(item.ID, "bob", "再中止", ""); !errors.Is(err, apperr.ErrGrayNotFound) {
		t.Fatalf("灰度应已清空，再中止应得 GRAY_NOT_FOUND，实际 %v", err)
	}
}

// TestGrayContentValidation：灰度内容 md5 = 灰度内容 md5；非法内容（schema 不过）被拒；空 cohort 被拒。
func TestGrayContentValidation(t *testing.T) {
	cfg, gray, _, _, db := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")

	g, err := gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", "")
	if err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}
	if g.ContentMD5 != merge.MD5Hex("v: gray\n") {
		t.Fatalf("灰度 md5 应等于灰度内容 md5，实际 %s", g.ContentMD5)
	}

	// 非法 yaml 被拒
	if _, err := gray.Publish(item.ID, "v: : : bad\n", []string{"s1"}, "alice", "坏内容", ""); err == nil {
		t.Fatalf("非法内容应被拒")
	}
	// 空 cohort 被拒
	if _, err := gray.Publish(item.ID, "v: ok\n", []string{"  ", ""}, "alice", "空名单", ""); !errors.Is(err, apperr.ErrEmptyCohort) {
		t.Fatalf("空 cohort 应得 EMPTY_COHORT，实际 %v", err)
	}

	// 库中始终至多一个活跃灰度（重发覆盖）
	if _, err := gray.Publish(item.ID, "v: gray2\n", []string{"s9"}, "alice", "重发", ""); err != nil {
		t.Fatalf("重发灰度失败: %v", err)
	}
	var active int64
	db.Model(&model.ConfigGray{}).Where("config_item_id = ? AND deleted_at = ?", item.ID, model.SoftDeleteSentinel).Count(&active)
	if active != 1 {
		t.Fatalf("同 item 活跃灰度应恰为 1，实际 %d", active)
	}
}

// TestGrayWakeOnlyCohort：灰度发布只唤醒 cohort 内 serverId，名单外不被唤醒。
func TestGrayWakeOnlyCohort(t *testing.T) {
	cfg, gray, _, hub, _ := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")

	// 注册两个 waiter：s1（cohort 内）与 s2（名单外）
	w1 := hub.Register("prod", "s1")
	defer hub.Deregister(w1)
	w2 := hub.Register("prod", "s2")
	defer hub.Deregister(w2)

	if _, err := gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "灰度", ""); err != nil {
		t.Fatalf("发布灰度失败: %v", err)
	}

	if !notified(w1) {
		t.Fatalf("cohort 内 s1 的 waiter 应被唤醒")
	}
	if notified(w2) {
		t.Fatalf("名单外 s2 的 waiter 不应被唤醒")
	}
}

// TestGrayPublishConcurrentLastWins：并发对同一 item 发布灰度（重发即覆盖语义）。
// 灰度发布以 config_item.gray_version 乐观锁 CAS 串行化「先软删后建」段，从源头消除
// uk_gray_item 上的死锁——各路重读重试后都应成功、无错误透出，且恰留一条活跃灰度
// （uk_gray_item 保证无重复无残留）。
func TestGrayPublishConcurrentLastWins(t *testing.T) {
	cfg, gray, _, _, _ := grayStack(t)
	item := mkGlobalItem(t, cfg, "app.yml", "v: stable\n")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = gray.Publish(item.ID, "v: gray\n", []string{"s1"}, "alice", "并发灰度", "")
		}(i)
	}
	wg.Wait()

	// 重发即覆盖：瞬时冲突由重试吸收，各路并发发布最终都成功（无错误透出）
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发发布第 %d 路应成功（重试后后写覆盖），实际 %v", i, err)
		}
	}
	// 并发结束后恰有一条该 item 的活跃灰度（后写覆盖，uk_gray_item 保证无重复无残留）
	active, err := gray.List(item.NamespaceCode)
	if err != nil {
		t.Fatalf("查询活跃灰度失败: %v", err)
	}
	count := 0
	for _, g := range active {
		if g.ConfigItemID == item.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重发即覆盖后应恰有一条活跃灰度，实际 %d", count)
	}
	// 每路成功发布恰做一次 CAS +1（失败 CAS 不自增）：并发后 gray_version 应等于成功次数 n，
	// 证明乐观锁真把并发串行化、无丢失更新。
	final, err := cfg.Get(item.ID)
	if err != nil {
		t.Fatalf("查询 item 失败: %v", err)
	}
	if final.GrayVersion != int64(n) {
		t.Fatalf("gray_version 应等于成功发布次数 %d（每路恰一次 CAS+1），实际 %d", n, final.GrayVersion)
	}
}

// notified 非阻塞探测 waiter 是否已收到唤醒信号（缓冲为 1，发布后即可读）。
func notified(w *longpoll.Waiter) bool {
	select {
	case <-w.NotifyChan():
		return true
	default:
		return false
	}
}

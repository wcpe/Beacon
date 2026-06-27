package service

import (
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/secret"
)

// undoTestCipher 返回一把未启用的 cipher（撤回测试不涉敏感项加密，保持明文行为）。
func undoTestCipher() *secret.Cipher {
	c, _ := secret.NewCipher("")
	return c
}

// newUndoTestDB 打开内存 sqlite 并迁移撤回子系统涉及的表（配置 / 文件 / 审计 / 可逆账目 / 设置）。
// 用 t.Name() 作每测试独立内存库（cache=shared 让本测试内多连接共享同一私有库）；连接池钉单连接，
// 使并发撤回的 CAS 走"一个先提交、另一个查到已迁移"的确定路径（根因串行化，勿在测试容忍并发缺陷）。
func newUndoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:undo_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.ConfigItem{}, &model.ConfigRevision{},
		&model.FileObject{}, &model.FileRevision{}, &model.AuditLog{},
		&model.ReversibleOperation{}, &model.Setting{}, &model.ReverseFetchTask{}, &model.AgentCommand{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// undoTestKit 聚合撤回测试用到的服务（已互相接线：记账器注入发布 / 下发 / ingest 路径）。
type undoTestKit struct {
	db        *gorm.DB
	configSvc *ConfigService
	fileSvc   *FileService
	undoSvc   *ReversibleOperationService
	repo      *repository.ReversibleOperationRepository
}

func newUndoKit(t *testing.T) *undoTestKit {
	t.Helper()
	db := newUndoTestDB(t)
	auditRepo := repository.NewAuditLogRepository(db)
	configRepo := repository.NewConfigItemRepository(db, undoTestCipher())
	revRepo := repository.NewConfigRevisionRepository(db, undoTestCipher())
	configSvc := NewConfigService(db, configRepo, revRepo, auditRepo)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	settingsSvc, err := NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		t.Fatalf("构造设置服务失败: %v", err)
	}
	repo := repository.NewReversibleOperationRepository(db)
	undoSvc := NewReversibleOperationService(db, repo, configSvc, fileSvc, auditRepo, settingsSvc)
	configSvc.SetReversibleRecorder(undoSvc)
	fileSvc.SetReversibleRecorder(undoSvc)
	return &undoTestKit{db: db, configSvc: configSvc, fileSvc: fileSvc, undoSvc: undoSvc, repo: repo}
}

// seedPublishedConfig 新建一个配置项并再发布一版（产生一条 publish 可逆账目），返回 item 与该账目。
func seedPublishedConfig(t *testing.T, kit *undoTestKit, dataID, v1, v2 string) (*model.ConfigItem, *model.ReversibleOperation) {
	t.Helper()
	item, err := kit.configSvc.Create(CreateConfigParams{
		Namespace: "prod", Group: "__GLOBAL__", DataID: dataID, ScopeLevel: model.ScopeGlobal,
		Format: "yaml", Content: v1, Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	if _, err := kit.configSvc.Publish(item.ID, v2, "alice", "", ""); err != nil {
		t.Fatalf("发布配置失败: %v", err)
	}
	ops, err := kit.repo.List(repository.ReversibleOperationFilter{Namespace: "prod", OpType: model.ReversibleOpPublish})
	if err != nil || len(ops) == 0 {
		t.Fatalf("发布后应有可逆账目: err=%v len=%d", err, len(ops))
	}
	return item, &ops[0]
}

// 撤回发布：撤回后配置项回到发布前内容、账目转 reversed。
func TestUndoPublish_RevertsToPreVersion(t *testing.T) {
	kit := newUndoKit(t)
	item, op := seedPublishedConfig(t, kit, "mysql.yml", "a: 1\n", "a: 2\n")
	if op.Status != model.ReversibleStatusReversible {
		t.Fatalf("账目应可撤回, got %s", op.Status)
	}

	if _, err := kit.undoSvc.Undo(op.ID, "bob", "1.2.3.4"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}

	got, err := kit.configSvc.Get(item.ID)
	if err != nil {
		t.Fatalf("取配置失败: %v", err)
	}
	if got.Content != "a: 1\n" {
		t.Fatalf("撤回后内容应回到发布前 a:1, got %q", got.Content)
	}
	// 撤回 = 回滚到 v1 内容、版本继续前移（v1→v2→撤回新版 v3）
	if got.Version != 3 {
		t.Fatalf("撤回后版本应为 3, got %d", got.Version)
	}
	after, _ := kit.repo.FindByID(op.ID)
	if after.Status != model.ReversibleStatusReversed {
		t.Fatalf("撤回后账目应为 reversed, got %s", after.Status)
	}
	if after.ReversedBy != "bob" || after.InversePayload != "" {
		t.Fatalf("撤回应回填撤回人并清空反向快照: by=%q payload=%q", after.ReversedBy, after.InversePayload)
	}
	// 撤回入审计（config.undo-publish）
	var auditN int64
	kit.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoPublish).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("应有 1 条 undo-publish 审计, got %d", auditN)
	}
}

// 幂等：同一账目重复撤回只生效一次，第二次返回幂等成功、不再二次回滚。
func TestUndo_Idempotent(t *testing.T) {
	kit := newUndoKit(t)
	item, op := seedPublishedConfig(t, kit, "app.yml", "x: 1\n", "x: 2\n")

	if _, err := kit.undoSvc.Undo(op.ID, "bob", ""); err != nil {
		t.Fatalf("首次撤回失败: %v", err)
	}
	verAfterFirst, _ := kit.configSvc.Get(item.ID)

	// 第二次撤回：幂等成功、不报错、不再回滚（版本不再前移）
	got, err := kit.undoSvc.Undo(op.ID, "carol", "")
	if err != nil {
		t.Fatalf("重复撤回应幂等成功, got err=%v", err)
	}
	if got.Status != model.ReversibleStatusReversed {
		t.Fatalf("重复撤回应返回 reversed, got %s", got.Status)
	}
	verAfterSecond, _ := kit.configSvc.Get(item.ID)
	if verAfterFirst.Version != verAfterSecond.Version {
		t.Fatalf("重复撤回不应再次回滚: v1=%d v2=%d", verAfterFirst.Version, verAfterSecond.Version)
	}
	// 仍只有 1 条撤回审计
	var auditN int64
	kit.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoPublish).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("幂等撤回应只 1 条审计, got %d", auditN)
	}
}

// 并发撤回同一账目：N 个并发撤回只有一个真回滚、其余幂等成功；版本只前移一次（不双撤回）。
func TestUndo_ConcurrentSameOp_NoDoubleUndo(t *testing.T) {
	kit := newUndoKit(t)
	item, op := seedPublishedConfig(t, kit, "c.yml", "n: 1\n", "n: 2\n")

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = kit.undoSvc.Undo(op.ID, "bob", "")
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("并发撤回 #%d 不应报错（幂等成功）, got %v", i, e)
		}
	}
	got, _ := kit.configSvc.Get(item.ID)
	// v1→v2(publish)→撤回新版 v3：并发撤回只生效一次，版本恰为 3
	if got.Version != 3 {
		t.Fatalf("并发撤回应只回滚一次（版本=3）, got %d", got.Version)
	}
	if got.Content != "n: 1\n" {
		t.Fatalf("撤回后内容应回到发布前, got %q", got.Content)
	}
	var auditN int64
	kit.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoPublish).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("并发撤回应只 1 条审计, got %d", auditN)
	}
}

// 被覆盖：同 scope 再发布一版会把旧账目置 superseded，旧账目不可撤回。
func TestUndo_SupersededRejected(t *testing.T) {
	kit := newUndoKit(t)
	item, op1 := seedPublishedConfig(t, kit, "s.yml", "v: 1\n", "v: 2\n")

	// 再发布一版 → 旧 op1 被 superseded、新建 op2
	if _, err := kit.configSvc.Publish(item.ID, "v: 3\n", "alice", "", ""); err != nil {
		t.Fatalf("二次发布失败: %v", err)
	}
	after1, _ := kit.repo.FindByID(op1.ID)
	if after1.Status != model.ReversibleStatusSuperseded {
		t.Fatalf("旧账目应被 superseded, got %s", after1.Status)
	}
	if after1.InversePayload != "" {
		t.Fatalf("被覆盖账目应清空反向快照, got %q", after1.InversePayload)
	}
	// 撤回被覆盖的旧账目 → 明确拒绝
	if _, err := kit.undoSvc.Undo(op1.ID, "bob", ""); err != apperr.ErrReversibleOpSuperseded {
		t.Fatalf("撤回被覆盖账目应返回 superseded, got %v", err)
	}
}

// 过期：超窗口的 reversible 账目被清理器置 expired，撤回明确拒绝。
func TestUndo_ExpiredRejected(t *testing.T) {
	kit := newUndoKit(t)
	_, op := seedPublishedConfig(t, kit, "e.yml", "k: 1\n", "k: 2\n")

	// 模拟清理器：把 before=未来时刻 的 reversible 账目过期（覆盖该账目）
	n, err := kit.undoSvc.ExpireStale(time.Now().UTC().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("过期清理应命中 1 条: n=%d err=%v", n, err)
	}
	after, _ := kit.repo.FindByID(op.ID)
	if after.Status != model.ReversibleStatusExpired {
		t.Fatalf("应被置 expired, got %s", after.Status)
	}
	if _, err := kit.undoSvc.Undo(op.ID, "bob", ""); err != apperr.ErrReversibleOpExpired {
		t.Fatalf("撤回过期账目应返回 expired, got %v", err)
	}
}

// 多撤回交错：两条不同账目各自撤回、互不影响、各回滚各自目标。
func TestUndo_MultipleDistinctOps_Interleaved(t *testing.T) {
	kit := newUndoKit(t)
	itemA, opA := seedPublishedConfig(t, kit, "a.yml", "a: 1\n", "a: 2\n")
	itemB, opB := seedPublishedConfig(t, kit, "b.yml", "b: 1\n", "b: 2\n")

	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	go func() { defer wg.Done(); _, errA = kit.undoSvc.Undo(opA.ID, "bob", "") }()
	go func() { defer wg.Done(); _, errB = kit.undoSvc.Undo(opB.ID, "carol", "") }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("交错撤回不应报错: A=%v B=%v", errA, errB)
	}
	gotA, _ := kit.configSvc.Get(itemA.ID)
	gotB, _ := kit.configSvc.Get(itemB.ID)
	if gotA.Content != "a: 1\n" || gotB.Content != "b: 1\n" {
		t.Fatalf("两账目各自回滚到各自前版本: A=%q B=%q", gotA.Content, gotB.Content)
	}
}

// 撤回不存在的账目 → NOT_FOUND。
func TestUndo_NotFound(t *testing.T) {
	kit := newUndoKit(t)
	if _, err := kit.undoSvc.Undo(9999, "bob", ""); err != apperr.ErrReversibleOpNotFound {
		t.Fatalf("撤回不存在账目应 NOT_FOUND, got %v", err)
	}
}

// 撤回下发（push）：撤回后文件回到下发前内容。
func TestUndoPush_RevertsFile(t *testing.T) {
	kit := newUndoKit(t)
	obj, err := kit.fileSvc.Create(CreateFileParams{
		Namespace: "prod", Group: "main", Path: "plugins/x.yml", ScopeLevel: model.ScopeGroup,
		Content: "p: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
	if _, err := kit.fileSvc.Publish(obj.ID, "p: 2\n", "alice", "", ""); err != nil {
		t.Fatalf("下发文件失败: %v", err)
	}
	ops, _ := kit.repo.List(repository.ReversibleOperationFilter{OpType: model.ReversibleOpPush})
	if len(ops) != 1 {
		t.Fatalf("下发应产生 1 条 push 账目, got %d", len(ops))
	}
	if _, err := kit.undoSvc.Undo(ops[0].ID, "bob", ""); err != nil {
		t.Fatalf("撤回下发失败: %v", err)
	}
	got, _ := kit.fileSvc.Get(obj.ID)
	if got.Content != "p: 1\n" {
		t.Fatalf("撤回下发后内容应回到下发前, got %q", got.Content)
	}
}

// 撤回反向抓取（fetch）：被新建项软删、被覆盖项回滚到 ingest 前版本。
func TestUndoFetch_SoftDeletesCreatedAndRollsBackUpdated(t *testing.T) {
	kit := newUndoKit(t)
	fileSvc := kit.fileSvc

	// 预置一个已存在的受管文件（将被 ingest 覆盖）
	existing, err := fileSvc.Create(CreateFileParams{
		Namespace: "prod", Group: "main", Path: "plugins/keep.yml", ScopeLevel: model.ScopeGroup,
		Content: "old: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建已存在文件失败: %v", err)
	}

	// 模拟一次 ingest：Import 新建 new.yml + 覆盖 keep.yml
	result, err := fileSvc.Import(ImportFilesParams{
		Namespace: "prod", Group: "main", ScopeLevel: model.ScopeGroup,
		Files: []ImportFile{
			{Path: "plugins/new.yml", Content: "new: 1\n"},
			{Path: "plugins/keep.yml", Content: "old: 2\n"},
		},
		Operator: "alice",
	})
	if err != nil {
		t.Fatalf("ingest 失败: %v", err)
	}
	if result.Created != 1 || result.Updated != 1 {
		t.Fatalf("ingest 应 1 新建 1 覆盖, got created=%d updated=%d", result.Created, result.Updated)
	}

	// 记 fetch 可逆账目（生产由 ReverseFetchTaskService 在 ingest 后补记；此处直接调记账）
	if err := kit.undoSvc.RecordFetch(RecordFetchParams{
		Namespace: "prod", Scope: model.ScopeGroup, ScopeTarget: "",
		TaskID: 1, CreatedIDs: result.CreatedIDs, UpdatedItems: result.UpdatedItems,
		ForwardRef: "1", Summary: "测试 ingest", Operator: "alice",
	}); err != nil {
		t.Fatalf("记 fetch 账目失败: %v", err)
	}
	ops, _ := kit.repo.List(repository.ReversibleOperationFilter{OpType: model.ReversibleOpFetch})
	if len(ops) != 1 {
		t.Fatalf("应有 1 条 fetch 账目, got %d", len(ops))
	}
	createdID := result.CreatedIDs[0]

	// 撤回 ingest
	if _, err := kit.undoSvc.Undo(ops[0].ID, "bob", ""); err != nil {
		t.Fatalf("撤回 fetch 失败: %v", err)
	}

	// 被新建项软删 → FindByID 取不到（已脱链）
	if got, _ := fileSvc.Get(createdID); got != nil {
		t.Fatalf("被新建项应被软删, 仍可见 %+v", got)
	}
	// 被覆盖项回滚到 ingest 前内容
	gotKeep, _ := fileSvc.Get(existing.ID)
	if gotKeep == nil || gotKeep.Content != "old: 1\n" {
		t.Fatalf("被覆盖项应回滚到 ingest 前 old:1, got %+v", gotKeep)
	}
	// 撤回入审计（config.undo-fetch）
	var auditN int64
	kit.db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoFetch).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("应有 1 条 undo-fetch 审计, got %d", auditN)
	}
}

// 撤回与新发布并发：撤回旧账目的同时对同一项发布新版——二者各自原子，账目状态最终一致（reversed 或 superseded 二选一，不脏写）。
func TestUndo_VsNewPublish_Concurrent(t *testing.T) {
	kit := newUndoKit(t)
	item, op := seedPublishedConfig(t, kit, "race.yml", "r: 1\n", "r: 2\n")

	var wg sync.WaitGroup
	wg.Add(2)
	var undoErr, pubErr error
	go func() { defer wg.Done(); _, undoErr = kit.undoSvc.Undo(op.ID, "bob", "") }()
	go func() { defer wg.Done(); _, pubErr = kit.configSvc.Publish(item.ID, "r: 9\n", "alice", "", "") }()
	wg.Wait()

	if pubErr != nil {
		t.Fatalf("新发布不应失败: %v", pubErr)
	}
	// 撤回要么成功（赢在覆盖前）、要么因被覆盖/状态拒（输给新发布），二者皆为合法确定结果——不得脏写。
	if undoErr != nil &&
		undoErr != apperr.ErrReversibleOpSuperseded &&
		undoErr != apperr.ErrReversibleOpState {
		t.Fatalf("撤回与新发布竞争应为成功 / superseded / state, got %v", undoErr)
	}
	after, _ := kit.repo.FindByID(op.ID)
	if after.Status == model.ReversibleStatusReversible {
		t.Fatalf("竞争后旧账目不应仍 reversible, got %s", after.Status)
	}
}

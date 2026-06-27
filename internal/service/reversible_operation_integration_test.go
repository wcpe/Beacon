//go:build integration

package service_test

import (
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// newUndoStack 在真实 MySQL 上装配撤回子系统全栈（配置 / 文件 / 撤回服务已互相接线）。
func newUndoStack(t *testing.T) (*service.ConfigService, *service.FileService, *service.ReversibleOperationService, *repository.ReversibleOperationRepository, *gorm.DB) {
	db := testDB(t)
	auditRepo := repository.NewAuditLogRepository(db)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	rr := repository.NewConfigRevisionRepository(db, noEncryptCipher())
	configSvc := service.NewConfigService(db, cr, rr, auditRepo)
	fileSvc := service.NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	settingsSvc, err := service.NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		t.Fatalf("构造设置服务失败: %v", err)
	}
	repo := repository.NewReversibleOperationRepository(db)
	undoSvc := service.NewReversibleOperationService(db, repo, configSvc, fileSvc, auditRepo, settingsSvc)
	configSvc.SetReversibleRecorder(undoSvc)
	fileSvc.SetReversibleRecorder(undoSvc)
	return configSvc, fileSvc, undoSvc, repo, db
}

func seedConfigPublish(t *testing.T, cfg *service.ConfigService, repo *repository.ReversibleOperationRepository, dataID string) *model.ReversibleOperation {
	t.Helper()
	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: "__GLOBAL__", DataID: dataID, ScopeLevel: model.ScopeGlobal,
		Format: "yaml", Content: "a: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	if _, err := cfg.Publish(item.ID, "a: 2\n", "alice", "", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	ops, err := repo.List(repository.ReversibleOperationFilter{Namespace: "prod", OpType: model.ReversibleOpPublish})
	if err != nil || len(ops) == 0 {
		t.Fatalf("应有可逆账目: err=%v len=%d", err, len(ops))
	}
	for i := range ops {
		if ops[i].ForwardRef == "prod/__GLOBAL__/"+dataID+"@global:" {
			return &ops[i]
		}
	}
	t.Fatalf("未找到 %s 的可逆账目", dataID)
	return nil
}

// 真 MySQL：撤回多表写原子 + 幂等 + 入审计。
func TestUndoPublish_AtomicAndAudited_MySQL(t *testing.T) {
	cfg, _, undoSvc, repo, db := newUndoStack(t)
	op := seedConfigPublish(t, cfg, repo, "atomic.yml")

	if _, err := undoSvc.Undo(op.ID, "bob", "1.1.1.1"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	after, _ := repo.FindByID(op.ID)
	if after.Status != model.ReversibleStatusReversed {
		t.Fatalf("撤回后应 reversed, got %s", after.Status)
	}
	var auditN int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoPublish).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("应 1 条 undo-publish 审计, got %d", auditN)
	}
}

// 真 MySQL：N 个并发撤回同一账目只生效一次（不双撤回），其余幂等成功。-count 反复跑验非脆。
func TestUndo_ConcurrentSameOp_MySQL(t *testing.T) {
	cfg, _, undoSvc, repo, db := newUndoStack(t)
	op := seedConfigPublish(t, cfg, repo, "race.yml")

	const workers = 10
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = undoSvc.Undo(op.ID, "bob", "")
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("并发撤回 #%d 应幂等成功, got %v", i, e)
		}
	}
	// 撤回审计恰 1 条（不双撤回）
	var auditN int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigUndoPublish).Count(&auditN)
	if auditN != 1 {
		t.Fatalf("并发撤回应只 1 条审计, got %d", auditN)
	}
}

// 真 MySQL：撤回 vs 同对象新发布并发——二者各自原子，旧账目终态不再 reversible，撤回结果为成功 / superseded / state。
func TestUndo_VsNewPublish_MySQL(t *testing.T) {
	cfg, _, undoSvc, repo, _ := newUndoStack(t)
	item, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: "__GLOBAL__", DataID: "vs.yml", ScopeLevel: model.ScopeGlobal,
		Format: "yaml", Content: "v: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	if _, err := cfg.Publish(item.ID, "v: 2\n", "alice", "", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	ops, _ := repo.List(repository.ReversibleOperationFilter{Namespace: "prod", OpType: model.ReversibleOpPublish})
	var op *model.ReversibleOperation
	for i := range ops {
		if ops[i].ForwardRef == "prod/__GLOBAL__/vs.yml@global:" {
			op = &ops[i]
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var undoErr, pubErr error
	go func() { defer wg.Done(); _, undoErr = undoSvc.Undo(op.ID, "bob", "") }()
	go func() { defer wg.Done(); _, pubErr = cfg.Publish(item.ID, "v: 9\n", "alice", "", "") }()
	wg.Wait()

	if pubErr != nil {
		t.Fatalf("新发布不应失败: %v", pubErr)
	}
	if undoErr != nil && undoErr != apperr.ErrReversibleOpSuperseded && undoErr != apperr.ErrReversibleOpState {
		t.Fatalf("撤回竞争应为成功 / superseded / state, got %v", undoErr)
	}
	after, _ := repo.FindByID(op.ID)
	if after.Status == model.ReversibleStatusReversible {
		t.Fatalf("竞争后旧账目不应仍 reversible, got %s", after.Status)
	}
}

// 真 MySQL：撤回 fetch——被新建项软删、被覆盖项回滚到 ingest 前版本。
func TestUndoFetch_MySQL(t *testing.T) {
	_, fileSvc, undoSvc, repo, _ := newUndoStack(t)
	existing, err := fileSvc.Create(service.CreateFileParams{
		Namespace: "prod", Group: "main", Path: "plugins/keep.yml", ScopeLevel: model.ScopeGroup,
		Content: "old: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
	result, err := fileSvc.Import(service.ImportFilesParams{
		Namespace: "prod", Group: "main", ScopeLevel: model.ScopeGroup,
		Files: []service.ImportFile{
			{Path: "plugins/new.yml", Content: "new: 1\n"},
			{Path: "plugins/keep.yml", Content: "old: 2\n"},
		},
		Operator: "alice",
	})
	if err != nil {
		t.Fatalf("ingest 失败: %v", err)
	}
	if err := undoSvc.RecordFetch(service.RecordFetchParams{
		Namespace: "prod", Scope: model.ScopeGroup, TaskID: 1,
		CreatedIDs: result.CreatedIDs, UpdatedItems: result.UpdatedItems,
		ForwardRef: "1", Summary: "测试", Operator: "alice",
	}); err != nil {
		t.Fatalf("记 fetch 账目失败: %v", err)
	}
	ops, _ := repo.List(repository.ReversibleOperationFilter{OpType: model.ReversibleOpFetch})
	if len(ops) != 1 {
		t.Fatalf("应 1 条 fetch 账目, got %d", len(ops))
	}
	createdID := result.CreatedIDs[0]
	if _, err := undoSvc.Undo(ops[0].ID, "bob", ""); err != nil {
		t.Fatalf("撤回 fetch 失败: %v", err)
	}
	if got, _ := fileSvc.Get(createdID); got != nil {
		t.Fatalf("被新建项应被软删")
	}
	gotKeep, _ := fileSvc.Get(existing.ID)
	if gotKeep == nil || gotKeep.Content != "old: 1\n" {
		t.Fatalf("被覆盖项应回滚到 old:1, got %+v", gotKeep)
	}
}

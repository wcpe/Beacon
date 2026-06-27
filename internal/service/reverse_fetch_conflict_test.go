package service

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// gormShared 持有共享内存 sqlite 的 *gorm.DB（辅助把 helper 签名收敛，避免到处传 db）。
type gormShared struct{ db *gorm.DB }

func nowMinus1h() time.Time { return time.Now().Add(-1 * time.Hour) }
func nowMinus2h() time.Time { return time.Now().Add(-2 * time.Hour) }

// seedExistingFile 在目标 group 层预置一个已发布文件（制造冲突）。
func seedExistingFile(t *testing.T, svc *ReverseFetchTaskService, path, content string) {
	t.Helper()
	if _, err := svc.fileSvc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: path, ScopeLevel: model.ScopeGroup,
		Content: content, Operator: "seed", Comment: "预置",
	}); err != nil {
		t.Fatalf("预置已有文件 %s 失败: %v", path, err)
	}
}

// TestNoConflictGoesStraightToDone 提交后目标无已有版本 → 直接落库 done（不进 conflict-review）。
func TestNoConflictGoesStraightToDone(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/lang.yml", Size: 1, IsText: true},
	})
	got, _ := svc.Submit(task.ID, []string{"A/config.yml", "B/lang.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)
	res, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "k: 1\n"},
		{Path: "B/lang.yml", Content: "hi: hello\n"},
	}, "")
	if err != nil {
		t.Fatalf("无冲突应直接落库: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("应落 2 个文件，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	done, _ := svc.Get(task.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("无冲突落库后应 done，实际 %s", done.Status)
	}
	if done.SubmitContent != "" {
		t.Fatal("无冲突路径不应暂存 submit_content")
	}
}

// TestConflictEntersReviewAndStashesNotLanded 有冲突 → 进 conflict-review、暂存全部回传内容、不落库覆盖已有版本。
func TestConflictEntersReviewAndStashesNotLanded(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	// 目标 group 层已有 A/config.yml（制造冲突）
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	existing, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	oldVersion := existing.Version

	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/new.yml", Size: 1, IsText: true},
	})
	got, _ := svc.Submit(task.ID, []string{"A/config.yml", "B/new.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)

	// 回传含冲突文件 A/config.yml（新内容）+ 非冲突 B/new.yml → 应进 conflict-review、不落库
	res, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "new: 2\n"},
		{Path: "B/new.yml", Content: "added: yes\n"},
	}, "")
	if err != nil {
		t.Fatalf("有冲突应静默进 conflict-review（无错），实际 %v", err)
	}
	if res != nil {
		t.Fatalf("进冲突审核不应有落库结果，实际 %+v", res)
	}
	cr, _ := svc.Get(task.ID)
	if cr.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("有冲突应进 conflict-review，实际 %s", cr.Status)
	}
	if cr.SubmitContent == "" {
		t.Fatal("conflict-review 应暂存 submit 回传内容")
	}
	// 暂存信封含全部回传文件 + 冲突集
	var env submitContentEnvelope
	_ = json.Unmarshal([]byte(cr.SubmitContent), &env)
	if len(env.Files) != 2 || len(env.Conflicts) != 1 || env.Conflicts[0] != "A/config.yml" {
		t.Fatalf("信封应含 2 文件 + 1 冲突，实际 %+v", env)
	}
	// 不落库：已有版本不变、非冲突文件也未落（待 resolve）
	stillOld, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if stillOld.Version != oldVersion || stillOld.Content != "old: 1\n" {
		t.Fatalf("进冲突审核不应覆盖已有版本，实际 v=%d content=%q", stillOld.Version, stillOld.Content)
	}
	if obj, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "B/new.yml", model.ScopeGroup, ""); obj != nil {
		t.Fatal("非冲突文件也应待 resolve 才落库，进冲突审核期不落")
	}
	// 互斥：conflict-review 仍占活跃，同实例不可再建
	if _, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err == nil {
		t.Fatal("conflict-review 应仍占活跃，互斥应拒新建")
	}
}

// setupConflictReview 预置一个含 1 冲突 + 1 非冲突的 conflict-review 任务，返回任务与冲突文件抓取 md5。
func setupConflictReview(t *testing.T, db *gormShared, svc *ReverseFetchTaskService) (*model.ReverseFetchTask, string) {
	t.Helper()
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	task := scanToPendingReview(t, db.db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/new.yml", Size: 1, IsText: true},
	})
	got, _ := svc.Submit(task.ID, []string{"A/config.yml", "B/new.yml"}, false, "alice", "")
	fetchCmd(t, db.db, got.SubmitCommandID)
	if _, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "new: 2\n"},
		{Path: "B/new.yml", Content: "added: yes\n"},
	}, ""); err != nil {
		t.Fatalf("进冲突审核失败: %v", err)
	}
	cr, _ := svc.Get(task.ID)
	return cr, filetree.ContentMD5("new: 2\n")
}

// TestConflictDiffReturnsFetchedAndExisting diff 返回抓取值 ⟷ 目标已有版本。
func TestConflictDiffReturnsFetchedAndExisting(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	diff, err := svc.ConflictDiff(cr.ID, "A/config.yml")
	if err != nil {
		t.Fatalf("取冲突 diff 失败: %v", err)
	}
	if diff.FetchedContent != "new: 2\n" || diff.FetchedMD5 != fetchedMD5 {
		t.Fatalf("抓取值应为回传内容，实际 %q / %s", diff.FetchedContent, diff.FetchedMD5)
	}
	if diff.ExistingContent != "old: 1\n" || diff.ExistingMD5 != filetree.ContentMD5("old: 1\n") {
		t.Fatalf("已有版本应为预置内容，实际 %q / %s", diff.ExistingContent, diff.ExistingMD5)
	}
	if diff.Version != 1 {
		t.Fatalf("已有版本号应为 1，实际 %d", diff.Version)
	}
	// 非冲突 path / 不存在 path 取 diff → 冲突不存在
	if _, err := svc.ConflictDiff(cr.ID, "B/new.yml"); err != apperr.ErrReverseFetchConflictNotFound {
		t.Fatalf("非冲突 path 取 diff 应 CONFLICT_NOT_FOUND，实际 %v", err)
	}
}

// TestResolveOverwriteRequiresReviewedMD5 overwrite 须带正确 reviewedMd5（盲确认 / 漂移 412）。
func TestResolveOverwriteRequiresReviewedMD5(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	// 盲确认（无 reviewedMd5）→ 412
	_, err := svc.Resolve(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite}}, "alice", "")
	if err != apperr.ErrReverseFetchReviewMismatch {
		t.Fatalf("盲确认应 412 REVERSE_FETCH_REVIEW_MISMATCH，实际 %v", err)
	}
	// 错 md5 → 412
	_, err = svc.Resolve(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: "deadbeef"}}, "alice", "")
	if err != apperr.ErrReverseFetchReviewMismatch {
		t.Fatalf("错 md5 应 412，实际 %v", err)
	}
	// 被拒后任务仍 conflict-review（未认领、可重 resolve）
	still, _ := svc.Get(cr.ID)
	if still.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("自审失败后任务应仍 conflict-review，实际 %s", still.Status)
	}
	_ = fetchedMD5
}

// TestResolveOverwriteAndNonConflictLand overwrite 自审通过 + 非冲突文件一并落库、任务 done。
func TestResolveOverwriteAndNonConflictLand(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	res, err := svc.Resolve(cr.ID, []ResolveDecision{
		{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
	}, "alice", "")
	if err != nil {
		t.Fatalf("overwrite 自审通过应落库: %v", err)
	}
	// A/config.yml 覆盖（已有→新版本，Updated）+ B/new.yml 非冲突首发（Created）
	if res.Created != 1 || res.Updated != 1 {
		t.Fatalf("应 created=1（非冲突首发）updated=1（冲突覆盖），实际 created=%d updated=%d", res.Created, res.Updated)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone || done.SubmitContent != "" {
		t.Fatalf("resolve 后应 done 且清空 submit_content，实际 status=%s contentLen=%d", done.Status, len(done.SubmitContent))
	}
	// 冲突文件被覆盖为抓取内容
	repo := repository.NewFileObjectRepository(shared.db)
	overwritten, _ := repo.FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if overwritten.Content != "new: 2\n" || overwritten.Version != 2 {
		t.Fatalf("冲突文件应被覆盖为抓取内容（v2），实际 v=%d content=%q", overwritten.Version, overwritten.Content)
	}
	// 非冲突文件落库
	if added, _ := repo.FindByIdentity("prod", "area1", "B/new.yml", model.ScopeGroup, ""); added == nil {
		t.Fatal("非冲突文件应在 resolve 时落库")
	}
	if rfCountAudit(t, shared.db, model.ActionFileReverseFetchIngest) != 1 {
		t.Fatal("resolve 应记一条 file.reverse-fetch-ingest 审计")
	}
}

// TestResolveKeepSkipsConflict keep 保留已有：冲突文件不覆盖，非冲突文件仍落库、任务 done。
func TestResolveKeepSkipsConflict(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)

	res, err := svc.Resolve(cr.ID, []ResolveDecision{
		{Path: "A/config.yml", Action: ResolveActionKeep},
	}, "alice", "")
	if err != nil {
		t.Fatalf("keep 应成功: %v", err)
	}
	// 只有非冲突 B/new.yml 首发；冲突 A/config.yml 保留已有、不落
	if res.Created != 1 || res.Updated != 0 {
		t.Fatalf("keep 应 created=1 updated=0，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	repo := repository.NewFileObjectRepository(shared.db)
	kept, _ := repo.FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if kept.Content != "old: 1\n" || kept.Version != 1 {
		t.Fatalf("keep 应保留已有版本不变，实际 v=%d content=%q", kept.Version, kept.Content)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("resolve 后应 done，实际 %s", done.Status)
	}
}

// TestResolveRequiresDecisionForEachConflict 冲突集每项须有决定（缺决定 → 拒，不落库）。
func TestResolveRequiresDecisionForEachConflict(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)

	// 空决定（漏掉唯一冲突）→ 拒
	if _, err := svc.Resolve(cr.ID, nil, "alice", ""); err != apperr.ErrInvalidParam {
		t.Fatalf("漏掉冲突决定应 INVALID_PARAM，实际 %v", err)
	}
	// 决定指向非冲突 path → 冲突不存在
	if _, err := svc.Resolve(cr.ID, []ResolveDecision{{Path: "B/new.yml", Action: ResolveActionKeep}}, "alice", ""); err != apperr.ErrReverseFetchConflictNotFound {
		t.Fatalf("决定指向非冲突 path 应 CONFLICT_NOT_FOUND，实际 %v", err)
	}
	// 任务仍 conflict-review
	still, _ := svc.Get(cr.ID)
	if still.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("拒后应仍 conflict-review，实际 %s", still.Status)
	}
}

// TestResolveConcurrentClaimOnce 并发双 resolve 只有一个认领成功（CAS conflict-review→ingesting）。
func TestResolveConcurrentClaimOnce(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, stateErrCount := 0, 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Resolve(cr.ID, []ResolveDecision{
				{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
			}, "alice", "")
			mu.Lock()
			defer mu.Unlock()
			switch err {
			case nil:
				okCount++
			case apperr.ErrReverseFetchTaskState:
				stateErrCount++
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || stateErrCount != 1 {
		t.Fatalf("并发双 resolve 应恰一个成功一个 STATE，实际 ok=%d state=%d", okCount, stateErrCount)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("认领者落库后应 done，实际 %s", done.Status)
	}
}

// TestExpireClearsSubmitContent conflict-review 任务过期 → expired 且清空 submit_content、互斥解除。
func TestExpireClearsSubmitContent(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)
	// 把创建时间推到 2 小时前
	if err := shared.db.Model(&model.ReverseFetchTask{}).Where("id = ?", cr.ID).
		Update("created_at", nowMinus2h()).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := svc.ExpireStale(nowMinus1h())
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
	got, _ := svc.Get(cr.ID)
	if got.Status != model.ReverseFetchTaskExpired || got.SubmitContent != "" || got.Manifest != "" {
		t.Fatalf("过期后应 expired 且清空瞬态，实际 status=%s submitLen=%d manifestLen=%d",
			got.Status, len(got.SubmitContent), len(got.Manifest))
	}
	// 互斥解除：同实例可再建
	if _, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("过期后同实例应可再建，实际 %v", err)
	}
}

package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// fileSyncReportAuthStub 是文件同步数据面的权威身份校验桩。
//
// identity 可在请求之间切换，用于同一路由上交替扮演源 agent 与目标 agent；
// err 非空时模拟「缺身份头 / token 非法」的拒绝路径。
type fileSyncReportAuthStub struct {
	identity agentauth.Identity
	err      error
}

func (s *fileSyncReportAuthStub) AuthenticateAgentReport(_, _, _, _ string) (agentauth.Identity, error) {
	if s.err != nil {
		return agentauth.Identity{}, s.err
	}
	return s.identity, nil
}

// fileSyncIdentityFixture 承载四条数据面端点的鉴权测试脚手架：
// 真库（sqlite）+ 真 service，才能验证「拒绝时不产生副作用」。
type fileSyncIdentityFixture struct {
	handler   *FileSyncHandler
	svc       *service.FileSyncService
	auth      *fileSyncReportAuthStub
	router    chi.Router
	db        *gorm.DB
	repo      *repository.FileSyncRepository
	task      *model.FileSyncTask
	target    *model.FileSyncTarget
	sourceCmd *model.AgentCommand
	targetCmd *model.AgentCommand
	blobRoot  string
}

func newFileSyncIdentityFixture(t *testing.T) *fileSyncIdentityFixture {
	t.Helper()
	// DSN 按用例名隔离：同包的 sqlite 内存库按名字共享，复用固定名字会让用例互相污染。
	dsn := fmt.Sprintf("file:fs_identity_%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 sqlite 失败：%v", err)
	}
	if err := db.AutoMigrate(
		&model.FileSyncTask{}, &model.FileSyncFile{}, &model.FileSyncBatch{},
		&model.FileSyncTarget{}, &model.FileSyncLog{}, &model.AgentCommand{},
	); err != nil {
		t.Fatalf("迁移测试表失败：%v", err)
	}
	task := &model.FileSyncTask{
		NamespaceCode: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		Status: model.FileSyncTaskStatusScanning, BatchSize: 1, IntervalSec: 0,
		FailureThresholdPercent: 50, Operator: "admin",
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	sourceCmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "source-1", Type: model.CommandTypeFileSyncSource,
		Payload: fmt.Sprintf(`{"taskId":%d,"directory":"plugins/demo"}`, task.ID),
		Status:  model.CommandStatusFetched, Operator: "admin",
	}
	if err := db.Create(sourceCmd).Error; err != nil {
		t.Fatalf("创建源扫描命令失败：%v", err)
	}
	task.SourceCommandID = sourceCmd.ID
	if err := db.Model(&model.FileSyncTask{}).Where("id = ?", task.ID).
		Update("source_command_id", sourceCmd.ID).Error; err != nil {
		t.Fatalf("绑定源扫描命令失败：%v", err)
	}
	target := &model.FileSyncTarget{
		TaskID: task.ID, BatchID: 0, BatchNo: 1, ServerID: "target-1",
		Status: model.FileSyncTargetStatusPending,
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("创建测试目标失败：%v", err)
	}
	targetCmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "target-1", Type: model.CommandTypeFileSyncApply,
		Payload: fmt.Sprintf(`{"taskId":%d,"batchId":0,"targetId":%d,"directory":"plugins/demo"}`, task.ID, target.ID),
		Status:  model.CommandStatusFetched, Operator: "admin",
	}
	if err := db.Create(targetCmd).Error; err != nil {
		t.Fatalf("创建目标命令失败：%v", err)
	}
	if err := db.Model(&model.FileSyncTarget{}).Where("id = ?", target.ID).
		Update("command_id", targetCmd.ID).Error; err != nil {
		t.Fatalf("绑定目标命令失败：%v", err)
	}
	target.CommandID = targetCmd.ID

	repo := repository.NewFileSyncRepository(db)
	svc := service.NewFileSyncService(db, repo, nil, nil, service.NewFileSyncEventHub())
	svc.SetCommandQueue(repository.NewAgentCommandRepository(db), nil)
	blobRoot := t.TempDir()
	svc.SetBlobRoot(blobRoot)
	auth := &fileSyncReportAuthStub{identity: fileSyncSourceIdentity()}
	h := NewFileSyncHandler(svc)
	h.SetReportAuthenticator(auth)

	r := chi.NewRouter()
	r.Post("/file-sync/{taskId}/manifest", h.ReceiveManifest)
	r.Put("/file-sync/{taskId}/blobs/{hash}", h.UploadBlob)
	r.Get("/file-sync/{taskId}/blobs/{hash}", h.DownloadBlob)
	r.Post("/file-sync/{taskId}/targets/result", h.TargetResult)
	return &fileSyncIdentityFixture{
		handler: h, svc: svc, auth: auth, router: r, db: db, repo: repo,
		task: task, target: target, sourceCmd: sourceCmd, targetCmd: targetCmd, blobRoot: blobRoot,
	}
}

// prepareRunning 把任务推进到 running 且有 1 个 running 批次，供目标结果回传的正向路径使用。
func (f *fileSyncIdentityFixture) prepareRunning(t *testing.T) {
	t.Helper()
	batch := &model.FileSyncBatch{
		TaskID: f.task.ID, BatchNo: 1, Status: model.FileSyncBatchStatusRunning, PlannedCount: 1,
	}
	if err := f.db.Create(batch).Error; err != nil {
		t.Fatalf("创建批次失败：%v", err)
	}
	if err := f.db.Model(&model.FileSyncTarget{}).Where("id = ?", f.target.ID).
		Updates(map[string]any{"batch_id": batch.ID, "status": model.FileSyncTargetStatusManifesting}).Error; err != nil {
		t.Fatalf("绑定批次失败：%v", err)
	}
	f.target.BatchID = batch.ID
	if err := f.db.Model(&model.FileSyncTask{}).Where("id = ?", f.task.ID).
		Updates(map[string]any{"status": model.FileSyncTaskStatusRunning, "source_ready": true}).Error; err != nil {
		t.Fatalf("推进任务状态失败：%v", err)
	}
}

func fileSyncSourceIdentity() agentauth.Identity {
	return agentauth.Identity{Namespace: "prod", ServerID: "source-1", Kind: model.ServerKindBackend, IdentityID: "identity-source-1"}
}

func fileSyncTargetIdentity() agentauth.Identity {
	return agentauth.Identity{Namespace: "prod", ServerID: "target-1", Kind: model.ServerKindBackend, IdentityID: "identity-target-1"}
}

// fileSyncOtherIdentity 是同命名空间内既非源、也未被规划为目标的一台服务器。
func fileSyncOtherIdentity() agentauth.Identity {
	return agentauth.Identity{Namespace: "prod", ServerID: "target-9", Kind: model.ServerKindBackend, IdentityID: "identity-target-9"}
}

func (f *fileSyncIdentityFixture) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *fileSyncIdentityFixture) manifestRequest(t *testing.T, taskID, commandID uint) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"commandId": commandID,
		"files":     []map[string]any{{"path": "config.yml", "size": 12, "hash": testFileSyncHash("config.yml")}},
	})
	if err != nil {
		t.Fatalf("构造清单请求失败：%v", err)
	}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/file-sync/%d/manifest", taskID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (f *fileSyncIdentityFixture) uploadRequest(taskID uint, hash string, content []byte) *http.Request {
	return httptest.NewRequest(http.MethodPut, fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), bytes.NewReader(content))
}

func (f *fileSyncIdentityFixture) downloadRequest(taskID uint, hash string) *http.Request {
	return httptest.NewRequest(http.MethodGet, fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), nil)
}

func (f *fileSyncIdentityFixture) targetResultRequest(t *testing.T, taskID, commandID uint) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"commandId": commandID, "ok": true, "backupPath": ".beacon/a"})
	if err != nil {
		t.Fatalf("构造目标结果请求失败：%v", err)
	}
	return httptest.NewRequest(http.MethodPost, fmt.Sprintf("/file-sync/%d/targets/result", taskID), bytes.NewReader(body))
}

func testFileSyncHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (f *fileSyncIdentityFixture) blobPath(hash string) string {
	return filepath.Join(f.blobRoot, fmt.Sprintf("%d", f.task.ID), hash)
}

func countFileSyncRows(t *testing.T, db *gorm.DB, table any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("统计行数失败：%v", err)
	}
	return n
}

// TestFileSyncDataPlaneRequiresConfirmedIdentity 锁定四条数据面端点都必须有已确认身份：
// 校验器未注入（失败关闭）与身份缺失（401）两种情形都不得进入业务逻辑、不得产生副作用。
func TestFileSyncDataPlaneRequiresConfirmedIdentity(t *testing.T) {
	cases := []struct {
		name string
		// request 用 fixture 生成对应端点的请求。
		request func(t *testing.T, f *fileSyncIdentityFixture) *http.Request
	}{
		{name: "manifest", request: func(t *testing.T, f *fileSyncIdentityFixture) *http.Request {
			t.Helper()
			return f.manifestRequest(t, f.task.ID, f.sourceCmd.ID)
		}},
		{name: "upload-blob", request: func(_ *testing.T, f *fileSyncIdentityFixture) *http.Request {
			return f.uploadRequest(f.task.ID, testFileSyncHash("config.yml"), []byte("config.yml"))
		}},
		{name: "download-blob", request: func(_ *testing.T, f *fileSyncIdentityFixture) *http.Request {
			return f.downloadRequest(f.task.ID, testFileSyncHash("config.yml"))
		}},
		{name: "target-result", request: func(t *testing.T, f *fileSyncIdentityFixture) *http.Request {
			t.Helper()
			return f.targetResultRequest(t, f.task.ID, f.targetCmd.ID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/未注入校验器失败关闭", func(t *testing.T) {
			f := newFileSyncIdentityFixture(t)
			f.handler.SetReportAuthenticator(nil)
			rec := f.do(t, tc.request(t, f))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("未注入身份校验器时必须失败关闭并返回 401，实际 %d：%s", rec.Code, rec.Body.String())
			}
			assertFileSyncNoSideEffect(t, f)
		})
		t.Run(tc.name+"/缺少身份头", func(t *testing.T) {
			f := newFileSyncIdentityFixture(t)
			f.auth.err = apperr.ErrUnauthorized
			rec := f.do(t, tc.request(t, f))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("缺少已确认身份时必须返回 401，实际 %d：%s", rec.Code, rec.Body.String())
			}
			assertFileSyncNoSideEffect(t, f)
		})
	}
}

// assertFileSyncNoSideEffect 断言一次被拒绝的数据面请求没有留下任何落地痕迹。
func assertFileSyncNoSideEffect(t *testing.T, f *fileSyncIdentityFixture) {
	t.Helper()
	if n := countFileSyncRows(t, f.db, &model.FileSyncFile{}, "task_id = ?", f.task.ID); n != 0 {
		t.Fatalf("被拒绝的请求不得写入源清单，实际 %d 行", n)
	}
	if n := countFileSyncRows(t, f.db, &model.FileSyncLog{}, "task_id = ?", f.task.ID); n != 0 {
		t.Fatalf("被拒绝的请求不得写入任务日志，实际 %d 行", n)
	}
	var task model.FileSyncTask
	if err := f.db.Where("id = ?", f.task.ID).First(&task).Error; err != nil {
		t.Fatalf("回查任务失败：%v", err)
	}
	if task.Status != model.FileSyncTaskStatusScanning || task.SourceReady {
		t.Fatalf("被拒绝的请求不得推进任务状态，实际 status=%s sourceReady=%v", task.Status, task.SourceReady)
	}
	var cmd model.AgentCommand
	if err := f.db.Where("id = ?", f.sourceCmd.ID).First(&cmd).Error; err != nil {
		t.Fatalf("回查源扫描命令失败：%v", err)
	}
	if cmd.Status != model.CommandStatusFetched {
		t.Fatalf("被拒绝的请求不得推进命令状态，实际 %s", cmd.Status)
	}
	var target model.FileSyncTarget
	if err := f.db.Where("id = ?", f.target.ID).First(&target).Error; err != nil {
		t.Fatalf("回查目标失败：%v", err)
	}
	if target.Status != model.FileSyncTargetStatusPending {
		t.Fatalf("被拒绝的请求不得推进目标状态，实际 %s", target.Status)
	}
	dir := filepath.Join(f.blobRoot, fmt.Sprintf("%d", f.task.ID))
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Fatalf("被拒绝的请求不得落盘 blob，实际目录有 %d 个文件", len(entries))
	}
}

// TestFileSyncManifestRejectsForeignOrMismatchedCommand 清单回传必须同时满足
// 「命令属于该 agent」与「URL taskId 与命令 payload 一致」，否则按命令不存在拒绝。
func TestFileSyncManifestRejectsForeignOrMismatchedCommand(t *testing.T) {
	t.Run("他人命令", func(t *testing.T) {
		f := newFileSyncIdentityFixture(t)
		f.auth.identity = fileSyncOtherIdentity()
		rec := f.do(t, f.manifestRequest(t, f.task.ID, f.sourceCmd.ID))
		assertFileSyncNotFoundNoSideEffect(t, rec, f)
	})
	t.Run("taskId 与命令 payload 不一致", func(t *testing.T) {
		f := newFileSyncIdentityFixture(t)
		f.auth.identity = fileSyncSourceIdentity()
		// 归属正确、但 URL 指向另一个任务：不得把 A 任务的命令结果落到 B 任务上。
		rec := f.do(t, f.manifestRequest(t, f.task.ID+1, f.sourceCmd.ID))
		assertFileSyncNotFoundNoSideEffect(t, rec, f)
	})
}

func assertFileSyncNotFoundNoSideEffect(t *testing.T, rec *httptest.ResponseRecorder, f *fileSyncIdentityFixture) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("越权 / 错配回传必须按不存在拒绝（404，不泄露存在性），实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), apperr.ErrCommandNotFound.Code) {
		t.Fatalf("越权 / 错配回传应返回 COMMAND_NOT_FOUND，实际：%s", rec.Body.String())
	}
	assertFileSyncNoSideEffect(t, f)
}

// TestFileSyncTargetResultRejectsForeignOrMismatchedCommand 目标结果回传同样只接受本人命令。
func TestFileSyncTargetResultRejectsForeignOrMismatchedCommand(t *testing.T) {
	t.Run("他人命令", func(t *testing.T) {
		f := newFileSyncIdentityFixture(t)
		f.prepareRunning(t)
		f.auth.identity = fileSyncOtherIdentity()
		rec := f.do(t, f.targetResultRequest(t, f.task.ID, f.targetCmd.ID))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("他人目标命令回传必须返回 404，实际 %d：%s", rec.Code, rec.Body.String())
		}
		assertTargetResultNoSideEffect(t, f)
	})
	t.Run("taskId 与命令 payload 不一致", func(t *testing.T) {
		f := newFileSyncIdentityFixture(t)
		f.prepareRunning(t)
		f.auth.identity = fileSyncTargetIdentity()
		rec := f.do(t, f.targetResultRequest(t, f.task.ID+1, f.targetCmd.ID))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("taskId 与命令 payload 不一致必须返回 404，实际 %d：%s", rec.Code, rec.Body.String())
		}
		assertTargetResultNoSideEffect(t, f)
	})
}

func assertTargetResultNoSideEffect(t *testing.T, f *fileSyncIdentityFixture) {
	t.Helper()
	var target model.FileSyncTarget
	if err := f.db.Where("id = ?", f.target.ID).First(&target).Error; err != nil {
		t.Fatalf("回查目标失败：%v", err)
	}
	if target.Status != model.FileSyncTargetStatusManifesting {
		t.Fatalf("被拒绝的目标回传不得推进目标状态，实际 %s", target.Status)
	}
	var cmd model.AgentCommand
	if err := f.db.Where("id = ?", f.targetCmd.ID).First(&cmd).Error; err != nil {
		t.Fatalf("回查目标命令失败：%v", err)
	}
	if cmd.Status != model.CommandStatusFetched {
		t.Fatalf("被拒绝的目标回传不得推进命令状态，实际 %s", cmd.Status)
	}
	if n := countFileSyncRows(t, f.db, &model.FileSyncLog{}, "task_id = ?", f.task.ID); n != 0 {
		t.Fatalf("被拒绝的目标回传不得写入任务日志，实际 %d 行", n)
	}
	var task model.FileSyncTask
	if err := f.db.Where("id = ?", f.task.ID).First(&task).Error; err != nil {
		t.Fatalf("回查任务失败：%v", err)
	}
	if task.Status != model.FileSyncTaskStatusRunning {
		t.Fatalf("被拒绝的目标回传不得推进任务状态，实际 %s", task.Status)
	}
}

// TestFileSyncBlobUploadRejectsNonSourceAgent blob 上传语义是「源 agent 推送源目录内容」，
// 故平台内任何非源服务器都不得向该 taskId 的缓存目录写字节。
func TestFileSyncBlobUploadRejectsNonSourceAgent(t *testing.T) {
	f := newFileSyncIdentityFixture(t)
	authHash := testFileSyncHash("config.yml")
	for _, identity := range []agentauth.Identity{fileSyncTargetIdentity(), fileSyncOtherIdentity()} {
		f.auth.identity = identity
		rec := f.do(t, f.uploadRequest(f.task.ID, authHash, []byte("config.yml")))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 非本源服务器，上传 blob 必须返回 404，实际 %d：%s", identity.ServerID, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(f.blobPath(authHash)); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的上传不得落盘 blob（stat 错误：%v）", err)
	}
}

// TestFileSyncBlobDownloadRejectsNonTargetAgent 下载语义是「目标 agent 拉取源目录内容副本」，
// 只对任务计划内的目标开放；源服务器与无关服务器都不得读。
func TestFileSyncBlobDownloadRejectsNonTargetAgent(t *testing.T) {
	f := newFileSyncIdentityFixture(t)
	hash := testFileSyncHash("config.yml")
	if rec := f.do(t, f.uploadRequest(f.task.ID, hash, []byte("config.yml"))); rec.Code != http.StatusNoContent {
		t.Fatalf("源 agent 预置 blob 应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	for _, identity := range []agentauth.Identity{fileSyncSourceIdentity(), fileSyncOtherIdentity()} {
		f.auth.identity = identity
		rec := f.do(t, f.downloadRequest(f.task.ID, hash))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 不是本任务目标，下载 blob 必须返回 404，实际 %d：%s", identity.ServerID, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "config.yml") {
			t.Fatalf("被拒绝的下载不得回吐文件内容，实际：%s", rec.Body.String())
		}
	}
}

// TestFileSyncDataPlaneAcceptsOwningAgents 正向路径：归属正确时四条端点都应正常工作。
func TestFileSyncDataPlaneAcceptsOwningAgents(t *testing.T) {
	f := newFileSyncIdentityFixture(t)
	f.auth.identity = fileSyncSourceIdentity()
	manifestRec := f.do(t, f.manifestRequest(t, f.task.ID, f.sourceCmd.ID))
	if manifestRec.Code != http.StatusNoContent {
		t.Fatalf("源 agent 回传本任务清单应返回 204，实际 %d：%s", manifestRec.Code, manifestRec.Body.String())
	}
	if n := countFileSyncRows(t, f.db, &model.FileSyncFile{}, "task_id = ?", f.task.ID); n != 1 {
		t.Fatalf("清单应写入 1 行，实际 %d 行", n)
	}
	var task model.FileSyncTask
	if err := f.db.Where("id = ?", f.task.ID).First(&task).Error; err != nil {
		t.Fatalf("回查任务失败：%v", err)
	}
	if !task.SourceReady {
		t.Fatal("清单写入后任务应标记源清单就绪")
	}

	content := []byte("binary\x00content\nwith-stream")
	hash := testFileSyncHash(string(content))
	if rec := f.do(t, f.uploadRequest(f.task.ID, hash, content)); rec.Code != http.StatusNoContent {
		t.Fatalf("源 agent 上传 blob 应返回 204，实际 %d：%s", rec.Code, rec.Body.String())
	}
	f.auth.identity = fileSyncTargetIdentity()
	downloadRec := f.do(t, f.downloadRequest(f.task.ID, hash))
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("目标 agent 下载 blob 应返回 200，实际 %d：%s", downloadRec.Code, downloadRec.Body.String())
	}
	if !bytes.Equal(downloadRec.Body.Bytes(), content) {
		t.Fatalf("下载内容应与上传字节一致，实际 %q", downloadRec.Body.String())
	}

	f.prepareRunning(t)
	f.auth.identity = fileSyncTargetIdentity()
	resultRec := f.do(t, f.targetResultRequest(t, f.task.ID, f.targetCmd.ID))
	if resultRec.Code != http.StatusNoContent {
		t.Fatalf("目标 agent 回传本人结果应返回 204，实际 %d：%s", resultRec.Code, resultRec.Body.String())
	}
	var target model.FileSyncTarget
	if err := f.db.Where("id = ?", f.target.ID).First(&target).Error; err != nil {
		t.Fatalf("回查目标失败：%v", err)
	}
	if target.Status != model.FileSyncTargetStatusSucceeded {
		t.Fatalf("目标回传成功后状态应为 succeeded，实际 %s", target.Status)
	}
}

// TestFileSyncBlobDownloadRejectsSameServerIDInOtherNamespace 验证同名实例跨命名空间不得读他人 blob。
//
// 动机：serverId 只在命名空间内唯一（Server 的唯一键是 (namespace_id, server_id)），
// 而目标表没有 namespace 列。只比对 serverId 的话，另一环境同名的实例可以凭可枚举的
// taskId 加上已知的 sha256 拉到本环境的文件正文。
func TestFileSyncBlobDownloadRejectsSameServerIDInOtherNamespace(t *testing.T) {
	f := newFileSyncIdentityFixture(t)
	hash := testFileSyncHash("config.yml")
	if rec := f.do(t, f.uploadRequest(f.task.ID, hash, []byte("config.yml"))); rec.Code != http.StatusNoContent {
		t.Fatalf("源 agent 预置 blob 应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// 与目标同名、但属于另一个命名空间的身份。
	f.auth.identity = agentauth.Identity{
		Namespace: "staging", ServerID: fileSyncTargetIdentity().ServerID,
		Kind: model.ServerKindBackend, IdentityID: "identity-staging-target",
	}
	rec := f.do(t, f.downloadRequest(f.task.ID, hash))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨命名空间同名实例下载必须返回 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "config.yml") {
		t.Fatalf("被拒绝的下载不得回吐文件内容，实际：%s", rec.Body.String())
	}
}

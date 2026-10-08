package service

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// —— FR-263：交付幂等语义收口（控制面侧契约）——
//
// 与波次 A（ADR-0088 / spec §4.6.4）的边界：agent 侧已定「同一条命令只发一条回执」，
// 本组在控制面侧定「重复到达时行为是什么」——两端合起来才是完整契约。

// TestDeliveryRedispatchReusesInFlightCommand 控制面重启后命令重发：已存在在途同键命令则复用，不建第二条。
// 幂等键 = (namespace, serverId, type, payload.orderId)。
func TestDeliveryRedispatchReusesInFlightCommand(t *testing.T) {
	db := newIntegrityDB(t, "redispatch")
	cmdRepo := repository.NewAgentCommandRepository(db)
	ns := "prod"
	payload := `{"orderId":7,"fileCount":3,"totalBytes":4096}`

	first := &model.AgentCommand{NamespaceCode: ns, ServerID: "t-1", Type: model.CommandTypeDeliveryPush,
		Payload: payload, Status: model.CommandStatusPending, Operator: "system"}
	if err := cmdRepo.Create(first); err != nil {
		t.Fatalf("建首条命令失败: %v", err)
	}
	// 模拟控制面重启后重跑下发：先按幂等键查在途命令。
	existing, err := cmdRepo.FindActiveByTypeAndOrder(ns, "t-1", model.CommandTypeDeliveryPush, 7)
	if err != nil {
		t.Fatalf("查在途同键命令失败: %v", err)
	}
	if existing == nil || existing.ID != first.ID {
		t.Fatalf("应命中在途同键命令（id=%d），实际 %+v", first.ID, existing)
	}
	// 不同单号的同类型命令不得被误命中（幂等键含 orderId）。
	other, err := cmdRepo.FindActiveByTypeAndOrder(ns, "t-1", model.CommandTypeDeliveryPush, 8)
	if err != nil {
		t.Fatalf("查异单命令失败: %v", err)
	}
	if other != nil {
		t.Fatalf("异单号不得命中同键命令，实际 %+v", other)
	}
}

// TestDeliveryRedispatchSkipsTerminalCommand 在途判定只看 pending/fetched：已终态命令不阻拦下一轮下发
// （否则目标重试 / 回滚再下发会被历史命令永久挡住）。
func TestDeliveryRedispatchSkipsTerminalCommand(t *testing.T) {
	db := newIntegrityDB(t, "redispatchdone")
	cmdRepo := repository.NewAgentCommandRepository(db)
	done := &model.AgentCommand{NamespaceCode: "prod", ServerID: "t-1", Type: model.CommandTypeDeliveryPush,
		Payload: `{"orderId":7}`, Status: model.CommandStatusDone, Operator: "system"}
	if err := cmdRepo.Create(done); err != nil {
		t.Fatalf("建终态命令失败: %v", err)
	}
	got, err := cmdRepo.FindActiveByTypeAndOrder("prod", "t-1", model.CommandTypeDeliveryPush, 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got != nil {
		t.Fatalf("已终态命令不应被算作在途，实际 %+v", got)
	}
}

// TestDeliveryDuplicateResultReceiptRejected 重复回执：第二条被前态 CAS 拒，且**不改变**已落定的状态与计数。
func TestDeliveryDuplicateResultReceiptRejected(t *testing.T) {
	svc, db := newIdempotencySvc(t, "dupreceipt")
	order := seedIdempotencyOrder(t, db, model.ChangeOrderStatusRolling, "src-1")
	cmd := seedFetchedCommand(t, db, "prod", "t-1", model.CommandTypeDeliveryPush, order.ID)

	id := agentauth.Identity{NamespaceID: order.NamespaceID, Namespace: "prod", ServerID: "t-1"}
	ok := DeliveryResultInput{Phase: DeliveryPhasePush, Status: DeliveryResultSuccess,
		ChangedFileCount: 3, SkippedFileCount: 1, BackupPresent: true}
	if err := svc.ReceiveResult(id, order.ID, ok); err != nil {
		t.Fatalf("首条回执应被接受，实际 %v", err)
	}
	// 首条落定后的事实快照。
	afterFirst := loadCommand(t, db, cmd.ID)
	if afterFirst.Status != model.CommandStatusDone {
		t.Fatalf("首条回执应置 done，实际 %s", afterFirst.Status)
	}
	// 第二条（agent 重发 / 控制面重复投递）必须被拒，且不改状态与 result_detail。
	err := svc.ReceiveResult(id, order.ID, DeliveryResultInput{Phase: DeliveryPhasePush,
		Status: DeliveryResultFailed, ChangedFileCount: 99, Error: "误报的失败"})
	if !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("重复回执应被拒（command_not_found），实际 %v", err)
	}
	afterSecond := loadCommand(t, db, cmd.ID)
	if afterSecond.Status != model.CommandStatusDone {
		t.Fatalf("重复回执不得改命令状态，实际 %s", afterSecond.Status)
	}
	if afterSecond.ResultDetail != afterFirst.ResultDetail {
		t.Fatalf("重复回执不得改 result_detail：\n首条=%s\n重复后=%s", afterFirst.ResultDetail, afterSecond.ResultDetail)
	}
	if strings.Contains(afterSecond.ResultDetail, "误报的失败") {
		t.Fatal("重复回执的失败原因不得覆盖已落定的成功事实")
	}
}

// TestDeliveryDuplicateResultDoesNotReWake 重复回执不得二次唤醒推进器（避免重复推进状态机）。
func TestDeliveryDuplicateResultDoesNotReWake(t *testing.T) {
	svc, db := newIdempotencySvc(t, "dupwake")
	order := seedIdempotencyOrder(t, db, model.ChangeOrderStatusRolling, "src-1")
	seedFetchedCommand(t, db, "prod", "t-1", model.CommandTypeDeliveryPush, order.ID)
	waker := &countingWaker{}
	svc.SetProgressWaker(waker)

	id := agentauth.Identity{NamespaceID: order.NamespaceID, Namespace: "prod", ServerID: "t-1"}
	input := DeliveryResultInput{Phase: DeliveryPhasePush, Status: DeliveryResultSuccess, BackupPresent: true}
	if err := svc.ReceiveResult(id, order.ID, input); err != nil {
		t.Fatalf("首条回执失败: %v", err)
	}
	if waker.calls != 1 {
		t.Fatalf("首条回执应唤醒 1 次，实际 %d", waker.calls)
	}
	// 第二条没有在途命令可挂 → 连唤醒都不应发生（唤醒是「回执落定」的副产物，不是「收到回执」的副产物）。
	if err := svc.ReceiveResult(id, order.ID, input); err == nil {
		t.Fatal("重复回执应被拒")
	}
	if waker.calls != 1 {
		t.Fatalf("重复回执不得二次唤醒推进器，实际 %d 次", waker.calls)
	}
}

// TestDeliveryConcurrentBlobReputIdempotent blob PUT 重传：并发上传同一内容只落一份、全部成功、字节一致。
func TestDeliveryConcurrentBlobReputIdempotent(t *testing.T) {
	svc, _ := newIdempotencySvc(t, "reput")
	content := []byte("delivery blob content for concurrent re-put")
	sha := shaOf(content)

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.Store(sha, int64(len(content)), bytes.NewReader(content))
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("第 %d 路并发重传应成功（幂等），实际 %v", i, errs[i])
		}
	}
	// 只落一份：磁盘一个文件、元数据一行、大小正确。
	size, err := svc.Head(sha)
	if err != nil || size != int64(len(content)) {
		t.Fatalf("重传后 Head 应就绪且大小正确，实际 size=%d err=%v", size, err)
	}
	var rows int64
	if err := svc.db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).Count(&rows).Error; err != nil {
		t.Fatalf("数元数据行失败: %v", err)
	}
	if rows != 1 {
		t.Fatalf("内容寻址应只落一行元数据，实际 %d 行", rows)
	}
	// 内容必须与声明一致（重传不得把别人的内容盖进来）。
	file, _, release, err := svc.Open(sha)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer release()
	got, _ := readAllFile(file)
	if !bytes.Equal(got, content) {
		t.Fatalf("重传后内容不一致：got=%q", got)
	}
}

// TestOrchestratorRedispatchReusesInFlightPush 端到端：控制面重启续跑（目标仍在 pending、命令已在途）
// 不得建第二条推送命令——重复命令会让 agent 收到两条 push，产生二次备份 / 二次覆盖。
func TestOrchestratorRedispatchReusesInFlightPush(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 100)
	if _, err := h.orch.applyStart(order.ID, "上线", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 首轮下发
	var before int64
	h.env.db.Model(&model.AgentCommand{}).Where("type = ?", model.CommandTypeDeliveryPush).Count(&before)
	if before != 2 {
		t.Fatalf("首轮应下发 2 条推送命令，实际 %d", before)
	}
	// 模拟控制面重启：目标回到 pending（未落库的中途状态），命令仍在途（fetched）。
	if err := h.env.db.Model(&model.ChangeTarget{}).Where("order_id = ?", order.ID).
		Update("status", model.ChangeTargetStatusPending).Error; err != nil {
		t.Fatalf("回退目标状态失败: %v", err)
	}
	h.tick() // 续跑重扫

	var after int64
	h.env.db.Model(&model.AgentCommand{}).Where("type = ?", model.CommandTypeDeliveryPush).Count(&after)
	if after != before {
		t.Fatalf("续跑重发不得新增推送命令：重发前 %d 条，重发后 %d 条", before, after)
	}
}

// TestDeliveryReputAfterReadyIsNoop 已 ready 的 blob 再 PUT（秒传路径）不重写磁盘、不改大小。
func TestDeliveryReputAfterReadyIsNoop(t *testing.T) {
	svc, _ := newIdempotencySvc(t, "reputready")
	content := []byte("already ready payload")
	sha := shaOf(content)
	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	firstStat := statOrFatal(t, svc.blobPath(sha))
	time.Sleep(2 * time.Millisecond) // 让 mtime 可辨（若重写则必变）
	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("秒传重传应幂等成功，实际 %v", err)
	}
	secondStat := statOrFatal(t, svc.blobPath(sha))
	if !secondStat.ModTime().Equal(firstStat.ModTime()) {
		t.Fatalf("秒传重传不得重写磁盘文件（mtime 变了：%v → %v）", firstStat.ModTime(), secondStat.ModTime())
	}
}

// —— 测试辅助 ——

// countingWaker 计数唤醒器（验证重复回执不二次唤醒）。
type countingWaker struct {
	mu    sync.Mutex
	calls int
}

func (w *countingWaker) WakeOrder(uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
}

// readAllFile 读尽一个已打开文件（测试小工具）。
func readAllFile(file *os.File) ([]byte, error) {
	return io.ReadAll(file)
}

// statOrFatal 取文件 stat，失败即终止测试。
func statOrFatal(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s 失败: %v", path, err)
	}
	return info
}

// newIdempotencySvc 装配幂等测试用数据面（临时 blob 根 + 宽松设置）。
func newIdempotencySvc(t *testing.T, name string) (*DeliveryBlobService, *gorm.DB) {
	t.Helper()
	db := newIntegrityDB(t, name)
	svc := NewDeliveryBlobService(db, repository.NewDeliveryBlobRepository(db),
		repository.NewChangeOrderRepository(db), repository.NewAgentCommandRepository(db), looseBlobSettings())
	svc.SetRoot(t.TempDir())
	return svc, db
}

// seedIdempotencyOrder 建一张指定状态、指定模板源的变更单（目标是 t-1）。
func seedIdempotencyOrder(t *testing.T, db *gorm.DB, status, source string) *model.ChangeOrder {
	t.Helper()
	order := model.ChangeOrder{NamespaceID: 1, Title: "idem", Status: status, SourceServerID: source}
	mustCreate(t, db, &order)
	mustCreate(t, db, &model.ChangeTarget{OrderID: order.ID, BatchID: 0, ServerID: "t-1",
		Status: model.ChangeTargetStatusPushing})
	return &order
}

// seedFetchedCommand 建一条 fetched 态交付命令（模拟已下发待回执）。
func seedFetchedCommand(t *testing.T, db *gorm.DB, ns, serverID, cmdType string, orderID uint) *model.AgentCommand {
	t.Helper()
	cmd := &model.AgentCommand{NamespaceCode: ns, ServerID: serverID, Type: cmdType,
		Payload: `{"orderId":` + itoa(orderID) + `}`, Status: model.CommandStatusFetched, Operator: "system"}
	mustCreate(t, db, cmd)
	return cmd
}

// loadCommand 重读一条命令的最新态。
func loadCommand(t *testing.T, db *gorm.DB, id uint) *model.AgentCommand {
	t.Helper()
	var cmd model.AgentCommand
	if err := db.Where("id = ?", id).First(&cmd).Error; err != nil {
		t.Fatalf("读命令失败: %v", err)
	}
	return &cmd
}

// itoa 小工具（避免为拼 payload 引 strconv 到多处）。
func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	return string(buf)
}

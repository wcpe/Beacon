package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestReadCommandNamespaceScope 同名 serverId 跨 namespace 不能互相读到 agent 日志正文。
// 修复前 ReadCommand 只比对裸 serverId，prod 作用域可读到 test 环境同名实例的日志。
func TestReadCommandNamespaceScope(t *testing.T) {
	db := newCommandSvcTestDB(t)
	ensureReadScopeTables(t, db)
	seedReadScopeServer(t, db, "prod", "lobby-1")
	seedReadScopeServer(t, db, "test", "lobby-1")
	// lobby-2 只存在于 prod，用于验证缺省 namespace 时按 serverId 反查仍可读（v1 路由兼容）。
	seedReadScopeServer(t, db, "prod", "lobby-2")
	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))

	const content = `[{"level":"info","text":"test 环境私有日志"}]`
	cmd := &model.AgentCommand{NamespaceCode: "test", ServerID: "lobby-1", Type: model.CommandTypeTailLogs, Status: model.CommandStatusDone, LogContent: content, Operator: "alice"}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建 test 环境日志命令失败: %v", err)
	}

	if _, err := logs.ReadCommand("prod", "lobby-1", cmd.ID, "alice", "127.0.0.1"); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("跨 namespace 读取必须按不存在拒绝，实际 %v", err)
	}
	// 缺省 namespace 且同名 serverId 跨环境共存时无法消歧，必须拒绝而不是猜一条。
	if _, err := logs.ReadCommand("", "lobby-1", cmd.ID, "alice", "127.0.0.1"); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("同名 serverId 跨环境歧义必须拒绝，实际 %v", err)
	}
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 0 {
		t.Fatalf("被拒绝的读取不得写成功审计，实际 %d 条", n)
	}

	got, err := logs.ReadCommand("test", "lobby-1", cmd.ID, "alice", "127.0.0.1")
	if err != nil || len(got.Lines) != 1 || got.Lines[0].Text != "test 环境私有日志" {
		t.Fatalf("正确 namespace 应读到日志：got=%+v err=%v", got, err)
	}

	// 唯一归属的 serverId 在缺省 namespace 时按反查结果读取，保持旧调用方兼容。
	unique := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-2", Type: model.CommandTypeTailLogs, Status: model.CommandStatusDone, LogContent: content, Operator: "alice"}
	if err := db.Create(unique).Error; err != nil {
		t.Fatalf("创建 prod 环境日志命令失败: %v", err)
	}
	if got, err := logs.ReadCommand("", "lobby-2", unique.ID, "alice", "127.0.0.1"); err != nil || len(got.Lines) != 1 {
		t.Fatalf("唯一归属实例应在缺省 namespace 时反查成功：got=%+v err=%v", got, err)
	}
}

// slowCommitPool 是「提交变慢」的 gorm 连接池包装（仅测试用）：
// 在事务 COMMIT 之前插入固定延迟，把「业务代码已返回但事务尚未提交」的窗口放大到可稳定观察。
//
// 为什么需要它：限速锁错位的本质窗口是「回调返回 → 解除锁 → 事务 COMMIT」之间那几微秒，
// 正常情况下第二个 goroutine 在 COMMIT 完成后才被调度，缺陷不会暴露（实测缺陷版也能通过）。
// 把 COMMIT 拖慢后，窗口内的第二个 goroutine 一定能进入自己的事务。
type slowCommitPool struct {
	gorm.ConnPool
	delay time.Duration
}

// BeginTx 开启事务并把提交包装成可延迟的实现。
func (p slowCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	switch beginner := p.ConnPool.(type) {
	case gorm.ConnPoolBeginner:
		pool, err := beginner.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &slowCommitTx{ConnPool: pool, delay: p.delay}, nil
	case gorm.TxBeginner:
		tx, err := beginner.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &slowCommitTx{ConnPool: tx, delay: p.delay}, nil
	default:
		return nil, errors.New("连接池既不支持 ConnPoolBeginner 也不支持 TxBeginner")
	}
}

// slowCommitTx 在真正提交前等待 delay，其余行为全部委托给底层事务。
// 必须用指针接收者：gorm 的 Commit / Rollback 会对 ConnPool 做 reflect IsNil 判空，
// 值类型会直接 panic。
type slowCommitTx struct {
	gorm.ConnPool
	delay time.Duration
}

func (t *slowCommitTx) Commit() error {
	time.Sleep(t.delay)
	return t.ConnPool.(gorm.TxCommitter).Commit()
}

func (t *slowCommitTx) Rollback() error {
	return t.ConnPool.(gorm.TxCommitter).Rollback()
}

// newSlowCommitCommandDB 打开一个独立的临时文件 sqlite，并把连接池换成「提交变慢」的包装。
//
// 为什么不用包内共用的 `file::memory:?cache=shared`：共享缓存模式下 SQLite 会放宽隔离，
// 一个连接能读到另一个连接**未提交**的行，缺陷（锁在提交前释放）反而被掩盖成「恰好只建一条」。
// 文件库是真正的连接级隔离，配合 busy_timeout 才能如实暴露重复插入。
// 为什么要换连接池而不是直接改 gorm 的 ConnPool 字段：root DB 的 Statement.ConnPool 在打开时
// 已经固化，事后赋值不生效；glebarez 的 Dialector.Conn 允许注入自定义 pool，是唯一干净的入口。
func newSlowCommitCommandDB(t *testing.T, delay time.Duration) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "agent-log-lock.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	raw, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("打开 sqlite 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db, err := gorm.Open(&sqlite.Dialector{DSN: dsn, Conn: slowCommitPool{ConnPool: raw, delay: delay}}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 gorm 失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestRequestTailLogsLockHeldAcrossCommit 验证单活跃限速的锁跨越事务提交持有。
//
// 只断言「并发 N 次只建一条命令」不足以锁住本缺陷：缺陷窗口只有几微秒，
// 第二个 goroutine 通常要等 COMMIT 完成后才被调度，测试会假通过（实测缺陷版 PASS）。
// 这里用 slowCommitPool 把 COMMIT 拖慢，让窗口可稳定观察：
//   - 锁位错误（锁在事务回调内获取）：第二个 goroutine 在 COMMIT 之前就能进入自己的事务，
//     读不到未提交行（0 条活跃命令）→ 重复建命令（或撞 SQLite 表锁报错），断言失败；
//   - 锁跨提交持有：第二个 goroutine 必须等第一个提交完并解锁才能进入，必然读到 1 条 → 命中限速。
func TestRequestTailLogsLockHeldAcrossCommit(t *testing.T) {
	// 租约式放大：COMMIT 至少晚于业务返回 delay 才真正落库。
	const commitDelay = 120 * time.Millisecond
	db := newSlowCommitCommandDB(t, commitDelay)

	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := logs.RequestTailLogs("prod", "lobby-1", "alice", "127.0.0.1")
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, apperr.ErrAgentLogActive) {
			t.Fatalf("除首个请求外只能收到 active 限速错误，实际 %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("同一目标并发只允许一次成功，实际 %d 次", successes)
	}
	var total int64
	if err := db.Model(&model.AgentCommand{}).Where("namespace = ? AND server_id = ? AND type = ?",
		"prod", "lobby-1", model.CommandTypeTailLogs).Count(&total).Error; err != nil || total != 1 {
		t.Fatalf("数据库应只有一条日志命令，实际 %d 条 err=%v", total, err)
	}
	var active int64
	if err := db.Model(&model.AgentCommand{}).Where("namespace = ? AND server_id = ? AND type = ? AND status IN ?",
		"prod", "lobby-1", model.CommandTypeTailLogs, []string{model.CommandStatusPending, model.CommandStatusFetched}).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("数据库应只有一条活跃日志命令，实际 %d 条 err=%v", active, err)
	}
}

// TestReadCommandAuditFailFailsRequest 验证读取日志前审计写失败即拒绝返回日志正文。
//
// 与浏览 / payload 直读同构的负向护栏：日志改为直读后不再有审批门槛，
// 「看了必留痕」就是唯一约束，审计失败必须 fail-closed，绝不能把正文当成功结果返回。
func TestReadCommandAuditFailFailsRequest(t *testing.T) {
	db := newCommandSvcTestDB(t)
	ensureReadScopeTables(t, db)
	seedReadScopeServer(t, db, "prod", "lobby-1")
	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeTailLogs,
		Status: model.CommandStatusDone, LogContent: `[{"level":"error","text":"内部错误原文"}]`, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建日志命令失败: %v", err)
	}
	failAuditCreate(t, db, errors.New("模拟审计不可用"))

	result, err := logs.ReadCommand("prod", "lobby-1", cmd.ID, "alice", "127.0.0.1")
	if err == nil {
		t.Fatal("审计写失败必须拒绝返回日志正文")
	}
	if result != nil && len(result.Lines) != 0 {
		t.Fatalf("审计写失败时绝不应返回日志行，实际 %+v", result.Lines)
	}
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 0 {
		t.Fatalf("审计失败不得留下成功读取审计，实际 %d 条", n)
	}
}

// TestReadCommandRejectsUnavailableCommand 验证不存在 / 类型不符 / 未完成的命令一律不返回日志正文。
func TestReadCommandRejectsUnavailableCommand(t *testing.T) {
	db := newCommandSvcTestDB(t)
	ensureReadScopeTables(t, db)
	seedReadScopeServer(t, db, "prod", "lobby-1")
	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))

	// 不存在：按不存在拒绝，不泄露是否曾有过该命令。
	if result, err := logs.ReadCommand("prod", "lobby-1", 999999, "alice", "127.0.0.1"); !errors.Is(err, apperr.ErrCommandNotFound) || result != nil {
		t.Fatalf("不存在的命令应拒绝，实际 result=%+v err=%v", result, err)
	}
	cases := []struct {
		name string
		cmd  *model.AgentCommand
	}{
		{name: "类型不符", cmd: &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeIngestPlugins, Status: model.CommandStatusDone, LogContent: `[]`}},
	}
	for _, tc := range cases {
		if err := db.Create(tc.cmd).Error; err != nil {
			t.Fatalf("创建命令失败（%s）: %v", tc.name, err)
		}
		result, err := logs.ReadCommand("prod", "lobby-1", tc.cmd.ID, "alice", "127.0.0.1")
		if !errors.Is(err, apperr.ErrCommandNotFound) || (result != nil && len(result.Lines) != 0) {
			t.Fatalf("%s 应拒绝且不返回日志行，实际 result=%+v err=%v", tc.name, result, err)
		}
	}
	// 被拒绝的读取不得留下成功审计（此处计数须在「未完成状态读」之前断言，后者会正常记一条）。
	if n := countBrowseAudit(t, db, model.ActionInstanceTailLogsRead); n != 0 {
		t.Fatalf("被拒绝的读取不得写成功审计，实际 %d 条", n)
	}
	// 未完成的命令允许返回状态（前端据此显示「进行中」），但必须没有任何日志正文。
	pending := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-2", Type: model.CommandTypeTailLogs, Status: model.CommandStatusPending, LogContent: `[]`, Operator: "alice"}
	seedReadScopeServer(t, db, "prod", "lobby-2")
	if err := db.Create(pending).Error; err != nil {
		t.Fatalf("创建未完成命令失败: %v", err)
	}
	if result, err := logs.ReadCommand("prod", "lobby-2", pending.ID, "alice", "127.0.0.1"); err != nil || len(result.Lines) != 0 {
		t.Fatalf("未完成命令只应返回状态、不得带出日志行，实际 result=%+v err=%v", result, err)
	}
}

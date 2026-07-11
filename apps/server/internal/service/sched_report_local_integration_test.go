//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// waitSchedRows 轮询等待某决策日表行数达到期望（异步写入池 flush 后落库）。
func waitSchedRows(t *testing.T, db *gorm.DB, name string, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if db.Migrator().HasTable(name) {
			var n int64
			if err := db.Table(name).Count(&n).Error; err == nil && n == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	var n int64
	if db.Migrator().HasTable(name) {
		_ = db.Table(name).Count(&n).Error
	}
	t.Fatalf("超时：%s 期望 %d 行，实际 %d", name, want, n)
}

// TestSchedReportLocalReplayUniqueMySQL 真 MySQL：补报经异步通道入库；重放（内存判重）与
// 绕过内存判重的重放（DB trace_id 唯一索引兜底）最终库内均不重复。
func TestSchedReportLocalReplayUniqueMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr146_svc")
	now := time.Now().UTC()
	name := store.DailyTableName("sched_decision", now)
	_ = db.Migrator().DropTable(name)

	repo := repository.NewSchedDecisionV2Repository(db)
	writer := NewAsyncDailyWriter()
	writer.flushInterval = 100 * time.Millisecond // 加速 flush
	RegisterFlusher(writer, RouteKindSchedDecision, repo.FlushDaily)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer.Start(ctx)

	id := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "int-req-1", Kind: model.ServerKindBackend}
	batch := []LocalDecisionReport{
		{LocalTraceID: "it-local-1", TsMs: now.UnixMilli(), Zone: "area-1", ChosenServerID: "s-a"},
		{LocalTraceID: "it-local-2", TsMs: now.UnixMilli() + 1000, Zone: "area-1", FailReason: "no_candidate"},
	}

	svc := NewSchedulingV2Service(healthview.NewStore(), nil)
	svc.SetDecisionEnqueuer(SchedDecisionEnqueuer{Writer: writer})
	res, err := svc.ReportLocal(id, batch)
	if err != nil || res.Accepted != 2 {
		t.Fatalf("首报应接收 2，实际 %+v err=%v", res, err)
	}
	waitSchedRows(t, db, name, 2)

	// 同服务重放 → 内存判重集合拦截。
	res, err = svc.ReportLocal(id, batch)
	if err != nil || res.Accepted != 0 || res.Deduplicated != 2 {
		t.Fatalf("重放应全判重，实际 %+v err=%v", res, err)
	}

	// 新服务实例（判重集合清空，模拟控制面重启）重放 → 入队成功但 DB 唯一索引兜底去重。
	svc2 := NewSchedulingV2Service(healthview.NewStore(), nil)
	svc2.SetDecisionEnqueuer(SchedDecisionEnqueuer{Writer: writer})
	res, err = svc2.ReportLocal(id, batch)
	if err != nil || res.Accepted != 2 {
		t.Fatalf("绕过内存判重的重放应入队 2，实际 %+v err=%v", res, err)
	}
	// 给足两轮 flush 时间后行数仍为 2（不重复）。
	time.Sleep(400 * time.Millisecond)
	waitSchedRows(t, db, name, 2)

	// 行内容抽查：source=local_fallback、trace_id=localTraceId、归属取权威身份。
	var got model.SchedDecisionV2
	if err := db.Table(name).Where("trace_id = ?", "it-local-1").Take(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Source != SchedSourceLocalFallback || got.RequesterServerID != "int-req-1" ||
		got.NamespaceID != 1 || got.ChosenServerID != "s-a" || got.ChosenScore != -1 {
		t.Fatalf("补报行内容不符: %+v", got)
	}
}
